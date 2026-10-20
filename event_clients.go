package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type EventClient struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Enabled  bool     `json:"enabled"`
	Rules    []string `json:"rules"`
	Created  int64    `json:"created"`
	LastUsed int64    `json:"last_used"`
}
type eventAccess struct {
	ID, Name string
	Rules    []string
	Legacy   bool
}
type eventAccessKey struct{}

func eventRuleAllowed(r *http.Request, rule string) bool {
	a, ok := r.Context().Value(eventAccessKey{}).(eventAccess)
	if !ok {
		return false
	}
	if a.Legacy {
		return true
	}
	for _, id := range a.Rules {
		if id == rule {
			return true
		}
	}
	return false
}
func eventTokenHash(token string) string {
	v := sha256.Sum256([]byte(token))
	return hex.EncodeToString(v[:])
}
func (s *Server) authorizeEvent(w http.ResponseWriter, r *http.Request) bool {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if hmac.Equal([]byte(token), []byte(s.eventToken)) && s.s.setting("event_legacy_enabled", "1") == "1" {
		*r = *r.WithContext(context.WithValue(r.Context(), eventAccessKey{}, eventAccess{ID: "legacy", Name: "兼容接入", Legacy: true}))
		return true
	}
	var c EventClient
	var raw string
	err := s.s.db.QueryRow("SELECT id,name,enabled,rules FROM event_clients WHERE token_hash=?", eventTokenHash(token)).Scan(&c.ID, &c.Name, &c.Enabled, &raw)
	if err == nil && c.Enabled && json.Unmarshal([]byte(raw), &c.Rules) == nil {
		*r = *r.WithContext(context.WithValue(r.Context(), eventAccessKey{}, eventAccess{ID: c.ID, Name: c.Name, Rules: c.Rules}))
		return true
	}
	fail(w, http.StatusUnauthorized, "自动生成接入凭据无效或已停用")
	return false
}

type eventResponse struct {
	http.ResponseWriter
	status int
}

