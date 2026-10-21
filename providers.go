package main

// Cloud API protocols and signing are adapted from OpenList drivers (AGPL-3.0).
// See THIRD_PARTY.md for source revisions and scope.
import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type object = map[string]any
type PlaybackTarget struct {
	URL     string
	Headers http.Header
	Mode    string
}
type Clouds interface {
	List(context.Context, string, string) ([]Entry, error)
	Resolve(context.Context, Media) (PlaybackTarget, error)
}

type expectedAccountRevisionKey struct{}
type expectedAccountRevision struct {
	ID       string
	Revision int64
}
type Providers struct {
	s      *Store
	client *http.Client
	locks  sync.Map
	// key115 overrides the built-in 115 public key; production leaves it nil
	// and uses defaultCipher115Public. It exists so isolated tests can run
	// the download protocol against a key pair they control.
	key115 *rsa.PublicKey
	// refreshed139 throttles 139 token refreshes per account (see prepare):
	// the 15-day early-refresh window would otherwise re-refresh on every
	// page of a long scan whenever the renewed token stays inside it.
	refreshMu    sync.Mutex
	refreshed139 map[string]time.Time
}

func newProviders(s *Store) *Providers {
	return &Providers{s: s, client: &http.Client{Timeout: 40 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}, refreshed139: map[string]time.Time{}}
}
func str(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}
func num(v any) int64 { n, _ := strconv.ParseInt(str(v), 10, 64); return n }
func obj(v any) object {
	if x, ok := v.(map[string]any); ok {
		return x
	}
	return object{}
}
func arr(v any) []any {
	if x, ok := v.([]any); ok {
		return x
	}
	return []any{}
}
func safeCode(v any) string {
	x := str(v)
	if len(x) > 64 {
		return "unknown"
	}
	for _, c := range x {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-", c) {
			return "unknown"
		}
	}
	return x
}
func networkError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &CloudError{Kind: "network", Code: "timeout", Retryable: true, Message: "网盘请求超时，请稍后重试", cause: context.DeadlineExceeded}
	}
	return &CloudError{Kind: "network", Code: "connection", Retryable: true, Message: "网盘连接失败，请检查容器网络或授权服务"}
}
func (p *Providers) lock(id string) *accountGate {
	v, _ := p.locks.LoadOrStore(id, &accountGate{})
	return v.(*accountGate)
}
func (p *Providers) with(ctx context.Context, id string, fn func(*Account) error) (err error) {
	mu := p.lock(id)
	high, _ := ctx.Value(playbackPriorityKey{}).(bool)
	if err = mu.acquire(ctx, high); err != nil {
		return err
	}
	defer mu.Unlock()
	a, e := p.s.account(id)
	if e != nil {
		return errors.New("账号不存在或凭据不可用")
	}
	if a.Disabled {
		return errors.New("此网盘已停用，请先在网盘管理中启用")
	}
	if expected, ok := ctx.Value(expectedAccountRevisionKey{}).(expectedAccountRevision); ok && (expected.ID != a.ID || expected.Revision != a.Revision) {
		return &CloudError{Kind: "configuration", Code: "account_changed", Message: "检查期间账号配置已改变，请重新诊断"}
	}
	before, _ := json.Marshal(a.Secrets)
	defer func() {
		after, _ := json.Marshal(a.Secrets)
		if !bytes.Equal(before, after) {
			if e := p.s.saveAccount(a); e != nil && err == nil {
				// Only a successful operation may be reclassified as a
				// storage failure; when fn already failed, keep its error
				// so health classification reflects the real cause.
				err = &CloudError{Kind: "storage", Code: "credential_save", Message: "凭据续期后保存失败，请检查数据卷"}
			}
		}
	}()
	if err = p.prepare(ctx, &a); err != nil {
		return err
	}
	return fn(&a)
}
func (p *Providers) request(ctx context.Context, method, u string, body any, headers http.Header) (object, error) {
	return p.requestCookies(ctx, method, u, body, headers, nil)
}
func (p *Providers) requestCookies(ctx context.Context, method, u string, body any, headers http.Header, receive func([]*http.Cookie)) (object, error) {
	return p.doRequest(ctx, method, u, body, headers, receive, false)
}

