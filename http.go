package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed web/* web/assets/*
var assets embed.FS

type Server struct {
	s               *Store
	engine          *Engine
	clouds          Clouds
	adminHash       [32]byte
	adminCredential *adminCredential
	authMu          sync.RWMutex
	eventToken      string
	output          string
	sessions        map[string]time.Time
	mu              sync.Mutex
	attempts        map[string][]time.Time
	links           *playbackCache
	playMu          sync.Mutex
	playHistory     []PlaybackRecord
	probeClient     *http.Client
	maintenanceMu   sync.Mutex
	backupMu        sync.Mutex
	backupFault     func(string) error // durability/process-interruption test seam
	restart         func()
}

func newServer(s *Store, e *Engine, p Clouds, data, output string) (*Server, error) {
	credential, err := loadAdminCredential(data)
	if err != nil {
		return nil, err
	}
	password := os.Getenv("STRMLINK_ADMIN_PASSWORD")
	if credential == nil && password == "" {
		name := filepath.Join(data, "initial-admin-password.txt")
		b, er := os.ReadFile(name)
		if os.IsNotExist(er) {
			password = randomID()
			if er = os.WriteFile(name, []byte(password+"\n"), 0600); er != nil {
				return nil, er
			}
		} else if er != nil {
			return nil, er
		} else {
			password = strings.TrimSpace(string(b))
		}
	}
	if credential == nil && password == "" {
		return nil, errors.New("管理员密码不能为空")
	}
	event, er := keyFile(data, "events.key", 32)
	if er != nil {
		return nil, er
	}
	token := fmt.Sprintf("%x", event)
	return &Server{s: s, engine: e, clouds: p, adminHash: sha256.Sum256([]byte(password)), adminCredential: credential, eventToken: token, output: output, sessions: map[string]time.Time{}, attempts: map[string][]time.Time{}, links: newPlaybackCache()}, nil
}

func (s *Server) currentEventToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventToken
}

// rotateEventToken replaces the legacy event token: the new token is written
// to events.key atomically (temp file + fsync + rename + directory sync) and
// only then swapped in, so the old token stops working the moment a rotation
// succeeds. The on-disk format stays the raw 32 bytes that newServer loads
// through keyFile.
func (s *Server) rotateEventToken() error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	dir := s.s.dir
	f, err := os.OpenFile(filepath.Join(dir, ".events-"+randomID()+".tmp"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, "events.key")); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	s.mu.Lock()
	s.eventToken = fmt.Sprintf("%x", raw)
	s.mu.Unlock()
	return nil
}

func (s *Server) eventTokenView(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, object{"event_token": s.currentEventToken()})
}

func (s *Server) rotateEventTokenHandler(w http.ResponseWriter, r *http.Request) {
	if err := s.rotateEventToken(); err != nil {
		fail(w, 500, "事件令牌轮换失败，请检查数据目录权限后重试")
		return
	}
	jsonOut(w, 200, object{"ok": true, "event_token": s.currentEventToken()})
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	jsonOut(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil {
		return errors.New("请求格式错误或字段不正确")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("请求包含多余内容")
	}
	return nil
}
func (s *Server) authed(r *http.Request) bool {
	c, e := r.Cookie("strmlink_session")
	if e != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, c.Value)
		return false
	}
	return true
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, e := url.Parse(origin)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host == r.Host
}
func (s *Server) guard(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			fail(w, 401, "请先登录管理页面")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && !sameOrigin(r) {
			fail(w, 403, "请求来源不匹配")
			return
		}
		if s.s.restoreRestricted && !restoreReviewRequestAllowed(r) {
			fail(w, 409, "请先完成恢复核对；当前仅开放查看、密码更新和只读诊断")
			return
		}
		fn(w, r)
	}
}
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.s.db.PingContext(r.Context()) != nil {
			fail(w, 503, "数据库不可用")
			return
		}
		jsonOut(w, 200, map[string]string{"status": "ok", "version": appVersion})
	})
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/password", s.guard(s.changePassword))
	mux.HandleFunc("POST /api/logout", s.guard(func(w http.ResponseWriter, r *http.Request) {
		if c, er := r.Cookie("strmlink_session"); er == nil {
			s.mu.Lock()
			delete(s.sessions, c.Value)
			s.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: "strmlink_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		jsonOut(w, 200, object{"ok": true})
	}))
	mux.HandleFunc("GET /api/state", s.guard(s.state))
	mux.HandleFunc("POST /api/backups/export", s.guard(s.exportBackup))
	mux.HandleFunc("GET /api/restore/status", s.guard(s.restoreStatus))
	mux.HandleFunc("POST /api/restore/import", s.guard(s.importBackup))
	mux.HandleFunc("POST /api/restore/{action}", s.guard(s.restoreAction))
	mux.HandleFunc("GET /api/dashboard", s.guard(s.dashboard))
	mux.HandleFunc("GET /api/outputs", s.guard(s.outputInventory))
	mux.HandleFunc("POST /api/outputs/check", s.guard(s.checkOutputs))
	mux.HandleFunc("POST /api/outputs/repair", s.guard(s.repairOutputs))
	mux.HandleFunc("GET /api/notifications", s.guard(s.notifications))
	mux.HandleFunc("POST /api/notifications/read", s.guard(s.notifications))
	mux.HandleFunc("GET /api/integrations", s.guard(s.integrations))
	mux.HandleFunc("POST /api/integrations", s.guard(s.integrations))
	mux.HandleFunc("POST /api/integrations/test", s.guard(s.testIntegration))
	mux.HandleFunc("GET /api/notification-channels", s.guard(s.notificationChannels))
	mux.HandleFunc("POST /api/notification-channels", s.guard(s.saveNotificationChannel))
	mux.HandleFunc("POST /api/notification-channels/{id}/remove", s.guard(s.removeNotificationChannel))
	mux.HandleFunc("POST /api/notification-channels/{id}/test", s.guard(s.testNotificationChannel))
	mux.HandleFunc("GET /api/event-clients", s.guard(s.eventClients))
	mux.HandleFunc("POST /api/event-clients", s.guard(s.saveEventClient))
	mux.HandleFunc("POST /api/event-clients/{id}/rotate", s.guard(s.rotateEventClient))
	mux.HandleFunc("POST /api/event-clients/{id}/remove", s.guard(s.removeEventClient))
	mux.HandleFunc("POST /api/event-clients/legacy", s.guard(s.legacyEventClient))
	mux.HandleFunc("POST /api/deliveries/{id}/retry", s.guard(s.retryDelivery))
	mux.HandleFunc("POST /api/filters/preview", s.guard(s.filterPreview))
	mux.HandleFunc("POST /api/rules/{id}/check", s.guard(s.preflight))
	mux.HandleFunc("GET /api/records", s.guard(s.records))
	mux.HandleFunc("GET /api/records/trace", s.guard(s.recordTrace))
	mux.HandleFunc("POST /api/accounts", s.guard(s.saveAccount))
	mux.HandleFunc("POST /api/accounts/{id}/check", s.guard(s.checkAccount))
	mux.HandleFunc("GET /api/accounts/{id}/health", s.guard(s.healthAccount))
	mux.HandleFunc("POST /api/accounts/{id}/recover", s.guard(s.recoverAccount))
	mux.HandleFunc("POST /api/accounts/{id}/quota", s.guard(s.accountQuota))
	mux.HandleFunc("POST /api/accounts/{id}/enabled", s.guard(s.enableAccount))
	mux.HandleFunc("POST /api/accounts/{id}/remove", s.guard(s.removeAccount))
	mux.HandleFunc("GET /api/browse", s.guard(s.browse))
	mux.HandleFunc("POST /api/rules", s.guard(s.saveRule))
	mux.HandleFunc("GET /api/output/directories", s.guard(s.outputDirectories))
	mux.HandleFunc("POST /api/jobs", s.guard(s.startJob))
	mux.HandleFunc("POST /api/jobs/retry-failed", s.guard(s.retryFailed))
	mux.HandleFunc("POST /api/jobs/{id}/retry", s.guard(s.retryJob))
	mux.HandleFunc("POST /api/rules/{id}/remove", s.guard(s.removeRule))
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.guard(func(w http.ResponseWriter, r *http.Request) {
		if er := s.engine.stop(r.PathValue("id")); er != nil {
			fail(w, 400, er.Error())
			return
		}
		jsonOut(w, 200, object{"ok": true})
	}))
	mux.HandleFunc("POST /api/settings", s.guard(s.settings))
	mux.HandleFunc("GET /api/settings/event-token", s.guard(s.eventTokenView))
	mux.HandleFunc("POST /api/settings/event-token/rotate", s.guard(s.rotateEventTokenHandler))
	mux.HandleFunc("POST /api/maintenance/links", s.guard(s.previewLinks))
	mux.HandleFunc("GET /api/changes", s.guard(s.changeHistory))
	mux.HandleFunc("POST /api/changes/preview", s.guard(s.previewChangeRepairHTTP))
	mux.HandleFunc("POST /api/changes/{id}/apply", s.guard(s.applyChangeRepairHTTP))
	mux.HandleFunc("GET /api/changes/{id}", s.guard(s.getChangeRepairHTTP))
	mux.HandleFunc("POST /api/maintenance/migration", s.guard(s.previewMigration))
	mux.HandleFunc("POST /api/maintenance/migration/scan", s.guard(s.scanInplace))
	mux.HandleFunc("POST /api/maintenance/migration/inplace", s.guard(s.previewInplace))
	mux.HandleFunc("POST /api/maintenance/{id}/apply", s.guard(s.applyPlan))
	mux.HandleFunc("POST /api/maintenance/{id}/rollback", s.guard(s.rollbackPlan))
	mux.HandleFunc("GET /api/maintenance/{id}/report", s.guard(func(w http.ResponseWriter, r *http.Request) {
		s.maintenanceMu.Lock()
		defer s.maintenanceMu.Unlock()
		p, e := s.loadPlan(r.PathValue("id"))
		if e != nil {
			fail(w, 404, "预览记录不存在")
			return
		}
		report := planSummary(p)
		items := []object{}
		for _, v := range p.Items {
			items = append(items, object{"path": v.Path, "error": v.Error, "applied": v.Applied, "storage": v.Storage, "source": v.Source, "before": v.Original, "after": v.Content})
		}
		report["report_items"] = items
		jsonOut(w, 200, report)
	}))
	mux.HandleFunc("POST /api/events", s.eventEndpoint(s.event))
	mux.HandleFunc("GET /api/events/status", s.eventEndpoint(s.eventStatus))
	mux.HandleFunc("GET /api/events/catalog", s.eventEndpoint(s.commandCatalog))
	mux.HandleFunc("GET /api/automation", s.guard(s.automation))
	mux.HandleFunc("POST /api/automation/targets", s.guard(s.saveCommandTargets))
	mux.HandleFunc("POST /api/automation/check", s.guard(s.checkAutomation))
	mux.HandleFunc("POST /api/integrations/{section}", s.guard(s.integrations))
	mux.HandleFunc("POST /api/ingest/{id}/retry", s.guard(s.retryIngest))
	mux.HandleFunc("GET /api/resolve/{id}", s.guard(s.diagnose))
	mux.HandleFunc("GET /api/media/{id}/playlist", s.guard(s.mediaPlaylist))
	mux.HandleFunc("GET /api/rules/{id}/playlist", s.guard(s.rulePlaylist))
	mux.HandleFunc("POST /api/playback/{id}/check", s.guard(s.checkPlayback))
	mux.HandleFunc("POST /api/media/{id}/diagnose", s.guard(s.diagnoseMedia))
	mux.HandleFunc("GET /api/diagnostics/{id}/export", s.guard(s.exportDiagnostic))
	mux.HandleFunc("POST /api/playback/cache/clear", s.guard(func(w http.ResponseWriter, r *http.Request) { s.links.clear(); jsonOut(w, 200, object{"ok": true}) }))
	mux.HandleFunc("GET /s/{id}/{name}", s.play)
	mux.HandleFunc("GET /redirect", func(w http.ResponseWriter, r *http.Request) {
		fail(w, 501, "当前服务未接管旧 /redirect 链接，请先迁移或使用新生成的 STRM")
	})
	sub, _ := fs.Sub(assets, "web")
	static := http.FileServer(http.FS(sub))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		r = activityRequest(r)
		if info, ok := r.Context().Value(requestInfoKey{}).(requestInfo); ok {
			w.Header().Set("X-YoLink-Request", info.ID)
		}
		// Management API requests get a bounded context so a stuck handler
		// cannot hold a connection forever. Playback/proxy streaming (/s/)
		// never passes through a deadline, and the backup export/import
		// endpoints manage their own longer timeouts, so they are exempt.
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/restore/import" && r.URL.Path != "/api/backups/export" {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
			defer cancel()
			r = r.WithContext(ctx)
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, 403, "请求来源不匹配")
		return
	}
	if !s.allowAuthAttempt(r) {
		fail(w, 429, "尝试次数过多，请一分钟后重试")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, 400, e.Error())
		return
	}
	s.authMu.RLock()
	defer s.authMu.RUnlock()
	if !s.passwordMatches(in.Password) {
		fail(w, 401, "密码不正确")
		return
	}
	token := randomID() + randomID()
	now := time.Now()
	s.mu.Lock()
	for k, exp := range s.sessions {
		if now.After(exp) {
			delete(s.sessions, k)
		}
	}
	if len(s.sessions) > 100 {
		clear(s.sessions)
	}
	s.sessions[token] = now.Add(12 * time.Hour)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "strmlink_session", Value: token, Path: "/", MaxAge: 43200, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	jsonOut(w, 200, object{"ok": true})
}
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	accounts, e := s.s.accounts()
	if e != nil {
		fail(w, 500, "账号数据读取失败")
		return
	}
	rules, e := s.s.rules()
	if e != nil {
		fail(w, 500, "规则数据读取失败")
		return
	}
	jobs, e := s.s.consoleJobs()
	if e != nil {
		fail(w, 500, "任务数据读取失败")
		return
	}
	media := []object{}
	rows, e := s.s.db.Query("SELECT payload FROM media ORDER BY rowid DESC LIMIT 12")
	if e != nil {
		fail(w, 500, "媒体索引读取失败")
		return
	}
	for rows.Next() {
		var p string
		var m Media
		if rows.Scan(&p) == nil && json.Unmarshal([]byte(p), &m) == nil {
			media = append(media, object{"id": m.ID, "name": m.Name, "path": m.Path, "account": m.Account, "url": "/s/" + m.ID + "/" + url.PathEscape(m.Name) + "?k=" + s.s.signature(m.ID)})
		}
	}
	rows.Close()
	overview, e := s.s.generationOverview()
	if e != nil {
		fail(w, 500, "生成统计读取失败")
		return
	}
	failures, e := s.s.unresolvedJobs()
	if e != nil {
		fail(w, 500, "失败记录读取失败")
		return
	}
	reports, e := s.s.accountReports()
	if e != nil {
		fail(w, 500, "网盘状态读取失败")
		return
	}
	ingest, e := s.s.ingestEvents()
	if e != nil {
		fail(w, 500, "入库记录读取失败")
		return
	}
	jsonOut(w, 200, object{"restore_restricted": s.s.restoreRestricted, "rule_latest": s.s.latestRules(), "version": appVersion, "accounts": accounts, "account_reports": reports, "playback": s.playbackState(), "ingest": ingest, "rules": rules, "jobs": jobs, "media": media, "overview": overview, "failures": failures, "settings": object{"public_base": s.s.setting("public_base", ""), "output_root": s.engine.outputMounts[0].Path, "output_mounts": s.engine.outputMounts}})
}
func (s *Server) saveAccount(w http.ResponseWriter, r *http.Request) {
	var a Account
	if er := decode(w, r, &a); er != nil {
		fail(w, 400, er.Error())
		return
	}
	if a.Name == "" || len(a.Name) > 100 {
		fail(w, 400, "账号名称不能为空，且不能超过 100 字节")
		return
	}
	if pr, ok := s.clouds.(*Providers); ok {
		mu := pr.lock(a.ID)
		mu.Lock()
		defer mu.Unlock()
	}
	switch a.Provider {
	case "139", "139old", "quark", "quarktv", "ali", "115":
	default:
		fail(w, 400, "不支持此网盘类型")
		return
	}
	if a.ID == "" {
		a.ID = randomID()
		a.Disabled = false
		a.Revision = 1
	} else {
		old, e := s.s.account(a.ID)
		if e != nil {
			fail(w, 400, "账号不存在")
			return
		}
		if old.Provider != a.Provider || old.Drive != a.Drive {
			fail(w, 400, "已有账号不能切换网盘类型或空间 ID，请新增账号")
			return
		}
		if a.Secrets == nil {
			a.Secrets = map[string]string{}
		}
		a.Disabled = old.Disabled
		a.Revision = old.Revision + 1
		for k, v := range old.Secrets {
			if a.Secrets[k] == "" {
				a.Secrets[k] = v
			}
		}
	}
	if a.Secrets == nil {
		a.Secrets = map[string]string{}
	}
	allowed := map[string]bool{"authorization": true, "cookie": true, "access_token": true, "refresh_token": true, "client_id": true, "client_secret": true, "expires_at": true, "device_id": true, "refresh_endpoint": true}
	for k := range a.Secrets {
		if !allowed[k] && k != "phone" && k != "api_host" {
			fail(w, 400, "不支持此凭据字段")
			return
		}
	}
	delete(a.Secrets, "api_host")
	delete(a.Secrets, "phone")
	if a.Root == "" {
		switch a.Provider {
		case "139":
			a.Root = "/"
		case "quark", "quarktv", "115":
			a.Root = "0"
		default:
			a.Root = "root"
		}
	}
	if a.Provider == "ali" && a.Drive == "" {
		fail(w, 400, "请填写阿里空间 drive_id")
		return
	}
	if a.Provider == "115" {
		cookie, e := validate115Cookie(a.Secrets["cookie"])
		if e != nil {
			fail(w, 400, e.Error())
			return
		}
		if !numeric115ID(a.Root) {
			fail(w, 400, "115 根目录 ID 应为数字，留空使用 0")
			return
		}
		a.Secrets["cookie"] = cookie
	}
	if e := s.s.saveAccount(a); e != nil {
		fail(w, 500, "账号保存失败")
		return
	}
	_, _ = s.s.db.Exec("DELETE FROM account_reports WHERE id=?", a.ID)
	a.Secrets = nil
	jsonOut(w, 200, a)
}
func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	aid := r.URL.Query().Get("account")
	a, e := s.s.account(aid)
	if e != nil {
		fail(w, 400, "请先添加网盘账号")
		return
	}
	if a.Disabled {
		fail(w, 409, "此网盘已停用，请先在网盘管理中启用")
		return
	}
	parent := r.URL.Query().Get("folder")
	if parent == "" {
		parent = a.Root
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	files, e := s.clouds.List(ctx, aid, parent)
	if e != nil {
		fail(w, 502, e.Error())
		return
	}
	jsonOut(w, 200, object{"folder": parent, "entries": files})
}
func (s *Server) saveRule(w http.ResponseWriter, r *http.Request) {
	var rule Rule
	if e := decode(w, r, &rule); e != nil {
		fail(w, 400, e.Error())
		return
	}
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	s.s.mu.Lock()
	defer s.s.mu.Unlock()
	if rule.ID == "" {
		rule.ID = randomID()
	} else {
		old, er := s.s.rule(rule.ID)
		if er != nil {
			fail(w, 400, "规则不存在")
			return
		}
		if old.Account != rule.Account || old.Folder != rule.Folder || old.SourcePath != rule.SourcePath || old.Output != rule.Output {
			var active int
			if e := s.s.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE status IN ('running','queued') AND json_extract(payload,'$.rule')=?", rule.ID).Scan(&active); e != nil {
				fail(w, 500, "任务状态读取失败")
				return
			}
			if active > 0 {
				fail(w, 409, "该目录有生成任务，请先取消或等待完成再调整映射")
				return
			}
		}
		rule.LastRun = old.LastRun
	}
	if _, e := s.s.account(rule.Account); e != nil {
		fail(w, 400, "账号不存在")
		return
	}
	if err := rule.Filter.validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	var e error
	rule.Output, e = s.engine.normalizeOutputDirectory(rule.Output)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	rule.SourcePath, e = validRemotePath(rule.SourcePath)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	if rule.Name == "" || rule.Folder == "" {
		fail(w, 400, "请填写名称并选择源目录")
		return
	}
	if rule.Enabled && rule.Interval < 60 {
		fail(w, 400, "自动同步间隔至少 60 秒")
		return
	}
	if rule.Interval == 0 {
		rule.Interval = 1800
	}
	rule.LastRun = time.Now().Unix()
	if e = s.s.saveRule(rule); e != nil {
		fail(w, 500, "保存规则失败")
		return
	}
	jsonOut(w, 200, rule)
}

type jobRequest struct {
	FullReconcile bool   `json:"full_reconcile,omitempty"`
	Path          string `json:"path,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Rule          string `json:"rule"`
	Folder        string `json:"folder"`
	SourcePath    string `json:"source_path"`
	DryRun        bool   `json:"dry_run"`
	Limit         int    `json:"limit"`
}

func (s *Server) startJob(w http.ResponseWriter, r *http.Request) {
	var in jobRequest
	if e := decode(w, r, &in); e != nil {
		fail(w, 400, e.Error())
		return
	}
	if in.Path != "" {
		s.ingestEvent(w, r, in)
		return
	}
	if in.FullReconcile && (in.Folder != "" || in.SourcePath != "" || in.DryRun || in.Limit != 0) {
		fail(w, 400, "完整核对需要不限量的规则根目录任务")
		return
	}
	j, e := s.engine.enqueueJob(in.Rule, in.Folder, in.SourcePath, in.DryRun, in.Limit, "manual", "", in.FullReconcile)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	jsonOut(w, 202, j)
}
func validBase(v string) (string, error) {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	u, e := url.Parse(v)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("填写完整服务地址，例如 http://192.168.1.10:8796，不含路径或参数")
	}
	return v, nil
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PublicBase string `json:"public_base"`
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, 400, e.Error())
		return
	}
	v, e := validBase(in.PublicBase)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	if e = s.s.setSetting("public_base", v); e != nil {
		fail(w, 500, "保存地址失败")
		return
	}
	jsonOut(w, 200, object{"ok": true})
}
func (s *Server) event(w http.ResponseWriter, r *http.Request) {
	if !s.eventAuthorized(w, r) {
		return
	}
	var in jobRequest
	if e := decode(w, r, &in); e != nil {
		fail(w, 400, e.Error())
		return
	}
	if !eventRuleAllowed(r, in.Rule) {
		fail(w, 403, "此接入凭据无权使用该目录规则")
		return
	}
	if in.Path != "" {
		s.ingestEvent(w, r, in)
		return
	}
	j, e := s.engine.enqueue(in.Rule, in.Folder, in.SourcePath, in.DryRun, in.Limit)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	jsonOut(w, 202, j)
}
func (s *Server) diagnose(w http.ResponseWriter, r *http.Request) {
	m, e := s.s.media(r.PathValue("id"))
	if e != nil {
		fail(w, 404, "媒体尚未生成")
		return
	}
	a, e := s.s.account(m.Account)
	if e != nil || a.Disabled {
		fail(w, 409, "媒体所属网盘不可用或已停用，请先启用")
		return
	}
	target, hit, e := s.resolveMedia(r.Context(), m, false)
	if e != nil {
		log.Printf("resolve diagnose failed for media %s: %v", m.ID, e)
		fail(w, 502, "播放地址获取失败，请稍后重试")
		return
	}
	u, _ := url.Parse(target.URL)
	jsonOut(w, 200, object{"cache_hit": hit, "mode": target.Mode, "target_host": u.Hostname(), "requires_headers": len(target.Headers) > 0, "note": "已获取下载地址；还需在实际播放器验证取流与拖动"})
}
func (s *Server) play(w http.ResponseWriter, r *http.Request) {
	if s.s.restoreRestricted {
		fail(w, 503, "恢复资料待核对，暂未开放播放入口")
		return
	}
	id := r.PathValue("id")
	started := time.Now()
	record := PlaybackRecord{Media: cleanRecordText(id, 128), Name: "未知媒体", Action: "play", Status: 502, Info: s.mediaActivity(r.Context(), Media{})}
	defer func() { record.Millis = time.Since(started).Milliseconds(); s.recordPlayback(record) }()
	if !hmac.Equal([]byte(r.URL.Query().Get("k")), []byte(s.s.signature(id))) {
		record.Status = 403
		record.Error = "播放链接签名无效"
		fail(w, 403, "播放链接签名无效")
		return
	}
	m, e := s.s.media(id)
	if e != nil {
		record.Status = 404
		record.Error = "媒体索引不存在"
		fail(w, 404, "媒体索引不存在")
		return
	}
	record.Media = m.ID
	record.Name = m.Name
	record.Info = s.mediaActivity(r.Context(), m)
	a, e := s.s.account(m.Account)
	if e != nil || a.Disabled {
		record.Status = 409
		record.Error = "媒体所属网盘不可用或已停用"
		fail(w, 409, "媒体所属网盘不可用或已停用，请先启用")
		return
	}
	target, hit, e := s.resolveMedia(r.Context(), m, false)
	record.Cache = hit
	record.Mode = target.Mode
	record.Info.Host = targetHost(target)
	if e != nil {
		record.Error = e.Error()
		fail(w, 502, "播放地址获取失败，请稍后重试或运行取流自检")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-StrmLink-Mode", target.Mode)
	if target.Mode == "proxy-required" {
		record.Status = 409
		record.Error = "当前通道需要中转，尚未允许"
		fail(w, 409, "此账号的原文件地址需要配套请求头；请在账号设置允许服务器中转")
		return
	}
	if target.Mode == "302" {
		if r.Method == "HEAD" {
			record.Action = "head"
			record.Status = s.playbackHead(w, r, target, m.Size)
			if record.Status != 200 {
				record.Error = "HEAD 预检失败，请运行取流自检"
			}
			record.Note = "HEAD 使用有界 GET 检查元数据；媒体 GET 仍返回 302"
			return
		}
		w.Header().Set("Location", target.URL)
		record.Status = 302
		w.WriteHeader(http.StatusFound)
		return
	}
	record.Status = 0
	record.Note = "已解析中转地址；此条记录不代表取流成功"
	record.Status, record.Bytes, e = s.proxy(w, r, target)
	if e != nil {
		record.Error = e.Error()
	}
}
func (s *Server) proxy(w http.ResponseWriter, r *http.Request, target PlaybackTarget) (int, int64, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, e
		}
		for _, ip := range ips {
			if ip.IP.IsLoopback() || ip.IP.IsPrivate() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsUnspecified() || ip.IP.IsMulticast() {
				return nil, errors.New("网盘地址解析到了内网")
			}
		}
		var last error
		for _, ip := range ips {
			conn, e := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, last
	}
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequestWithContext(r.Context(), r.Method, target.URL, nil)
	if e != nil {
		fail(w, 502, "下载地址无效")
		return 502, 0, errors.New("下载地址无效")
	}
	headers := target.Headers
	if headers == nil {
		headers = http.Header{}
	}
	req.Header = headers.Clone()
	for _, h := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, e := client.Do(req)
	if e != nil {
		fail(w, 502, "原文件中转连接失败")
		return 502, 0, errors.New("原文件中转连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		fail(w, 502, "网盘下载地址要求再次跳转，请重新授权后重试")
		return 502, 0, errors.New("原文件下载地址要求再次跳转")
	}
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method != "HEAD" {
		n, copyErr := io.Copy(w, resp.Body)
		if copyErr != nil {
			return resp.StatusCode, n, errors.New("中转读取或客户端连接中断")
		}
		return resp.StatusCode, n, nil
	}
	return resp.StatusCode, 0, nil
}
