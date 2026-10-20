package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const legacyChannelID = "legacy-webhook"

type NotificationChannel struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Enabled  bool     `json:"enabled"`
	Events   []string `json:"events"`
	Webhook  string   `json:"webhook,omitempty"`
	Secret   string   `json:"secret,omitempty"`
	BotToken string   `json:"bot_token,omitempty"`
	ChatID   string   `json:"chat_id,omitempty"`
	Version  int      `json:"version"`
	Tested   int64    `json:"tested"`
	TestOK   bool     `json:"test_ok"`
}

func (s *Store) initConnectionSchema() error {
	rows, err := s.db.Query("PRAGMA table_info(deliveries)")
	if err != nil {
		return err
	}
	hasChannel := false
	for rows.Next() {
		var cid, nn, pk int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &nn, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		hasChannel = hasChannel || name == "channel"
	}
	rows.Close()
	if !hasChannel {
		tx, er := s.db.Begin()
		if er != nil {
			return er
		}
		defer tx.Rollback()
		_, er = tx.Exec(`CREATE TABLE deliveries_v2(id TEXT PRIMARY KEY,job TEXT,kind TEXT,status TEXT,attempts INTEGER DEFAULT 0,next_at INTEGER,payload TEXT,error TEXT DEFAULT '',channel TEXT NOT NULL DEFAULT '',channel_name TEXT NOT NULL DEFAULT '',channel_version INTEGER NOT NULL DEFAULT 1,created INTEGER NOT NULL DEFAULT 0,UNIQUE(job,kind,channel));
INSERT INTO deliveries_v2(id,job,kind,status,attempts,next_at,payload,error,channel,channel_name) SELECT id,job,kind,status,attempts,next_at,payload,error,CASE WHEN kind='webhook' THEN 'legacy-webhook' ELSE '' END,CASE WHEN kind='webhook' THEN '原通知连接' ELSE '媒体库刷新' END FROM deliveries ORDER BY rowid;
DROP TABLE deliveries;ALTER TABLE deliveries_v2 RENAME TO deliveries;`)
		if er != nil {
			return er
		}
		if er = tx.Commit(); er != nil {
			return er
		}
	}
	_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS event_clients(id TEXT PRIMARY KEY,name TEXT NOT NULL,token_hash TEXT NOT NULL,enabled INTEGER NOT NULL,rules TEXT NOT NULL,created INTEGER NOT NULL,last_used INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS event_calls(id INTEGER PRIMARY KEY AUTOINCREMENT,client TEXT NOT NULL,name TEXT NOT NULL,created INTEGER NOT NULL,action TEXT NOT NULL,status INTEGER NOT NULL);`)
	return err
}

func (s *Store) channels() ([]NotificationChannel, error) {
	raw := s.setting("notification_channels", "")
	if raw != "" {
		sealed, err := base64.StdEncoding.DecodeString(raw)
		n := s.box.NonceSize()
		if err != nil || len(sealed) < n {
			return nil, errors.New("通知通道配置无法读取")
		}
		plain, err := s.box.Open(nil, sealed[:n], sealed[n:], []byte("notification_channels"))
		if err != nil {
			return nil, errors.New("通知通道配置无法解密")
		}
		var items []NotificationChannel
		err = json.Unmarshal(plain, &items)
		return items, err
	}
	c, err := s.integrationConfig()
	if err != nil {
		return nil, err
	}
	items := []NotificationChannel{}
	if c.Webhook != "" {
		typ, name := "webhook", "原 Webhook 通知"
		if u, er := url.Parse(c.Webhook); er == nil && strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/webhooks/yolink") {
			typ, name = "hermes", "Hermes 私聊"
		}
		items = append(items, NotificationChannel{ID: legacyChannelID, Name: name, Type: typ, Enabled: c.WebhookEnabled, Events: []string{"success", "failure"}, Webhook: c.Webhook, Secret: c.WebhookSecret, Version: 1})
	}
	return items, nil
}
func (s *Store) encodeChannels(items []NotificationChannel) (string, error) {
	if items == nil {
		items = []NotificationChannel{}
	}
	plain, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.box.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(s.box.Seal(nonce, nonce, plain, []byte("notification_channels"))), nil
}

func channelView(c NotificationChannel) object {
	host := ""
	if u, err := url.Parse(c.Webhook); err == nil {
		host = u.Host
	}
	return object{"id": c.ID, "name": c.Name, "type": c.Type, "enabled": c.Enabled, "events": c.Events, "version": c.Version, "host": host, "has_webhook": c.Webhook != "", "has_secret": c.Secret != "", "has_bot_token": c.BotToken != "", "has_chat_id": c.ChatID != "", "tested": c.Tested, "test_ok": c.TestOK}
}
func (c NotificationChannel) validate() error {
	if strings.TrimSpace(c.Name) == "" || len([]rune(c.Name)) > 60 || strings.ContainsAny(c.Name, "\r\n\t") {
		return errors.New("请填写 1–60 个字符的连接名称")
	}
	if c.Type != "hermes" && c.Type != "webhook" && c.Type != "telegram" {
		return errors.New("通知类型无效")
	}
	if len(c.Webhook) > 4096 || len(c.Secret) > 4096 || len(c.BotToken) > 256 || len(c.ChatID) > 100 {
		return errors.New("通知参数过长")
	}
	seen := map[string]bool{}
	for _, ev := range c.Events {
		if ev != "success" && ev != "failure" || seen[ev] {
			return errors.New("请选择有效的通知事件")
		}
		seen[ev] = true
	}
	if c.Enabled && len(c.Events) == 0 {
		return errors.New("至少选择一种通知事件")
	}
	if c.Type == "telegram" {
		if (c.Enabled || c.BotToken != "") && !regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`).MatchString(c.BotToken) {
			return errors.New("请填写有效的 Telegram Bot Token")
		}
		if (c.Enabled || c.ChatID != "") && !regexp.MustCompile(`^(-?[0-9]+|@[A-Za-z0-9_]{5,})$`).MatchString(c.ChatID) {
			return errors.New("Chat ID 应为会话数字 ID 或 @频道用户名")
		}
	} else if c.Enabled || c.Webhook != "" {
		if err := validServiceURL(c.Webhook); err != nil {
			return err
		}
	}
	return nil
}