// requestRead is requestCookies for read-only provider queries (directory
// listing and download resolution). Transient failures are retried with
// backoff so one rate-limit response cannot fail a whole scan or playback
// start. Never route credential refresh or other secret-rotating calls
// through it: a lost response would make a retry reuse stale secrets.
func (p *Providers) requestRead(ctx context.Context, method, u string, body any, headers http.Header, receive func([]*http.Cookie)) (object, error) {
	return p.doRequest(ctx, method, u, body, headers, receive, true)
}

func retryableCloud(err error) bool {
	var cloud *CloudError
	return errors.As(err, &cloud) && cloud.Retryable
}

func retryBackoff(err error, attempt int) time.Duration {
	var cloud *CloudError
	if errors.As(err, &cloud) && cloud.RetryAfter > 0 {
		return min(cloud.RetryAfter, 5*time.Second)
	}
	return 300 * time.Millisecond << (attempt - 1)
}

func (p *Providers) doRequest(ctx context.Context, method, u string, body any, headers http.Header, receive func([]*http.Cookie), retry bool) (object, error) {
	var raw []byte
	contentType := "application/json"
	if body != nil {
		var e error
		if form, ok := body.(url.Values); ok {
			raw = []byte(form.Encode())
			contentType = "application/x-www-form-urlencoded"
		} else {
			raw, e = json.Marshal(body)
		}
		if e != nil {
			return nil, e
		}
	}
	attempts := 1
	if retry {
		attempts = 3
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(retryBackoff(lastErr, attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		out, err := p.doRequestOnce(ctx, method, u, raw, contentType, body != nil, headers, receive)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !retry || !retryableCloud(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (p *Providers) doRequestOnce(ctx context.Context, method, u string, raw []byte, contentType string, hasBody bool, headers http.Header, receive func([]*http.Cookie)) (object, error) {
	// A small per-request delay keeps recursive scans from flooding provider APIs.
	timer := time.NewTimer(180 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	req, e := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(raw))
	if e != nil {
		return nil, errors.New("无效的网盘接口地址")
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Accept", "application/json")
	if hasBody {
		req.Header.Set("Content-Type", contentType)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0")
	}
	resp, e := p.client.Do(req)
	if e != nil {
		return nil, networkError(e)
	}
	defer resp.Body.Close()
	if receive != nil {
		receive(resp.Cookies())
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, cloudHTTPError(resp.StatusCode, resp.Header.Get("Retry-After"), time.Now())
	}
	// Provider JSON answers are small (one page or one ticket); cap the body
	// well below the old 16MB and require exactly one JSON value so trailing
	// garbage from a confused upstream cannot be accepted silently.
	b, e := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if e != nil {
		return nil, networkError(e)
	}
	if len(b) > 4<<20 {
		return nil, cloudProtocolError("网盘接口响应过大")
	}
	var out object
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if dec.Decode(&out) != nil || out == nil {
		return nil, cloudProtocolError("网盘接口响应格式异常")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, cloudProtocolError("网盘接口响应格式异常")
	}
	return out, nil
}
func headers(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		h.Set(k, v)
	}
	return h
}
func queryURL(base string, q map[string]string) string {
	u, _ := url.Parse(base)
	v := u.Query()
	for k, x := range q {
		v.Set(k, x)
	}
	u.RawQuery = v.Encode()
	return u.String()
}
func apiSuccess(r object) error {
	if c := str(r["code"]); c != "" && c != "0" && c != "200" {
		// The generic "code" field is provider-specific business data; some
		// providers use numbers that merely look like HTTP statuses (e.g.
		// "500" for a business refusal), so it must not inherit HTTP
		// semantics. Only the "status" field below mirrors HTTP status.
		return cloudBusinessError(c)
	}
	if c := num(r["errno"]); c != 0 {
		return &CloudError{Kind: "other", Code: strconv.FormatInt(c, 10), Message: fmt.Sprintf("网盘接口错误 %d，请检查账号权益或通道状态", c)}
	}
	if n := num(r["status"]); n >= 400 || n < 0 {
		return cloudAPIError(str(r["status"]))
	}
	return nil
}

func (p *Providers) prepare(ctx context.Context, a *Account) error {
	if a.Secrets == nil {
		a.Secrets = map[string]string{}
	}
	switch a.Provider {
	case "115":
		cookie, e := validate115Cookie(a.Secrets["cookie"])
		if e != nil {
			return e
		}
		a.Secrets["cookie"] = cookie
	case "139", "139old":
		a.Secrets["authorization"] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(a.Secrets["authorization"]), "Basic "))
		decoded, e := base64.StdEncoding.DecodeString(a.Secrets["authorization"])
		parts := strings.SplitN(string(decoded), ":", 3)
		if e != nil || len(parts) != 3 {
			return errors.New("移动 Authorization 格式错误，应导入 Basic 后面的完整值")
		}
		a.Secrets["phone"] = parts[1]
		tokenParts := strings.Split(parts[2], "|")
		if len(tokenParts) >= 4 {
			exp, _ := strconv.ParseInt(tokenParts[3], 10, 64)
			if exp > 0 && exp < time.Now().Add(15*24*time.Hour).UnixMilli() {
				if exp < time.Now().UnixMilli() {
					return errors.New("移动 Authorization 已过期，请重新从官网登录后导入")
				}
				// prepare runs once per page during a scan; if the renewed
				// token still lands inside the 15-day window, refreshing on
				// every page would flood the refresh endpoint. Refresh at
				// most once per hour per account (in-process state only, so
				// the throttle itself never triggers a credential write).
				if !p.throttled139(a.ID) {
					if e = p.refresh139(ctx, a, parts); e != nil {
						return e
					}
					p.markRefreshed139(a.ID)
				}
			}
		}
		if a.Provider == "139" && a.Secrets["api_host"] == "" {
			r, e := p.mobile(ctx, a, "https://user-njs.yun.139.com/user/route/qryRoutePolicy", object{"userInfo": object{"userType": 1, "accountType": 1, "accountName": a.Secrets["phone"]}, "modAddrType": 1})
			if e != nil {
				return e
			}
			for _, v := range arr(obj(r["data"])["routePolicyList"]) {
				x := obj(v)
				if str(x["modName"]) == "personal" {
					u := str(x["httpsUrl"])
					parsed, _ := url.Parse(u)
					if parsed != nil && parsed.Scheme == "https" && (strings.HasSuffix(parsed.Hostname(), ".139.com") || strings.HasSuffix(parsed.Hostname(), ".10086.cn")) {
						a.Secrets["api_host"] = strings.TrimRight(u, "/")
					}
				}
			}
			if a.Secrets["api_host"] == "" {
				return errors.New("移动云盘未返回个人云路由，请确认所选个人云版本")
			}
		}
	case "quark":
		if a.Secrets["cookie"] == "" {
			return errors.New("请导入夸克网页登录后的 Cookie")
		}
	case "quarktv":
		if a.Secrets["device_id"] == "" {
			a.Secrets["device_id"] = randomID()
		}
		exp, _ := strconv.ParseInt(a.Secrets["expires_at"], 10, 64)
		if a.Secrets["access_token"] == "" || (exp > 0 && exp < time.Now().Add(2*time.Minute).Unix()) {
			if e := p.refreshTV(ctx, a); e != nil {
				return e
			}
		}
	case "ali":
		exp, _ := strconv.ParseInt(a.Secrets["expires_at"], 10, 64)
		if a.Secrets["access_token"] == "" || (exp > 0 && exp < time.Now().Add(2*time.Minute).Unix()) {
			if e := p.refreshAli(ctx, a); e != nil {
				return e
			}
		}
		if a.Drive == "" {
			return errors.New("请填写阿里 OpenAPI 的 drive_id（资源库或备份盘 ID）")
		}
	default:
		return errors.New("不支持的网盘类型")
	}
	return nil
}
func (p *Providers) throttled139(id string) bool {
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()
	last, ok := p.refreshed139[id]
	return ok && time.Since(last) < time.Hour
}

func (p *Providers) markRefreshed139(id string) {
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()
	if p.refreshed139 == nil {
		p.refreshed139 = map[string]time.Time{}
	}
	p.refreshed139[id] = time.Now()
}

func (p *Providers) refresh139(ctx context.Context, a *Account, parts []string) error {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(parts[2]))
	token := escaped.String()
	escaped.Reset()
	_ = xml.EscapeText(&escaped, []byte(parts[1]))
	body := "<root><token>" + token + "</token><account>" + escaped.String() + "</account><clienttype>656</clienttype></root>"
	req, e := http.NewRequestWithContext(ctx, "POST", "https://aas.caiyun.feixin.10086.cn/tellin/authTokenRefresh.do", strings.NewReader(body))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/xml")
	resp, e := p.client.Do(req)
	if e != nil {
		return networkError(e)
	}
	defer resp.Body.Close()
	var x struct {
		Return string `xml:"return"`
		Token  string `xml:"token"`
	}
	if resp.StatusCode != 200 {
		return cloudHTTPError(resp.StatusCode, resp.Header.Get("Retry-After"), time.Now())
	}
	if xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&x) != nil {
		return cloudProtocolError("移动续期接口返回格式异常，请稍后重试")
	}
	if x.Return != "0" || x.Token == "" {
		return &CloudError{Kind: "other", Code: "refresh_rejected", Message: "移动续期请求未成功，请核对账号与通道状态"}
	}
	// Validate the renewed token with the same rules prepare() parses by
	// before replacing the stored credential: if the refresh service ever
	// changes its response shape, keep the old (still working) token instead
	// of persisting a value the next prepare() would reject outright.
	if original := strings.Split(parts[2], "|"); len(original) >= 4 {
		renewed := strings.Split(x.Token, "|")
		valid := false
		if len(renewed) >= 4 {
			exp, perr := strconv.ParseInt(renewed[3], 10, 64)
			valid = perr == nil && exp > 0
		}
		if !valid {
			return &CloudError{Kind: "protocol", Code: "refresh_shape", Message: "移动续期返回的令牌格式异常，已保留原凭据，请稍后重试"}
		}
	}
	a.Secrets["authorization"] = base64.StdEncoding.EncodeToString([]byte(parts[0] + ":" + parts[1] + ":" + x.Token))
	return nil
}
func mobileSign(body, ts, nonce string) string {
	enc := url.QueryEscape(body)
	rep := strings.NewReplacer("+", "%20", "%21", "!", "%27", "'", "%28", "(", "%29", ")", "%2A", "*")
	chars := strings.Split(rep.Replace(enc), "")
	sort.Strings(chars)
	first := md5.Sum([]byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(chars, "")))))
	second := md5.Sum([]byte(ts + ":" + nonce))
	last := md5.Sum([]byte(hex.EncodeToString(first[:]) + hex.EncodeToString(second[:])))
	return strings.ToUpper(hex.EncodeToString(last[:]))
}
func (p *Providers) mobile(ctx context.Context, a *Account, u string, body any) (object, error) {
	raw, _ := json.Marshal(body)
	ts := time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")
	nonce := randomID()[:16]
	h := headers(map[string]string{"Authorization": "Basic " + a.Secrets["authorization"], "Caller": "web", "CMS-DEVICE": "default", "Mcloud-Channel": "1000101", "Mcloud-Client": "10701", "Mcloud-Route": "001", "Mcloud-Sign": ts + "," + nonce + "," + mobileSign(string(raw), ts, nonce), "Mcloud-Version": "7.14.0", "Origin": "https://yun.139.com", "Referer": "https://yun.139.com/w/", "x-DeviceInfo": "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||", "x-huawei-channelSrc": "10000034", "x-inner-ntwk": "2", "x-m4c-caller": "PC", "x-m4c-src": "10002", "x-SvcType": "1", "X-Yun-Api-Version": "v1", "X-Yun-App-Channel": "10000034", "X-Yun-Channel-Source": "10000034", "X-Yun-Client-Info": "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||dW5kZWZpbmVk||", "X-Yun-Module-Type": "100", "X-Yun-Svc-Type": "1"})
	r, e := p.requestRead(ctx, "POST", u, body, h, nil)
	if e != nil {
		return nil, e
	}
	if r["success"] != true {
		return nil, fmt.Errorf("移动云盘接口失败（%s），请检查 Authorization 或个人云版本", safeCode(r["code"]))
	}
	return r, nil
}
func (p *Providers) quark(ctx context.Context, a *Account, method, path string, body any, q map[string]string) (object, error) {
	if q == nil {
		q = map[string]string{}
	}
	q["pr"] = "ucpro"
	q["fr"] = "pc"
	r, e := p.requestRead(ctx, method, queryURL("https://drive.quark.cn/1/clouddrive"+path, q), body, headers(map[string]string{"Cookie": a.Secrets["cookie"], "Referer": "https://pan.quark.cn/", "User-Agent": quarkUA}), func(cookies []*http.Cookie) {
		for _, c := range cookies {
			if c.Name != "__puus" && c.Name != "__pus" {
				continue
			}
			if c.Value == "" {
				continue
			}
			parts := strings.Split(a.Secrets["cookie"], ";")
			found := false
			for i, part := range parts {
				if strings.TrimSpace(strings.SplitN(part, "=", 2)[0]) == c.Name {
					parts[i] = c.Name + "=" + c.Value
					found = true
				}
			}
			if !found {
				parts = append(parts, c.Name+"="+c.Value)
			}
			a.Secrets["cookie"] = strings.Join(parts, ";")
		}
	})
	if e != nil {
		return nil, e
	}
	return r, apiSuccess(r)
}

const quarkUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch"
const quarkTVUA = "Mozilla/5.0 (Linux; U; Android 13; zh-cn; M2004J7AC Build/UKQ1.231108.001) AppleWebKit/533.1 (KHTML, like Gecko) Mobile Safari/533.1"

func (p *Providers) tv(ctx context.Context, a *Account, path string, q map[string]string) (object, error) {
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	hash := sha256.Sum256([]byte("GET&" + path + "&" + ts + "&kw2dvtd7p4t3pjl2d9ed9yc8yej8kw2d"))
	rid := md5.Sum([]byte(a.Secrets["device_id"] + ts))
	for k, v := range tvDevice(a) {
		q[k] = v
	}
	q["access_token"] = a.Secrets["access_token"]
	q["req_id"] = hex.EncodeToString(rid[:])
	r, e := p.requestRead(ctx, "GET", queryURL("https://open-api-drive.quark.cn"+path, q), nil, headers(map[string]string{"User-Agent": quarkTVUA, "x-pan-tm": ts, "x-pan-token": hex.EncodeToString(hash[:]), "x-pan-client-id": "d3194e61504e493eb6222857bccfed94"}), nil)
	if e != nil {
		return nil, e
	}
	return r, apiSuccess(r)
}
func tvDevice(a *Account) map[string]string {
	return map[string]string{"device_id": a.Secrets["device_id"], "app_ver": "1.8.2.2", "device_brand": "Xiaomi", "platform": "tv", "device_name": "M2004J7AC", "device_model": "M2004J7AC", "build_device": "M2004J7AC", "build_product": "M2004J7AC", "device_gpu": "Adreno (TM) 550", "activity_rect": "{}", "channel": "GENERAL"}
}

