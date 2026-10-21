package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type IntegrationConfig struct {
	Enabled            bool   `json:"enabled"`
	ServerType         string `json:"server_type"`
	Base               string `json:"base"`
	APIKey             string `json:"api_key,omitempty"`
	LocalPrefix        string `json:"local_prefix"`
	RemotePrefix       string `json:"remote_prefix"`
	RefreshAll         bool   `json:"refresh_all"`
	WebhookEnabled     bool   `json:"webhook_enabled"`
	Webhook            string `json:"webhook,omitempty"`
	WebhookSecret      string `json:"webhook_secret,omitempty"`
	HasWebhookSecret   bool   `json:"has_webhook_secret"`
	ClearWebhookSecret bool   `json:"clear_webhook_secret,omitempty"`
	HasAPIKey          bool   `json:"has_api_key"`
	HasWebhook         bool   `json:"has_webhook"`
	ClearAPIKey        bool   `json:"clear_api_key,omitempty"`
	ClearWebhook       bool   `json:"clear_webhook,omitempty"`
}

func (s *Store) integrationConfig() (IntegrationConfig, error) {
	var c IntegrationConfig
	v := s.setting("integration_config", "")
	if v == "" {
		return c, nil
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	n := s.box.NonceSize()
	if err != nil || len(raw) < n {
		return c, errors.New("联动配置无法读取")
	}
	raw, err = s.box.Open(nil, raw[:n], raw[n:], []byte("integrations"))
	if err != nil {
		return c, errors.New("联动配置无法解密")
	}
	err = json.Unmarshal(raw, &c)
	return c, err
}
func validServiceURL(v string) error {
	u, err := url.Parse(v)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("请填写完整的 HTTP 或 HTTPS 地址")
	}
	return nil
}
func (c IntegrationConfig) validate() error {
	if c.Enabled {
		if c.ServerType != "emby" && c.ServerType != "jellyfin" {
			return errors.New("请选择 Emby 或 Jellyfin")
		}
		if err := validServiceURL(c.Base); err != nil {
			return err
		}
		u, _ := url.Parse(c.Base)
		if u.RawQuery != "" {
			return errors.New("媒体库地址不能包含查询参数")
		}
		if c.APIKey == "" {
			return errors.New("请填写媒体库 API Key")
		}
		if !c.RefreshAll && (c.LocalPrefix == "" || c.RemotePrefix == "") {
			return errors.New("请填写输出路径映射，或选择刷新整个媒体库")
		}
	}
	if c.WebhookEnabled {
		if err := validServiceURL(c.Webhook); err != nil {
			return errors.New("请填写有效的通知 Webhook 地址")
		}
	}
	if len(c.APIKey) > 4096 || len(c.Webhook) > 4096 || len(c.WebhookSecret) > 4096 || len(c.Base) > 2048 {
		return errors.New("联动配置过长")
	}
	return nil
}
func (s *Store) saveIntegration(c IntegrationConfig) error {
	c.HasAPIKey = false
	c.HasWebhook = false
	c.HasWebhookSecret = false
	c.ClearWebhookSecret = false
	c.ClearAPIKey = false
	c.ClearWebhook = false
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	nonce := make([]byte, s.box.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	sealed := s.box.Seal(nonce, nonce, raw, []byte("integrations"))
	return s.setSetting("integration_config", base64.StdEncoding.EncodeToString(sealed))
}
func (s *Server) integrations(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		s.s.channelsMu.Lock()
		defer s.s.channelsMu.Unlock()
	}
	c, err := s.s.integrationConfig()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if r.Method == "POST" {
		var next IntegrationConfig
		if err = decode(w, r, &next); err != nil {
			fail(w, 400, err.Error())
			return
		}
		if s.s.setting("notification_channels", "") != "" && (r.PathValue("section") == "webhook" || (r.PathValue("section") == "" && (next.WebhookEnabled || next.Webhook != "" || next.WebhookSecret != ""))) {
			fail(w, 409, "请在通知通道中管理接收连接")
			return
		}
		if next.APIKey == "" && !next.ClearAPIKey {
			next.APIKey = c.APIKey
		}
		if next.Webhook == "" && !next.ClearWebhook {
			next.Webhook = c.Webhook
		}
		if next.WebhookSecret == "" && !next.ClearWebhookSecret {
			next.WebhookSecret = c.WebhookSecret
		}
		switch r.PathValue("section") {
		case "library":
			next.WebhookEnabled, next.Webhook, next.WebhookSecret = c.WebhookEnabled, c.Webhook, c.WebhookSecret
		case "webhook":
			c.WebhookEnabled, c.Webhook, c.WebhookSecret = next.WebhookEnabled, next.Webhook, next.WebhookSecret
			next = c
		case "":
		default:
			fail(w, 404, "联动设置分类不存在")
			return
		}
		if err = next.validate(); err != nil {
			fail(w, 400, err.Error())
			return
		}
		if err = s.s.saveIntegration(next); err != nil {
			fail(w, 500, "联动配置保存失败")
			return
		}
		c = next
	}
	c.HasAPIKey = c.APIKey != ""
	c.HasWebhook = c.Webhook != ""
	c.HasWebhookSecret = c.WebhookSecret != ""
	c.APIKey = ""
	c.Webhook = ""
	c.WebhookSecret = ""
	deliveries := []object{}
	rows, err := s.s.db.Query(`SELECT d.id,d.job,d.kind,d.status,d.attempts,d.next_at,d.error,COALESCE(j.created,d.created,0),COALESCE(json_extract(j.payload,'$.source_path'),json_extract(d.payload,'$.path'),json_extract(d.payload,'$.source_path'),''),COALESCE(json_extract(d.payload,'$.title'),''),d.channel,d.channel_name FROM deliveries d LEFT JOIN jobs j ON j.id=d.job ORDER BY d.rowid DESC LIMIT 30`)
	if err == nil {
		for rows.Next() {
			var id, job, kind, status, message, source, title, channel, channelName string
			var attempts int
			var next, created int64
			if rows.Scan(&id, &job, &kind, &status, &attempts, &next, &message, &created, &source, &title, &channel, &channelName) == nil {
				deliveries = append(deliveries, object{"id": id, "job": job, "kind": kind, "status": status, "attempts": attempts, "next_at": next, "error": cleanRecordText(message, 300), "source_path": source, "title": title, "created": created, "channel": channel, "channel_name": channelName})
			}
		}
		rows.Close()
	}
	jsonOut(w, 200, object{"config": c, "deliveries": deliveries})
}
// checkServiceDestination vets where an integration request may carry
// credentials (the media library API key or a signed webhook). Link-local
// destinations are always refused: that range includes the cloud metadata
// address 169.254.169.254, the one destination that can leak credentials
// the user never configured, and no legitimate receiver lives there.
// Loopback and other private addresses stay allowed on purpose: pointing
// a webhook or library at a same-host or LAN self-hosted service (Hermes
// on this machine, a NAS Emby) is an intended deployment shape — the
// repository's own integration tests codify loopback webhook receivers —
// so that residual surface is an accepted trade-off of holding the
// configuration permission, not something this check tries to remove.
func checkServiceDestination(ctx context.Context, target string) error {
	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" {
		return errors.New("联动请求地址无效")
	}
	host := u.Hostname()
	var addrs []netip.Addr
	if addr, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{addr}
	} else {
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(ips) == 0 {
			return errors.New("连接失败，请检查地址、网络及服务状态")
		}
		for _, v := range ips {
			if addr, ok := netip.AddrFromSlice(v.IP); ok {
				addrs = append(addrs, addr)
			}
		}
		if len(addrs) == 0 {
			return errors.New("连接失败，请检查地址、网络及服务状态")
		}
	}
	for _, addr := range addrs {
		addr = addr.Unmap()
		if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() {
			return errors.New("联动地址指向链路本地地址（含云元数据服务），已拒绝发送凭据")
		}
	}
	return nil
}

