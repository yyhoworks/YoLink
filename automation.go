package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
)

type CommandTarget struct {
	Alias   string `json:"alias"`
	Rule    string `json:"rule"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

type commandTargetView struct {
	CommandTarget
	Valid    bool   `json:"valid"`
	Reason   string `json:"reason"`
	RuleName string `json:"rule_name"`
	Output   string `json:"output"`
	Notify   bool   `json:"notify"`
}

func (s *Store) commandTargets() ([]CommandTarget, string, error) {
	raw := s.setting("command_targets", "[]")
	items := []CommandTarget{}
	err := json.Unmarshal([]byte(raw), &items)
	return items, contentHash([]byte(raw)), err
}

func (s *Server) commandTargetInfo(t CommandTarget) commandTargetView {
	v := commandTargetView{CommandTarget: t}
	r, err := s.s.rule(t.Rule)
	if err != nil {
		v.Reason = "目录规则已移除，请重新选择规则"
		return v
	}
	v.RuleName = r.Name
	v.Notify = r.Notify
	a, err := s.s.account(r.Account)
	if err != nil || a.Disabled {
		v.Reason = "所属网盘不可用或已停用"
		return v
	}
	clean, err := validRemotePath(t.Path)
	if err != nil || clean != t.Path || (t.Path != r.SourcePath && !strings.HasPrefix(t.Path, strings.TrimRight(r.SourcePath, "/")+"/")) {
		v.Reason = "云端目录必须在所选规则内"
		return v
	}
	v.Output = path.Join(absoluteOutputPath(s.engine, r.Output), strings.TrimPrefix(strings.TrimPrefix(t.Path, strings.TrimRight(r.SourcePath, "/")), "/"))
	v.Valid = true
	return v
}

func (s *Server) commandCatalog(w http.ResponseWriter, r *http.Request) {
	if !s.eventAuthorized(w, r) {
		return
	}
	targets, revision, err := s.s.commandTargets()
	if err != nil {
		fail(w, 500, "目录命令配置无法读取")
		return
	}
	items := []commandTargetView{}
	for _, t := range targets {
		if v := s.commandTargetInfo(t); v.Enabled && v.Valid && eventRuleAllowed(r, t.Rule) {
			items = append(items, v)
		}
	}
	jsonOut(w, 200, object{"items": items, "revision": revision})
}

func (s *Server) saveCommandTargets(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision string          `json:"revision"`
		Items    []CommandTarget `json:"items"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(in.Items) > 128 {
		fail(w, 400, "最多配置 128 个目录命令")
		return
	}
	names := map[string]bool{}
	for i := range in.Items {
		t := &in.Items[i]
		t.Alias = strings.TrimSpace(t.Alias)
		bad := t.Alias == "" || len([]rune(t.Alias)) > 40 || strings.ContainsAny(t.Alias, "/\\") || strings.Contains("|help|帮助|list|列表|status|状态|", "|"+strings.ToLower(t.Alias)+"|")
		for _, c := range t.Alias {
			if unicode.IsSpace(c) || unicode.IsControl(c) {
				bad = true
			}
		}
		if bad {
			fail(w, 400, "目录名称需为 1–40 个字符，不能含空格或斜线，也不能使用帮助、列表、状态等保留名称")
			return
		}
		name := strings.ToLower(t.Alias)
		if names[name] {
			fail(w, 400, "目录命令名称重复："+t.Alias)
			return
		}
		names[name] = true
		clean, err := validRemotePath(t.Path)
		if err != nil {
			fail(w, 400, t.Alias+"：请填写完整云端目录路径")
			return
		}
		t.Path = clean
		v := s.commandTargetInfo(*t)
		if t.Enabled && !v.Valid {
			fail(w, 400, t.Alias+"："+v.Reason)
			return
		}
	}
	if in.Items == nil {
		in.Items = []CommandTarget{}
	}
	raw, err := json.Marshal(in.Items)
	if err != nil {
		fail(w, 500, "目录命令保存失败")
		return
	}
	tx, err := s.s.db.Begin()
	if err != nil {
		fail(w, 500, "目录命令保存失败")
		return
	}
	defer tx.Rollback()
	old := "[]"
	err = tx.QueryRow("SELECT value FROM settings WHERE id='command_targets'").Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, 500, "目录命令读取失败")
		return
	}
	if in.Revision != contentHash([]byte(old)) {
		fail(w, 409, "目录配置已更新，请重新加载列表后再保存；当前草稿仍保留")
		return
	}
	if _, err = tx.Exec("INSERT INTO settings(id,value) VALUES('command_targets',?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", string(raw)); err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 500, "目录命令保存失败，请稍后重试")
		return
	}
	jsonOut(w, 200, object{"ok": true, "revision": contentHash(raw)})
}

type automationHealth struct {
	Checked     int64  `json:"checked"`
	OK          bool   `json:"ok"`
	Note        string `json:"note"`
	Fingerprint string `json:"fingerprint"`
}