func (w *eventResponse) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *eventResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (s *Server) eventEndpoint(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.s.restoreRestricted {
			fail(w, 503, "恢复待核对期间不接受外部事件")
			return
		}
		response := &eventResponse{ResponseWriter: w}
		fn(response, r)
		a, ok := r.Context().Value(eventAccessKey{}).(eventAccess)
		if !ok {
			return
		}
		if response.status == 0 {
			response.status = 200
		}
		now := time.Now().Unix()
		if a.Legacy {
			_ = s.s.setSetting("event_legacy_last_used", scanJSON(now))
		} else {
			s.s.db.Exec("UPDATE event_clients SET last_used=? WHERE id=?", now, a.ID)
		}
		s.s.db.Exec("INSERT INTO event_calls(client,name,created,action,status) VALUES(?,?,?,?,?)", a.ID, a.Name, now, r.URL.Path, response.status)
		s.s.db.Exec("DELETE FROM event_calls WHERE id NOT IN (SELECT id FROM event_calls ORDER BY id DESC LIMIT 500)")
	}
}
func (s *Server) eventClients(w http.ResponseWriter, r *http.Request) {
	items := []EventClient{}
	rows, err := s.s.db.Query("SELECT id,name,enabled,rules,created,last_used FROM event_clients ORDER BY created,id")
	if err != nil {
		fail(w, 500, "接入列表读取失败")
		return
	}
	for rows.Next() {
		var c EventClient
		var raw string
		if rows.Scan(&c.ID, &c.Name, &c.Enabled, &raw, &c.Created, &c.LastUsed) == nil && json.Unmarshal([]byte(raw), &c.Rules) == nil {
			items = append(items, c)
		}
	}
	rows.Close()
	calls := []object{}
	rows, err = s.s.db.Query("SELECT client,name,created,action,status FROM event_calls ORDER BY id DESC LIMIT 20")
	if err != nil {
		fail(w, 500, "调用记录读取失败")
		return
	}
	for rows.Next() {
		var id, name, action string
		var at int64
		var status int
		if rows.Scan(&id, &name, &at, &action, &status) == nil {
			calls = append(calls, object{"client": id, "name": name, "created": at, "action": action, "status": status})
		}
	}
	rows.Close()
	var last int64
	_ = json.Unmarshal([]byte(s.s.setting("event_legacy_last_used", "0")), &last)
	jsonOut(w, 200, object{"items": items, "calls": calls, "legacy": object{"enabled": s.s.setting("event_legacy_enabled", "1") == "1", "last_used": last}})
}
func (s *Server) validateEventClient(c EventClient) error {
	if strings.TrimSpace(c.Name) == "" || len([]rune(c.Name)) > 60 || strings.ContainsAny(c.Name, "\r\n\t") {
		return errors.New("请填写 1–60 个字符的应用名称")
	}
	if len(c.Rules) == 0 || len(c.Rules) > 128 {
		return errors.New("至少选择一个允许生成的目录规则")
	}
	seen := map[string]bool{}
	for _, id := range c.Rules {
		if seen[id] {
			return errors.New("目录规则重复")
		}
		seen[id] = true
		if _, err := s.s.rule(id); err != nil {
			return errors.New("所选目录规则已移除，请重新选择")
		}
	}
	return nil
}
func (s *Server) saveEventClient(w http.ResponseWriter, r *http.Request) {
	var c EventClient
	if err := decode(w, r, &c); err != nil {
		fail(w, 400, err.Error())
		return
	}
	c.Name = strings.TrimSpace(c.Name)
	if err := s.validateEventClient(c); err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.s.mu.Lock()
	defer s.s.mu.Unlock()
	token := ""
	if c.ID == "" {
		var count int
		s.s.db.QueryRow("SELECT count(*) FROM event_clients").Scan(&count)
		if count >= 32 {
			fail(w, 400, "最多配置 32 个接入应用")
			return
		}
		c.ID = randomID()
		c.Created = time.Now().Unix()
		token = randomID() + randomID()
		_, err := s.s.db.Exec("INSERT INTO event_clients(id,name,token_hash,enabled,rules,created) VALUES(?,?,?,?,?,?)", c.ID, c.Name, eventTokenHash(token), c.Enabled, scanJSON(c.Rules), c.Created)
		if err != nil {
			fail(w, 500, "创建接入失败")
			return
		}
	} else {
		result, err := s.s.db.Exec("UPDATE event_clients SET name=?,enabled=?,rules=? WHERE id=?", c.Name, c.Enabled, scanJSON(c.Rules), c.ID)
		if err != nil {
			fail(w, 500, "保存接入失败")
			return
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			fail(w, 404, "接入应用已删除")
			return
		}
	}
	jsonOut(w, 200, object{"ok": true, "id": c.ID, "token": token})
}
func (s *Server) rotateEventClient(w http.ResponseWriter, r *http.Request) {
	token := randomID() + randomID()
	result, err := s.s.db.Exec("UPDATE event_clients SET token_hash=? WHERE id=?", eventTokenHash(token), r.PathValue("id"))
	if err != nil {
		fail(w, 500, "更换凭据失败")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		fail(w, 404, "接入应用不存在")
		return
	}
	jsonOut(w, 200, object{"token": token, "note": "旧凭据已失效，新凭据仅显示此次。"})
}
func (s *Server) removeEventClient(w http.ResponseWriter, r *http.Request) {
	result, err := s.s.db.Exec("DELETE FROM event_clients WHERE id=?", r.PathValue("id"))
	if err != nil {
		fail(w, 500, "撤销接入失败")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		fail(w, 404, "接入应用已删除")
		return
	}
	jsonOut(w, 200, object{"ok": true})
}
func (s *Server) legacyEventClient(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	v := "0"
	if in.Enabled {
		v = "1"
	}
	if err := s.s.setSetting("event_legacy_enabled", v); err != nil {
		fail(w, 500, "兼容接入保存失败")
		return
	}
	jsonOut(w, 200, object{"ok": true})
}