// validateRefreshEndpoint guards the one place where a long-lived
// credential (the Quark TV refresh_token) is sent to a user-configured URL.
// Official Quark hosts are trusted by name; anything else must resolve
// exclusively to public addresses, using the same non-public definition as
// the media client, so the token can never be posted to a LAN or loopback
// service by a crafted or mistyped account configuration.
func validateRefreshEndpoint(ctx context.Context, u *url.URL) error {
	host := u.Hostname()
	if host == "quark.cn" || strings.HasSuffix(host, ".quark.cn") {
		return nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return errors.New("TV 续期服务地址无法解析，已拒绝发送续期凭据")
	}
	for _, v := range ips {
		addr, ok := netip.AddrFromSlice(v.IP)
		if !ok || isNonPublicIP(addr) {
			return errors.New("TV 续期服务地址指向内网或本机地址，已拒绝发送续期凭据；自建续期服务请使用公网 HTTPS 地址")
		}
	}
	return nil
}

func (p *Providers) refreshTV(ctx context.Context, a *Account) error {
	endpoint := a.Secrets["refresh_endpoint"]
	if endpoint == "" {
		return errors.New("夸克 TV Access Token 不可用。请重新导入，或自行配置可信的 HTTPS 续期服务")
	}
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("TV 续期服务必须为 HTTPS 地址")
	}
	if e := validateRefreshEndpoint(ctx, u); e != nil {
		return e
	}
	if a.Secrets["refresh_token"] == "" {
		return errors.New("请填写夸克 TV Refresh Token")
	}
	body := tvDevice(a)
	rid := md5.Sum([]byte(a.Secrets["device_id"] + strconv.FormatInt(time.Now().UnixMilli(), 10)))
	body["req_id"] = hex.EncodeToString(rid[:])
	body["refresh_token"] = a.Secrets["refresh_token"]
	r, e := p.request(ctx, "POST", endpoint, body, nil)
	if e != nil {
		return e
	}
	d := obj(r["data"])
	if str(d["access_token"]) == "" {
		return errors.New("夸克 TV 续期服务未返回令牌")
	}
	a.Secrets["access_token"] = str(d["access_token"])
	if str(d["refresh_token"]) != "" {
		a.Secrets["refresh_token"] = str(d["refresh_token"])
	}
	exp := num(d["expires_in"])
	if exp <= 0 {
		exp = 3600
	}
	a.Secrets["expires_at"] = strconv.FormatInt(time.Now().Unix()+exp, 10)
	return nil
}
func (p *Providers) refreshAli(ctx context.Context, a *Account) error {
	if a.Secrets["client_id"] == "" || a.Secrets["client_secret"] == "" || a.Secrets["refresh_token"] == "" {
		return errors.New("阿里 OpenAPI 令牌不可用；请导入有效 Access Token，或提供自己的应用凭据与 Refresh Token")
	}
	r, e := p.request(ctx, "POST", "https://openapi.alipan.com/oauth/access_token", object{"client_id": a.Secrets["client_id"], "client_secret": a.Secrets["client_secret"], "grant_type": "refresh_token", "refresh_token": a.Secrets["refresh_token"]}, nil)
	if e != nil {
		return e
	}
	if str(r["access_token"]) == "" {
		return errors.New("阿里授权续期失败，请检查应用及令牌类型")
	}
	a.Secrets["access_token"] = str(r["access_token"])
	if str(r["refresh_token"]) != "" {
		a.Secrets["refresh_token"] = str(r["refresh_token"])
	}
	exp := num(r["expires_in"])
	if exp <= 0 {
		exp = 3600
	}
	a.Secrets["expires_at"] = strconv.FormatInt(time.Now().Unix()+exp, 10)
	return nil
}
func (p *Providers) ali(ctx context.Context, a *Account, path string, body any) (object, error) {
	r, e := p.requestRead(ctx, "POST", "https://openapi.alipan.com"+path, body, headers(map[string]string{"Authorization": "Bearer " + a.Secrets["access_token"]}), nil)
	if e != nil {
		return nil, e
	}
	return r, apiSuccess(r)
}

