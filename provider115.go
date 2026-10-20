package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const pan115UA = "Mozilla/5.0 115Browser/27.0.6.7"

type pan115UserAgentKey struct{}

func download115UA(ctx context.Context) string {
	ua, _ := ctx.Value(pan115UserAgentKey{}).(string)
	if ua == "" || len(ua) > 2048 || strings.ContainsAny(ua, "\r\n\x00") {
		return pan115UA
	}
	return ua
}

func numeric115ID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func validate115Cookie(raw string) (string, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "Cookie:"))
	if raw == "" || len(raw) > 32768 || strings.ContainsAny(raw, "\r\n\x00") {
		return "", errors.New("请导入有效的 115 Cookie，包含 UID、CID、SEID")
	}
	r := &http.Request{Header: http.Header{"Cookie": {raw}}}
	for _, name := range []string{"UID", "CID", "SEID"} {
		c, err := r.Cookie(name)
		if err != nil || c.Value == "" {
			return "", errors.New("115 Cookie 缺少 UID、CID 或 SEID，请复制完整值")
		}
	}
	return raw, nil
}
func check115(r object) error {
	if r["state"] != true && str(r["state"]) != "1" {
		code := r["errno"]
		if code == nil {
			code = r["errNo"]
		}
		return fmt.Errorf("115 请求失败（%s），请检查 Cookie 是否过期、账号权益或风控状态", safeCode(code))
	}
	return nil
}
func (p *Providers) request115(ctx context.Context, a *Account, method, endpoint string, body any, receive func([]*http.Cookie)) (object, error) {
	// Together with requestCookies' 180 ms delay, cap a serial account at 1 req/s.
	timer := time.NewTimer(820 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	ua := pan115UA
	if method == "POST" {
		ua = download115UA(ctx)
	}
	r, err := p.requestCookies(ctx, method, endpoint, body, headers(map[string]string{"Cookie": a.Secrets["cookie"], "Referer": "https://115.com/", "User-Agent": ua}), receive)
	if err != nil {
		return nil, err
	}
	return r, check115(r)
}
func (p *Providers) list115(ctx context.Context, a *Account, parent string) ([]Entry, error) {
	if !numeric115ID(parent) {
		return nil, errors.New("115 目录 ID 应为数字，根目录为 0")
	}
	entries := []Entry{}
	seen := map[string]bool{}
	offset := 0
	for page := 0; page < 10000; page++ {
		r, err := p.request115(ctx, a, "GET", queryURL("https://webapi.115.com/files", map[string]string{"cid": parent, "aid": "1", "o": "file_name", "asc": "1", "offset": strconv.Itoa(offset), "limit": "100", "show_dir": "1", "format": "json", "fc_mix": "0"}), nil, nil)
		if err != nil {
			return nil, err
		}
		rows, ok := r["data"].([]any)
		if !ok || r["count"] == nil || num(r["count"]) < 0 {
			return nil, errors.New("115 目录响应缺少条目或总数")
		}
		if cid := str(r["cid"]); cid != "" && cid != parent {
			return nil, errors.New("115 返回的目录与请求不符，已停止扫描")
		}
		if r["offset"] != nil && num(r["offset"]) != int64(offset) {
			return nil, errors.New("115 分页位置异常，已停止扫描")
		}
		previous := len(entries)
		for _, value := range rows {
			x := obj(value)
			f := Entry{ID: str(x["fid"]), PickCode: str(x["pc"]), Name: html.UnescapeString(str(x["n"])), Size: num(x["s"]), Modified: str(x["t"])}
			if f.ID == "" || f.ID == "0" {
				f.ID, f.Dir = str(x["cid"]), true
			}
			if !numeric115ID(f.ID) || f.Name == "" || f.Size < 0 {
				return nil, errors.New("115 返回了无效的文件条目")
			}
			if !seen[f.ID] {
				entries = append(entries, f)
				seen[f.ID] = true
			}
		}
		offset += len(rows)
		if int64(len(entries)) >= num(r["count"]) {
			return entries, nil
		}
		if len(entries) == previous {
			return nil, errors.New("115 目录分页提前结束或重复，请稍后重试")
		}
	}
	return nil, errors.New("115 目录超过分页上限，请缩小扫描范围")
}
func (p *Providers) resolve115(ctx context.Context, a *Account, m Media) (PlaybackTarget, error) {
	if !numeric115ID(m.RemoteID) {
		return PlaybackTarget{}, errors.New("115 文件 ID 无效，请重新生成索引")
	}
	pickcode := m.PickCode
	if pickcode == "" {
		r, err := p.request115(ctx, a, "GET", queryURL("https://webapi.115.com/files/get_info", map[string]string{"file_id": m.RemoteID}), nil, nil)
		if err != nil {
			return PlaybackTarget{}, err
		}
		for _, value := range arr(r["data"]) {
			x := obj(value)
			if str(x["fid"]) == m.RemoteID {
				pickcode = str(x["pc"])
			}
		}
	}
	if pickcode == "" {
		return PlaybackTarget{}, errors.New("115 未返回此文件的 pickcode，请检查文件是否存在")
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return PlaybackTarget{}, errors.New("115 下载请求初始化失败")
	}
	body, _ := json.Marshal(map[string]string{"pickcode": pickcode})
	encoded, err := encode115(body, key)
	if err != nil {
		return PlaybackTarget{}, err
	}
	cookies := []*http.Cookie{}
	r, err := p.request115(ctx, a, "POST", queryURL("https://proapi.115.com/app/chrome/downurl", map[string]string{"t": strconv.FormatInt(time.Now().Unix(), 10)}), url.Values{"data": {encoded}}, func(values []*http.Cookie) { cookies = values })
	if err != nil {
		return PlaybackTarget{}, err
	}
	decoded, err := decode115(str(r["data"]), key)
	if err != nil {
		return PlaybackTarget{}, err
	}
	var data object
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.UseNumber()
	if decoder.Decode(&data) != nil {
		return PlaybackTarget{}, errors.New("115 下载响应格式异常")
	}
	file := obj(data[m.RemoteID])
	target := PlaybackTarget{URL: str(obj(file["url"])["url"]), Mode: "302", Headers: headers(map[string]string{"User-Agent": download115UA(ctx)})}
	if a.AllowProxy {
		target.Mode = "proxy"
		target.Headers.Set("Referer", "https://115.com/")
		target.Headers.Set("Cookie", a.Secrets["cookie"])
		// Download-only cookies belong to this target, never to the saved account.
		request := &http.Request{Header: target.Headers}
		for _, cookie := range cookies {
			request.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
		}
	}
	return target, nil
}
func (p *Providers) quota115(ctx context.Context, a *Account) (QuotaUsage, error) {
	r, err := p.request115(ctx, a, "GET", "https://webapi.115.com/files/index_info", nil, nil)
	if err != nil {
		return QuotaUsage{}, err
	}
	space := obj(obj(r["data"])["space_info"])
	used := obj(space["all_use"])["size"]
	usage := QuotaUsage{Total: num(obj(space["all_total"])["size"]), Used: num(used)}
	if used == nil || usage.Total <= 0 || usage.Used < 0 || usage.Used > usage.Total {
		return QuotaUsage{}, errors.New("115 当前授权未返回有效容量信息")
	}
	return usage, nil
}