// The configurable transport is only injected by isolated tests. Production uses
// Telegram's fixed HTTPS endpoint and never touches update consumers or menus.
var telegramHTTPClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func sendChannel(ctx context.Context, c NotificationChannel, payload any, id string) error {
	if c.Type != "telegram" {
		return webhookRequest(ctx, IntegrationConfig{Webhook: c.Webhook, WebhookSecret: c.Secret}, payload, id)
	}
	var v struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	raw, _ := json.Marshal(payload)
	_ = json.Unmarshal(raw, &v)
	text := strings.TrimSpace(v.Title + "\n" + v.Text)
	runes := []rune(text)
	if len(runes) > 3800 {
		text = string(runes[:3800]) + "…"
	}
	body, _ := json.Marshal(object{"chat_id": c.ChatID, "text": text, "disable_web_page_preview": true})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+c.BotToken+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return errors.New("Telegram 请求无法创建")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := telegramHTTPClient.Do(req)
	if err != nil {
		return errors.New("Telegram 连接失败，请检查 NAS 网络与代理配置")
	}
	defer res.Body.Close()
	var out struct {
		OK        bool `json:"ok"`
		ErrorCode int  `json:"error_code"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(&out)
	if err != nil || res.StatusCode != 200 || !out.OK {
		return fmt.Errorf("Telegram 发送失败（HTTP %d，错误码 %d），请检查 Token、Chat ID 及机器人会话权限", res.StatusCode, out.ErrorCode)
	}
	return nil
}

func (s *Server) notificationChannels(w http.ResponseWriter, r *http.Request) {
	items, err := s.s.channels()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	views := []object{}
	for _, c := range items {
		views = append(views, channelView(c))
	}
	jsonOut(w, 200, object{"items": views, "revision": contentHash([]byte(scanJSON(items)))})
}

type channelSaveRequest struct {
	NotificationChannel
	Revision      string `json:"revision"`
	ClearSecret   bool   `json:"clear_secret"`
	ClearBotToken bool   `json:"clear_bot_token"`
	ClearChatID   bool   `json:"clear_chat_id"`
}

func (s *Server) saveNotificationChannel(w http.ResponseWriter, r *http.Request) {
	var in channelSaveRequest
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.s.channelsMu.Lock()
	defer s.s.channelsMu.Unlock()
	items, err := s.s.channels()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if in.Revision != contentHash([]byte(scanJSON(items))) {
		fail(w, 409, "通知连接已更新，请刷新列表后重新编辑；当前输入保留")
		return
	}
	index := -1
	old := NotificationChannel{}
	for i, c := range items {
		if c.ID == in.ID {
			index, old = i, c
			break
		}
	}
	next := in.NotificationChannel
	next.Name = strings.TrimSpace(next.Name)
	next.Tested, next.TestOK = 0, false
	if in.ID != "" && index < 0 {
		fail(w, 404, "通知连接已删除")
		return
	}
	if index < 0 {
		if len(items) >= 16 {
			fail(w, 400, "最多配置 16 个通知通道")
			return
		}
		next.ID = randomID()
		next.Version = 1
	} else {
		if next.Type != old.Type {
			fail(w, 400, "现有连接类型不可更换，请新增另一种连接")
			return
		}
		if next.Webhook == "" {
			next.Webhook = old.Webhook
		}
		if next.Secret == "" && !in.ClearSecret {
			next.Secret = old.Secret
		}
		if next.BotToken == "" && !in.ClearBotToken {
			next.BotToken = old.BotToken
		}
		if next.ChatID == "" && !in.ClearChatID {
			next.ChatID = old.ChatID
		}
		next.Version = old.Version
		next.Tested, next.TestOK = old.Tested, old.TestOK
	}
	if next.Type == "telegram" {
		next.Webhook, next.Secret = "", ""
	} else {
		next.BotToken, next.ChatID = "", ""
	}
	if err = next.validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	for _, c := range items {
		if c.ID != next.ID && strings.EqualFold(c.Name, next.Name) {
			fail(w, 400, "连接名称已存在，请使用不同名称")
			return
		}
	}
	changed := index >= 0 && (next.Webhook != old.Webhook || next.Secret != old.Secret || next.BotToken != old.BotToken || next.ChatID != old.ChatID)
	if changed {
		next.Version++
		next.Tested, next.TestOK = 0, false
	}
	if index < 0 {
		items = append(items, next)
	} else {
		items[index] = next
	}
	tx, err := s.s.db.Begin()
	if err != nil {
		fail(w, 500, "通知连接保存失败")
		return
	}
	defer tx.Rollback()
	if changed {
		_, err = tx.Exec("UPDATE deliveries SET status='cancelled',next_at=0,error='接收配置已更换，旧投递已取消' WHERE channel=? AND status IN ('pending','paused','failed')", next.ID)
	}
	if err == nil && !next.Enabled {
		_, err = tx.Exec("UPDATE deliveries SET status='paused',next_at=0 WHERE channel=? AND status='pending'", next.ID)
	}
	if err == nil && next.Enabled {
		_, err = tx.Exec("UPDATE deliveries SET status='pending',next_at=?,error='' WHERE channel=? AND channel_version=? AND status='paused'", time.Now().Unix(), next.ID, next.Version)
	}
	if err == nil {
		err = s.s.persistChannels(tx, items)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 500, "通知连接保存失败")
		return
	}
	jsonOut(w, 200, object{"item": channelView(next), "ok": true})
}
func (s *Store) persistChannels(tx *sql.Tx, items []NotificationChannel) error {
	raw, err := s.encodeChannels(items)
	if err != nil {
		return err
	}
	// On first management action, move legacy notification credentials into the
	// channel collection atomically, keeping the independent media settings.
	var value string
	err = tx.QueryRow("SELECT value FROM settings WHERE id='integration_config'").Scan(&value)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if value != "" {
		sealed, er := base64.StdEncoding.DecodeString(value)
		n := s.box.NonceSize()
		if er != nil || len(sealed) < n {
			return errors.New("原通知配置无法读取")
		}
		plain, er := s.box.Open(nil, sealed[:n], sealed[n:], []byte("integrations"))
		if er != nil {
			return er
		}
		var c IntegrationConfig
		if er = json.Unmarshal(plain, &c); er != nil {
			return er
		}
		c.WebhookEnabled, c.Webhook, c.WebhookSecret = false, "", ""
		b, _ := json.Marshal(c)
		nonce := make([]byte, n)
		if _, er = rand.Read(nonce); er != nil {
			return er
		}
		value = base64.StdEncoding.EncodeToString(s.box.Seal(nonce, nonce, b, []byte("integrations")))
		if _, er = tx.Exec("UPDATE settings SET value=? WHERE id='integration_config'", value); er != nil {
			return er
		}
	}
	_, err = tx.Exec("INSERT INTO settings(id,value) VALUES('notification_channels',?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", raw)
	return err
}
func (s *Server) removeNotificationChannel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision string `json:"revision"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.s.channelsMu.Lock()
	defer s.s.channelsMu.Unlock()
	items, err := s.s.channels()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if in.Revision != contentHash([]byte(scanJSON(items))) {
		fail(w, 409, "连接列表已更新，请刷新后重试")
		return
	}
	id := r.PathValue("id")
	next := []NotificationChannel{}
	found := false
	for _, c := range items {
		if c.ID == id {
			found = true
		} else {
			next = append(next, c)
		}
	}
	if !found {
		fail(w, 404, "通知连接已删除")
		return
	}
	tx, err := s.s.db.Begin()
	if err != nil {
		fail(w, 500, "删除失败")
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec("UPDATE deliveries SET status='cancelled',next_at=0,error='通知连接已删除，未发送投递已取消' WHERE channel=? AND status IN ('pending','paused','failed')", id)
	if err == nil {
		err = s.s.persistChannels(tx, next)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 500, "删除通知连接失败")
		return
	}
	jsonOut(w, 200, object{"ok": true})
}
func (s *Server) testNotificationChannel(w http.ResponseWriter, r *http.Request) {
	s.s.channelsMu.Lock()
	defer s.s.channelsMu.Unlock()
	items, err := s.s.channels()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	index := -1
	for i, c := range items {
		if c.ID == r.PathValue("id") {
			index = i
			break
		}
	}
	if index < 0 {
		fail(w, 404, "通知连接不存在")
		return
	}
	c := items[index]
	if !c.Enabled {
		fail(w, 400, "请先启用并保存此通知连接")
		return
	}
	if err = c.validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err = sendChannel(ctx, c, object{"title": "YoLink 测试通知", "text": "通知连接「" + c.Name + "」测试成功", "event": "test"}, randomID())
	items[index].Tested = time.Now().Unix()
	items[index].TestOK = err == nil
	tx, saveErr := s.s.db.Begin()
	if saveErr == nil {
		saveErr = s.s.persistChannels(tx, items)
		if saveErr == nil {
			saveErr = tx.Commit()
		} else {
			tx.Rollback()
		}
	}
	if saveErr != nil {
		fail(w, 500, "测试已执行，但结果保存失败，请刷新状态")
		return
	}
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	jsonOut(w, 200, object{"ok": true, "note": "接收服务已接受测试通知，请在目标会话核对。"})
}

func (e *Engine) queueNotifications(job string, payload any) {
	e.s.channelsMu.Lock()
	defer e.s.channelsMu.Unlock()
	items, err := e.s.channels()
	if err != nil {
		return
	}
	var v struct {
		Status string `json:"status"`
		Event  string `json:"event"`
	}
	raw := scanJSON(payload)
	_ = json.Unmarshal([]byte(raw), &v)
	event := "success"
	if v.Event == "ingest_failed" || v.Status == "failed" || v.Status == "partial" || v.Status == "cancelled" || v.Status == "interrupted" {
		event = "failure"
	}
	for _, c := range items {
		if !c.Enabled {
			continue
		}
		for _, allowed := range c.Events {
			if allowed == event {
				_, _ = e.s.db.Exec("INSERT OR IGNORE INTO deliveries(id,job,kind,status,next_at,payload,channel,channel_name,channel_version,created) VALUES(?,?,'webhook','pending',?,?,?,?,?,?)", randomID(), job, time.Now().Add(3*time.Second).Unix(), raw, c.ID, c.Name, c.Version, time.Now().Unix())
				break
			}
		}
	}
}
func (e *Engine) processNotification(id string) {
	e.s.channelsMu.Lock()
	defer e.s.channelsMu.Unlock()
	var channel, status, raw string
	var version, attempts int
	if e.s.db.QueryRow("SELECT channel,status,payload,channel_version,attempts FROM deliveries WHERE id=?", id).Scan(&channel, &status, &raw, &version, &attempts) != nil || status != "pending" {
		return
	}
	items, err := e.s.channels()
	if err != nil {
		return
	}
	var current *NotificationChannel
	for i := range items {
		if items[i].ID == channel {
			current = &items[i]
			break
		}
	}
	if current == nil || current.Version != version {
		e.s.db.Exec("UPDATE deliveries SET status='cancelled',next_at=0,error='通知连接已删除或更换' WHERE id=?", id)
		return
	}
	if !current.Enabled {
		e.s.db.Exec("UPDATE deliveries SET status='paused',next_at=0 WHERE id=?", id)
		return
	}
	var payload any
	_ = json.Unmarshal([]byte(raw), &payload)
	err = sendChannel(e.ctx, *current, payload, id)
	e.finishDelivery(id, attempts, err)
}