func integrationRequest(ctx context.Context, method, target, key string, body any, headers ...http.Header) error {
	if err := checkServiceDestination(ctx, target); err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewBufferString(scanJSON(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return errors.New("联动请求地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	for _, values := range headers {
		for name, value := range values {
			req.Header[name] = value
		}
	}
	if key != "" {
		req.Header.Set("X-Emby-Token", key)
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("连接失败，请检查地址、网络及服务状态")
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("服务返回 HTTP %d，请检查地址与凭据", res.StatusCode)
	}
	return nil
}

// Sign the exact JSON bytes sent on the wire. Delivery IDs survive retries;
// timestamps are renewed so receivers can reject captured, stale requests.
func webhookRequest(ctx context.Context, c IntegrationConfig, body any, deliveryID string) error {
	headers := http.Header{}
	headers.Set("X-Request-ID", deliveryID)
	if c.WebhookSecret != "" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(c.WebhookSecret))
		_, _ = mac.Write([]byte(timestamp + "." + scanJSON(body)))
		headers.Set("X-Webhook-Timestamp", timestamp)
		headers.Set("X-Webhook-Signature-V2", fmt.Sprintf("%x", mac.Sum(nil)))
	}
	return integrationRequest(ctx, "POST", c.Webhook, "", body, headers)
}
func (s *Server) testIntegration(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind string `json:"kind"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	c, err := s.s.integrationConfig()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	switch in.Kind {
	case "library":
		if !c.Enabled {
			fail(w, 400, "请先保存并启用媒体库联动")
			return
		}
		err = integrationRequest(ctx, "GET", strings.TrimRight(c.Base, "/")+"/System/Info", c.APIKey, nil)
	case "webhook":
		if !c.WebhookEnabled {
			fail(w, 400, "请先保存并启用通知 Webhook")
			return
		}
		err = webhookRequest(ctx, c, object{"title": "YoLink 测试通知", "text": "通知连接测试成功", "event": "test"}, randomID())
	default:
		fail(w, 400, "测试类型无效")
		return
	}
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	jsonOut(w, 200, object{"ok": true})
}
func (e *Engine) queueDelivery(job, kind string, payload any) {
	channel, name := "", "媒体库刷新"
	if kind == "webhook" {
		channel, name = legacyChannelID, "原通知连接"
	}
	_, _ = e.s.db.Exec("INSERT OR IGNORE INTO deliveries(id,job,kind,status,next_at,payload,channel,channel_name,created) VALUES(?,?,?,'pending',?,?,?,?,?)", randomID(), job, kind, time.Now().Add(3*time.Second).Unix(), scanJSON(payload), channel, name, time.Now().Unix())
}
func (e *Engine) afterGeneration(j Job, r Rule) {
	if j.DryRun || (j.Status == "cancelled" && j.Trigger != "ingest") {
		return
	}
	title := "生成完成"
	if j.Status == "cancelled" {
		title = "生成任务已取消"
	}
	level := "info"
	if j.Status == "failed" || j.Status == "partial" {
		title = "生成需要处理"
		level = "error"
	}
	body := fmt.Sprintf("%s：新增 %d，跳过 %d，失败 %d。%s", r.Name, j.CreatedFiles, j.Skipped, j.Errors, j.Message)
	if j.SourcePath != "" {
		body = j.SourcePath + "\n" + body
	}
	if j.CreatedFiles > 0 || j.Errors > 0 || j.Status == "failed" {
		dedupe := "job:" + j.ID
		if level == "error" {
			dedupe = "issue:" + r.ID + ":" + j.ErrorKind
		}
		e.s.notify(title, body, level, dedupe)
	}
	if r.Notify && (j.CreatedFiles > 0 || j.Errors > 0 || j.Status == "failed" || j.Trigger == "ingest") {
		e.queueNotifications(j.ID, object{"title": title, "text": cleanRecordText(body, 500), "event": "generation", "job": j.ID, "status": j.Status, "source_path": j.SourcePath, "created": j.CreatedFiles, "skipped": j.Skipped, "errors": j.Errors})
	}
	if r.RefreshLibrary && j.CreatedFiles > 0 {
		e.queueDelivery(j.ID, "library", object{"output": absoluteOutputPath(e, r.Output)})
	}
}
func (e *Engine) deliveryWorker() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.processDeliveries()
		}
	}
}
func (e *Engine) processDeliveries() {
	if e.s.restoreRestricted {
		return
	}
	c, err := e.s.integrationConfig()
	if err != nil {
		return
	}
	rows, err := e.s.db.Query("SELECT id,job,kind,attempts,payload FROM deliveries WHERE status='pending' AND next_at<=? ORDER BY rowid LIMIT 20", time.Now().Unix())
	if err != nil {
		return
	}
	type delivery struct {
		id, job, kind, raw string
		attempts           int
	}
	items := []delivery{}
	for rows.Next() {
		var d delivery
		if rows.Scan(&d.id, &d.job, &d.kind, &d.attempts, &d.raw) == nil {
			items = append(items, d)
		}
	}
	rows.Close()
	library := []delivery{}
	paths := map[string]bool{}
	for _, d := range items {
		if d.kind == "library" {
			if !c.Enabled {
				e.finishDelivery(d.id, d.attempts, errors.New("媒体库联动尚未启用"))
				continue
			}
			var in struct {
				Output string `json:"output"`
			}
			_ = json.Unmarshal([]byte(d.raw), &in)
			if !c.RefreshAll {
				local := strings.TrimRight(strings.ReplaceAll(c.LocalPrefix, "\\", "/"), "/")
				out := strings.ReplaceAll(in.Output, "\\", "/")
				if out != local && !strings.HasPrefix(out, local+"/") {
					e.finishDelivery(d.id, 3, errors.New("输出目录不在媒体库路径映射范围，请检查本地路径"))
					continue
				}
				paths[strings.TrimRight(c.RemotePrefix, "/")+strings.TrimPrefix(out, local)] = true
			}
			library = append(library, d)
			continue
		}
		e.processNotification(d.id)
	}
	if len(library) > 0 {
		target := strings.TrimRight(c.Base, "/") + "/Library/Media/Updated"
		updates := []object{}
		for p := range paths {
			updates = append(updates, object{"Path": p, "UpdateType": "Created"})
		}
		var payload any = object{"Updates": updates}
		if c.RefreshAll {
			target = strings.TrimRight(c.Base, "/") + "/Library/Refresh"
			payload = nil
		}
		err = integrationRequest(e.ctx, "POST", target, c.APIKey, payload)
		for _, d := range library {
			e.finishDelivery(d.id, d.attempts, err)
		}
	}
}
func (e *Engine) finishDelivery(id string, attempts int, err error) {
	attempts++
	status, message := "sent", ""
	next := int64(0)
	if err != nil {
		message = cleanRecordText(err.Error(), 300)
		status = "pending"
		next = time.Now().Add(time.Duration(1<<min(attempts, 6)) * 15 * time.Second).Unix()
		if attempts >= 3 {
			status = "failed"
			e.s.notify("联动发送失败", message+"，可在运行记录的通知投递中重试。", "error", "delivery:"+id+":"+strconv.Itoa(attempts))
		}
	}
	_, _ = e.s.db.Exec("UPDATE deliveries SET status=?,attempts=?,next_at=?,error=? WHERE id=?", status, attempts, next, message, id)
}
func (s *Server) retryDelivery(w http.ResponseWriter, r *http.Request) {
	s.s.channelsMu.Lock()
	defer s.s.channelsMu.Unlock()
	var kind, channel string
	var version int
	if s.s.db.QueryRow("SELECT kind,channel,channel_version FROM deliveries WHERE id=?", r.PathValue("id")).Scan(&kind, &channel, &version) != nil {
		fail(w, 404, "投递记录不存在")
		return
	}
	if strings.HasPrefix(kind, "webhook") {
		items, err := s.s.channels()
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		valid := false
		for _, c := range items {
			if c.ID == channel && c.Version == version && c.Enabled {
				valid = true
				break
			}
		}
		if !valid {
			fail(w, 409, "原通知连接已停用、删除或更换，无法重试此记录")
			return
		}
	}
	result, err := s.s.db.Exec("UPDATE deliveries SET status='pending',attempts=0,next_at=?,error='' WHERE id=? AND status='failed'", time.Now().Unix(), r.PathValue("id"))
	if err != nil {
		fail(w, 500, "提交重试失败")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		fail(w, 400, "仅失败的联动可重试")
		return
	}
	jsonOut(w, 202, object{"ok": true})
}