func (s *Server) checkAutomation(w http.ResponseWriter, r *http.Request) {
	c, err := s.s.integrationConfig()
	if err != nil {
		fail(w, 500, "通知配置无法读取")
		return
	}
	if !c.WebhookEnabled || c.Webhook == "" {
		fail(w, 400, "请先保存并启用 Webhook 通知")
		return
	}
	u, err := url.Parse(c.Webhook)
	if err != nil || validServiceURL(c.Webhook) != nil {
		fail(w, 400, "通知接收地址无效")
		return
	}
	u.Path = "/health"
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	h := automationHealth{Checked: time.Now().UnixMilli(), Fingerprint: contentHash([]byte(c.Webhook + "\x00" + c.WebhookSecret)), Note: "无法连接 Hermes 接收服务，请检查地址和服务状态"}
	res, err := client.Do(req)
	if err == nil {
		var body struct {
			Status   string `json:"status"`
			Platform string `json:"platform"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&body)
		res.Body.Close()
		if res.StatusCode == 200 && decodeErr == nil && body.Status == "ok" && body.Platform == "webhook" {
			h.OK = true
			h.Note = "Hermes 接收接口可达；发送测试通知可检查实际投递"
		} else {
			h.Note = "接收服务未通过 Hermes 健康检查，请核对 Webhook 地址"
		}
	}
	if err = s.s.setSetting("automation_health", scanJSON(h)); err != nil {
		fail(w, 500, "检查结果保存失败")
		return
	}
	jsonOut(w, 200, object{"ok": h.OK, "note": h.Note, "checked": h.Checked})
}

func (s *Server) automationPipeline() ([]object, error) {
	events, err := s.s.ingestEvents()
	if err != nil {
		return nil, err
	}
	items := []object{}
	for i, v := range events {
		if i >= 10 {
			break
		}
		item := object{"id": v.ID, "path": v.Path, "rule": v.Rule, "created": v.Created, "status": v.Status, "error": cleanRecordText(v.Error, 300), "job": "", "notify_status": "not_requested"}
		deliveryJob := fmt.Sprintf("ingest:%s:%d", v.ID, v.Revision)
		if v.Job != "" {
			j, er := s.s.job(v.Job)
			if er != nil && !errors.Is(er, sql.ErrNoRows) {
				return nil, er
			}
			seen := map[string]bool{}
			for depth := 0; er == nil && j.RetriedBy != "" && depth < 10 && !seen[j.ID]; depth++ {
				seen[j.ID] = true
				j, er = s.s.job(j.RetriedBy)
			}
			if er == nil && j.ID != "" {
				item["job"] = j.ID
				item["status"] = j.Status
				item["created_files"] = j.CreatedFiles
				item["skipped"] = j.Skipped
				item["errors"] = j.Errors
				item["message"] = cleanRecordText(j.Message, 300)
				deliveryJob = j.ID
			}
		}
		var id, status, message string
		var attempts int
		er := s.s.db.QueryRow("SELECT id,status,attempts,error FROM deliveries WHERE job=? AND kind='webhook' ORDER BY CASE status WHEN 'failed' THEN 0 WHEN 'pending' THEN 1 WHEN 'paused' THEN 2 ELSE 3 END,rowid DESC LIMIT 1", deliveryJob).Scan(&id, &status, &attempts, &message)
		if er != nil && !errors.Is(er, sql.ErrNoRows) {
			return nil, er
		}
		if er == nil {
			item["delivery"] = id
			item["notify_status"] = status
			item["attempts"] = attempts
			item["notify_error"] = cleanRecordText(message, 300)
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Server) automation(w http.ResponseWriter, r *http.Request) {
	targets, revision, err := s.s.commandTargets()
	if err != nil {
		fail(w, 500, "目录命令配置无法读取")
		return
	}
	items := []commandTargetView{}
	for _, t := range targets {
		items = append(items, s.commandTargetInfo(t))
	}
	c, err := s.s.integrationConfig()
	if err != nil {
		fail(w, 500, "通知配置无法读取")
		return
	}
	h := automationHealth{}
	_ = json.Unmarshal([]byte(s.s.setting("automation_health", "{}")), &h)
	if h.Fingerprint != contentHash([]byte(c.Webhook+"\x00"+c.WebhookSecret)) {
		h = automationHealth{}
	}
	host := ""
	if u, er := url.Parse(c.Webhook); er == nil {
		host = u.Host
	}
	pipeline, err := s.automationPipeline()
	if err != nil {
		fail(w, 500, "联动任务状态读取失败")
		return
	}
	jsonOut(w, 200, object{"targets": items, "revision": revision, "pipeline": pipeline, "connection": object{"enabled": c.WebhookEnabled, "signed": c.WebhookSecret != "", "host": host, "checked": h.Checked, "ok": h.OK, "note": h.Note}})
}