func (p *Providers) List(ctx context.Context, aid, parent string) (entries []Entry, err error) {
	return p.list(ctx, aid, parent, 0)
}

// ListFirstPage returns only the first page of a directory. Connection
// checks use it to prove reachability and credential validity without
// paging through a huge root folder (115 alone spends ~1s per page in
// mandatory throttling, so a full listing cannot fit the check timeout).
func (p *Providers) ListFirstPage(ctx context.Context, aid, parent string) (entries []Entry, err error) {
	return p.list(ctx, aid, parent, 1)
}

func (p *Providers) list(ctx context.Context, aid, parent string, maxPages int) (entries []Entry, err error) {
	observe := p.beginCloudObservation(ctx, aid, "list")
	defer func() { observe(err) }()
	entries = []Entry{}
	cursor := ""
	seen := map[string]bool{}
	pages := map[string]bool{}
	var revision int64
	expectedTotal := int64(-1)
	offset := 0
	for page := 0; page < 10000; page++ {
		done := false
		err = p.with(ctx, aid, func(a *Account) error {
			if page == 0 {
				revision = a.Revision
			} else if revision != a.Revision {
				return &CloudError{Kind: "configuration", Code: "account_changed", Message: "扫描期间账号配置已改变，请重新扫描"}
			}
			if parent == "" {
				parent = a.Root
			}
			if a.Provider == "115" {
				batch, total, e := p.list115Page(ctx, a, parent, offset)
				if e != nil {
					return e
				}
				if expectedTotal >= 0 && expectedTotal != total {
					return cloudProtocolError("目录总数在扫描期间发生变化，请重新扫描")
				}
				expectedTotal = total
				for _, f := range batch {
					if seen[f.ID] {
						return cloudProtocolError("目录分页包含重复条目，请重新扫描")
					}
					seen[f.ID] = true
					entries = append(entries, f)
				}
				offset += len(batch)
				if int64(len(entries)) > total {
					return cloudProtocolError("目录条目超过返回总数，请重新扫描")
				}
				done = int64(len(entries)) == total
				if !done && len(batch) == 0 {
					return cloudProtocolError("115 目录分页提前结束，请稍后重试")
				}
				return nil
			}
			var r object
			var e error
			var rows []any
			var next string
			var total int64
			var data object
			var listValue, totalValue, cursorValue any
			switch a.Provider {
			case "139":
				r, e = p.mobile(ctx, a, a.Secrets["api_host"]+"/file/list", object{"imageThumbnailStyleList": []string{"Small"}, "orderBy": "updated_at", "orderDirection": "DESC", "pageInfo": object{"pageCursor": cursor, "pageSize": 100}, "parentFileId": parent})
				data = obj(r["data"])
				listValue, cursorValue = data["items"], data["nextPageCursor"]
			case "139old":
				r, e = p.mobile(ctx, a, "https://yun.139.com/orchestration/personalCloud/catalog/v1.0/getDisk", object{"catalogID": parent, "sortDirection": 1, "startNumber": offset + 1, "endNumber": offset + 100, "filterType": 0, "catalogSortType": 0, "contentSortType": 0, "commonAccountInfo": object{"account": a.Secrets["phone"], "accountType": 1}})
				data = obj(obj(r["data"])["getDiskResult"])
				if e == nil {
					dirs, dirOK := data["catalogList"].([]any)
					files, fileOK := data["contentList"].([]any)
					if !dirOK || !fileOK {
						return cloudProtocolError("移动旧个人云目录响应缺少有效列表")
					}
					listValue = append(dirs, files...)
					totalValue = data["nodeCount"]
				}
			case "quark":
				r, e = p.quark(ctx, a, "GET", "/file/sort", nil, map[string]string{"pdir_fid": parent, "_size": "100", "_page": strconv.Itoa(page + 1), "_fetch_total": "1", "fetch_all_file": "1", "fetch_risk_file_name": "1", "_sort": "file_type:asc,file_name:asc"})
				data = obj(r["data"])
				listValue = data["list"]
				totalValue = obj(r["metadata"])["_total"]
				if totalValue == nil {
					totalValue = obj(r["metadata"])["total"]
				}
			case "quarktv":
				r, e = p.tv(ctx, a, "/file", map[string]string{"method": "list", "parent_fid": parent, "page_index": strconv.Itoa(page), "page_size": "100", "order_by": "1", "desc": "0", "list_all": "0"})
				data = obj(r["data"])
				listValue, totalValue = data["files"], data["total_count"]
			case "ali":
				r, e = p.ali(ctx, a, "/adrive/v1.0/openFile/list", object{"drive_id": a.Drive, "parent_file_id": parent, "limit": 100, "marker": cursor, "order_by": "name", "order_direction": "ASC"})
				listValue, cursorValue = r["items"], r["next_marker"]
			}
			if e != nil {
				return e
			}
			var ok bool
			rows, ok = listValue.([]any)
			if !ok {
				return cloudProtocolError("网盘目录响应缺少有效条目列表")
			}
			cursorBased := a.Provider == "139" || a.Provider == "ali"
			if cursorBased {
				// 139 explicitly returns JSON null on the final page (including
				// nonempty pages). Missing fields and non-string values still fail;
				// do not infer completion from an absent cursor or a short list.
				if a.Provider == "139" && cursorValue == nil {
					if _, present := data["nextPageCursor"]; present {
						cursorValue = ""
					}
				}
				next, ok = cursorValue.(string)
				if !ok {
					return cloudProtocolError("网盘目录响应缺少有效分页游标")
				}
			} else {
				total, e = strconv.ParseInt(str(totalValue), 10, 64)
				if e != nil || total < 0 {
					return cloudProtocolError("网盘目录响应缺少有效总数")
				}
				if expectedTotal >= 0 && expectedTotal != total {
					return cloudProtocolError("目录总数在扫描期间发生变化，请重新扫描")
				}
				expectedTotal = total
			}
			for _, v := range rows {
				x := obj(v)
				var f Entry
				switch a.Provider {
				case "139":
					f = Entry{ID: str(x["fileId"]), Name: str(x["name"]), Dir: str(x["type"]) == "folder", Size: num(x["size"]), Modified: str(x["updatedAt"])}
				case "139old":
					f = Entry{ID: str(x["contentID"]), Name: str(x["contentName"]), Size: num(x["contentSize"]), Modified: str(x["updateTime"])}
					if str(x["catalogID"]) != "" {
						f.ID = str(x["catalogID"])
						f.Name = str(x["catalogName"])
						f.Dir = true
					}
				case "quark":
					f = Entry{ID: str(x["fid"]), Name: html.UnescapeString(str(x["file_name"])), Dir: x["dir"] == true || num(x["file_type"]) == 0, Size: num(x["size"]), Modified: str(x["updated_at"])}
				case "quarktv":
					f = Entry{ID: str(x["fid"]), Name: str(x["filename"]), Dir: num(x["isdir"]) == 1, Size: num(x["size"]), Modified: str(x["updated_at"])}
				case "ali":
					f = Entry{ID: str(x["file_id"]), Name: str(x["name"]), Dir: str(x["type"]) == "folder", Size: num(x["size"]), Modified: str(x["updated_at"])}
				}
				if f.ID == "" || f.Name == "" {
					return cloudProtocolError("网盘目录返回了缺少 ID 或名称的条目")
				}
				if seen[f.ID] {
					return cloudProtocolError("目录分页包含重复条目，请重新扫描")
				}
				entries = append(entries, f)
				seen[f.ID] = true
			}
			offset += len(rows)
			if cursorBased {
				if next == "" {
					done = true
					return nil
				}
				if pages[next] || len(rows) == 0 {
					return cloudProtocolError("网盘分页游标重复或分页无进展，已停止扫描")
				}
				pages[next] = true
				cursor = next
			} else {
				if int64(len(entries)) > total {
					return cloudProtocolError("目录条目超过返回总数，请重新扫描")
				}
				if int64(len(entries)) == total {
					done = true
					return nil
				}
				if len(rows) == 0 {
					return cloudProtocolError("目录分页提前结束，请稍后重试")
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if done {
			return entries, nil
		}
		if maxPages > 0 && page+1 >= maxPages {
			return entries, nil
		}
	}
	return nil, cloudProtocolError("目录超过分页上限，请缩小扫描范围")
}
func (p *Providers) Resolve(ctx context.Context, m Media) (target PlaybackTarget, err error) {
	observe := p.beginCloudObservation(ctx, m.Account, "resolve")
	defer func() { observe(err) }()
	ctx = context.WithValue(ctx, playbackPriorityKey{}, true)
	err = p.with(ctx, m.Account, func(a *Account) error {
		var r object
		var e error
		target.Mode = "302"
		switch a.Provider {
		case "115":
			target, e = p.resolve115(ctx, a, m)
		case "139":
			r, e = p.mobile(ctx, a, a.Secrets["api_host"]+"/file/getDownloadUrl", object{"fileId": m.RemoteID})
			d := obj(r["data"])
			target.URL = str(d["url"])
			if d["cdnSwitch"] == true && str(d["cdnUrl"]) != "" {
				target.URL = str(d["cdnUrl"])
			}
		case "139old":
			r, e = p.mobile(ctx, a, "https://yun.139.com/orchestration/personalCloud/uploadAndDownload/v1.0/downloadRequest", object{"appName": "", "contentID": m.RemoteID, "commonAccountInfo": object{"account": a.Secrets["phone"], "accountType": 1}})
			target.URL = str(obj(r["data"])["downloadURL"])
		case "quark":
			r, e = p.quark(ctx, a, "POST", "/file/download", object{"fids": []string{m.RemoteID}}, nil)
			rows := arr(r["data"])
			if len(rows) > 0 {
				target.URL = str(obj(rows[0])["download_url"])
			}
			target.Headers = headers(map[string]string{"Cookie": a.Secrets["cookie"], "Referer": "https://pan.quark.cn/", "User-Agent": quarkUA})
			target.Mode = "proxy-required"
			if a.AllowProxy {
				target.Mode = "proxy"
			}
		case "quarktv":
			r, e = p.tv(ctx, a, "/file", map[string]string{"method": "download", "group_by": "source", "fid": m.RemoteID, "resolution": "low,normal,high,super,2k,4k", "support": "dolby_vision"})
			target.URL = str(obj(r["data"])["download_url"])
		case "ali":
			r, e = p.ali(ctx, a, "/adrive/v1.0/openFile/getDownloadUrl", object{"drive_id": a.Drive, "file_id": m.RemoteID, "expire_sec": 600})
			target.URL = str(r["url"])
		}
		if e != nil {
			return e
		}
		u, e := url.Parse(target.URL)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("网盘未返回有效的原文件下载地址，请检查权限或账号权益")
		}
		return nil
	})
	return
}
