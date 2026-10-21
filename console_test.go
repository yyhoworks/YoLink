package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFilterPresetsPreviewAndGenerationAgree(t *testing.T) {
	f := setup(t)
	f.r.Filter = FileFilter{Preset: "video", ExcludeDirs: "剧集"}
	if err := f.s.saveRule(f.r); err != nil {
		t.Fatal(err)
	}
	preview := generate(t, f, true, 0)
	if preview.Seen != 1 || preview.Filtered != 2 || preview.CreatedFiles != 1 {
		t.Fatalf("preview %+v", preview)
	}
	result := generate(t, f, false, 0)
	if result.CreatedFiles != 1 || result.Filtered != 2 {
		t.Fatalf("generate %+v", result)
	}
	if _, err := os.Stat(filepath.Join(f.output, "test/media/音乐.strm")); !os.IsNotExist(err) {
		t.Fatal("excluded audio was generated")
	}
	if (FileFilter{Preset: "custom", Extensions: "mkv,av1"}).validate() != nil {
		t.Fatal("custom extensions rejected")
	}
	if (FileFilter{Pattern: "["}).validate() == nil {
		t.Fatal("invalid regex accepted")
	}
	if (FileFilter{Preset: "audio", MinBytes: 5}).media(Entry{Name: "music.flac", Size: 4}) {
		t.Fatal("minimum size ignored")
	}
}
func TestDifferencePreviewPreservesConflictAndCountsExisting(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	file := filepath.Join(f.output, "test/media/电影 & 测试.strm")
	os.WriteFile(file, []byte("edited by owner"), 0644)
	j := generate(t, f, true, 0)
	if j.CreatedFiles != 0 || j.PreviewSame != 2 || j.PreviewConflicts != 1 || len(j.Preview) != 3 {
		t.Fatalf("diff %+v", j)
	}
	raw, _ := os.ReadFile(file)
	if string(raw) != "edited by owner" {
		t.Fatal("preview overwrote conflict")
	}
}
func TestInventoryHealthChecksAreReadOnlyAndScoped(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	s, _, _ := testToolsServer(t, f, f.c)
	_, _, key, _ := f.e.outputLocation("test/media/电影 & 测试.strm")
	file := filepath.Join(f.output, filepath.FromSlash(key))
	if status, _ := s.inspectOutput(context.Background(), key, true); status != "healthy" {
		t.Fatal(status)
	}
	f.c.fail = "root"
	if status, _ := s.inspectOutput(context.Background(), key, true); status != "source_unknown" {
		t.Fatal("network failure was mistaken for missing source", status)
	}
	f.c.fail = ""
	os.WriteFile(file, []byte("manual modification"), 0644)
	if status, _ := s.inspectOutput(context.Background(), key, false); status != "modified" {
		t.Fatal(status)
	}
	before, _ := os.ReadFile(file)
	s.inspectOutput(context.Background(), key, true)
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("health check modified file")
	}
	if status, _ := s.inspectOutput(context.Background(), "../../outside.strm", true); status != "unknown" {
		t.Fatal("unowned path accepted")
	}
	os.Remove(file)
	if status, _ := s.inspectOutput(context.Background(), key, false); status != "missing" {
		t.Fatal(status)
	}
}
func TestMetricsExcludePreviewSkipAndManagementChecks(t *testing.T) {
	f := setup(t)
	generate(t, f, true, 0)
	generate(t, f, false, 0)
	generate(t, f, false, 0)
	f.s.appendActivity(Activity{Kind: "playback", Event: "play", Outcome: "redirected"})
	f.s.appendActivity(Activity{Kind: "playback", Event: "head", Outcome: "metadata"})
	f.s.appendActivity(Activity{Kind: "direct", Event: "get", Outcome: "resolved", Millis: 20, Cache: true})
	f.s.appendActivity(Activity{Kind: "direct", Event: "check", Outcome: "readable"})
	_, h, cookie := testToolsServer(t, f, f.c)
	req := httptest.NewRequest("GET", "/api/dashboard", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var d struct{ Stats map[string]int64 }
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Stats["today_created"] != 3 || d.Stats["requests"] != 1 || d.Stats["resolve_total"] != 1 || d.Stats["cache"] != 1 {
		t.Fatalf("incorrect metrics %s", w.Body)
	}
	if _, err := f.s.db.Exec("DELETE FROM activity_events"); err != nil {
		t.Fatal(err)
	}
	var n int
	f.s.db.QueryRow("SELECT SUM(n) FROM metric_buckets WHERE kind='generation'").Scan(&n)
	if n != 3 {
		t.Fatal("statistics depended on retained logs")
	}
}
func TestInventoryCanLocateOneMediaFromDashboard(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	_, h, cookie := testToolsServer(t, f, f.c)
	mid := f.s.mediaID(f.a, "movie")
	req := httptest.NewRequest("GET", "/api/outputs?media="+mid, nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var result struct {
		Items []OutputItem
		Total int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || result.Total != 1 || len(result.Items) != 1 || result.Items[0].Media != mid {
		t.Fatal(w.Code, w.Body.String())
	}
}

type restartCloud struct {
	base  *fixtureCloud
	mu    sync.Mutex
	calls map[string]int
	block bool
	// subStarted is closed the first time the "sub" directory is listed, so
	// tests can wait on a channel instead of polling the call counter.
	subStarted  chan struct{}
	subSignaled sync.Once
}

func (c *restartCloud) List(ctx context.Context, a, parent string) ([]Entry, error) {
	c.mu.Lock()
	c.calls[parent]++
	c.mu.Unlock()
	if parent == "sub" && c.subStarted != nil {
		c.subSignaled.Do(func() { close(c.subStarted) })
	}
	if c.block && parent == "sub" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.base.List(ctx, a, parent)
}
func (c *restartCloud) Resolve(ctx context.Context, m Media) (PlaybackTarget, error) {
	return c.base.Resolve(ctx, m)
}
func TestRestartContinuesSavedDirectoryCursor(t *testing.T) {
	f := setup(t)
	f.e.Close()
	slow := &restartCloud{base: f.c, calls: map[string]int{}, block: true, subStarted: make(chan struct{})}
	e1, err := newEngine(context.Background(), f.s, slow, f.output)
	if err != nil {
		t.Fatal(err)
	}
	j, err := e1.enqueue(f.r.ID, "", "", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-slow.subStarted:
	case <-time.After(30 * time.Second):
		t.Fatal("did not reach checkpoint")
	}
	e1.Close()
	s2, err := openStore(f.data)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.db.Close()
	cloud := &restartCloud{base: f.c, calls: map[string]int{}}
	e2, err := newEngine(context.Background(), s2, cloud, f.output)
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	review := *f
	review.s = s2
	review.e = e2
	done := finish(t, &review, j)
	if done.Status != "completed" || done.CreatedFiles != 3 || done.Resumes != 1 {
		t.Fatalf("resume result %+v", done)
	}
	cloud.mu.Lock()
	roots := cloud.calls["root"]
	cloud.mu.Unlock()
	if roots != 0 {
		t.Fatal("resume rescanned completed root")
	}
}
func TestFailureRetryTargetsUnfinishedDirectory(t *testing.T) {
	f := setup(t)
	f.c.fail = "sub"
	old := generate(t, f, false, 0)
	if old.CreatedFiles != 2 {
		t.Fatal(old)
	}
	f.c.fail = ""
	next, err := f.e.retry(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	next = finish(t, f, next)
	if len(next.RetryDirs) != 1 || next.RetryDirs[0].ID != "sub" || next.CreatedFiles != 1 || next.Skipped != 0 {
		t.Fatalf("retry rescanned successful directories %+v", next)
	}
}
func TestLibraryRefreshAndWebhookAreDurableAndSecretsHidden(t *testing.T) {
	f := setup(t)
	var mu sync.Mutex
	paths := []string{}
	var updateBody string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/Library/Media/Updated" {
			updateBody = string(raw)
			if r.Header.Get("X-Emby-Token") != "secret-library-key" {
				t.Error("missing library authorization")
			}
		}
		w.WriteHeader(204)
	}))
	defer target.Close()
	config := IntegrationConfig{Enabled: true, ServerType: "emby", Base: target.URL, APIKey: "secret-library-key", LocalPrefix: filepath.ToSlash(f.output), RemotePrefix: "/media", WebhookEnabled: true, Webhook: target.URL + "/notify?token=secret-notify"}
	if err := config.validate(); err != nil {
		t.Fatal(err)
	}
	if err := f.s.saveIntegration(config); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.s.setting("integration_config", ""), "secret") {
		t.Fatal("secrets stored unencrypted")
	}
	f.r.RefreshLibrary = true
	f.r.Notify = true
	f.s.saveRule(f.r)
	generate(t, f, false, 0)
	f.s.db.Exec("UPDATE deliveries SET next_at=0")
	f.e.processDeliveries()
	mu.Lock()
	if len(paths) != 2 || !strings.Contains(updateBody, "/media/test/media") {
		t.Fatalf("deliveries %v %s", paths, updateBody)
	}
	mu.Unlock()
	var sent int
	f.s.db.QueryRow("SELECT COUNT(*) FROM deliveries WHERE status='sent'").Scan(&sent)
	if sent != 2 {
		t.Fatal("deliveries not persisted")
	}
	_, h, cookie := testToolsServer(t, f, f.c)
	req := httptest.NewRequest("GET", "/api/integrations", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), "secret-library-key") || strings.Contains(w.Body.String(), "secret-notify") {
		t.Fatal("secrets exposed by read API")
	}
}
func TestNotificationDedupAndPreciseRecordFilters(t *testing.T) {
	f := setup(t)
	f.s.notify("失败", "原因", "error", "same")
	f.s.notify("失败", "原因", "error", "same")
	var n int
	f.s.db.QueryRow("SELECT COUNT(*) FROM notifications").Scan(&n)
	if n != 1 {
		t.Fatal("notification was duplicated")
	}
	f.s.appendActivity(Activity{Kind: "generation", Event: "strm", Outcome: "created", Rule: "r1", Job: "j1", Name: "one"})
	f.s.appendActivity(Activity{Kind: "generation", Event: "strm", Outcome: "created", Rule: "r2", Job: "j2", Name: "two"})
	_, h, cookie := testToolsServer(t, f, f.c)
	_, items, total := recordsGET(t, h, cookie, "/api/records?kind=generation&rule=r1&job=j1")
	if total != 1 || items[0].Name != "one" {
		t.Fatal("precise rule/job filter failed")
	}
}
func TestFailedRefreshCanRetryWithoutRegenerating(t *testing.T) {
	f := setup(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer target.Close()
	c := IntegrationConfig{Enabled: true, ServerType: "jellyfin", Base: target.URL, APIKey: "fixture", RefreshAll: true}
	if err := f.s.saveIntegration(c); err != nil {
		t.Fatal(err)
	}
	f.r.RefreshLibrary = true
	mustSucceed(t, f.s.saveRule(f.r))
	j := generate(t, f, false, 0)
	for i := 0; i < 3; i++ {
		if _, err := f.s.db.Exec("UPDATE deliveries SET next_at=0"); err != nil {
			t.Fatal(err)
		}
		f.e.processDeliveries()
	}
	var id, status string
	if err := f.s.db.QueryRow("SELECT id,status FROM deliveries WHERE kind='library'").Scan(&id, &status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatal(status)
	}
	stored, _ := f.s.job(j.ID)
	if stored.Status != "completed" || stored.CreatedFiles != 3 {
		t.Fatal("delivery failure changed generation", stored)
	}
	_, h, cookie := testToolsServer(t, f, f.c)
	req := httptest.NewRequest("POST", "/api/deliveries/"+id+"/retry", strings.NewReader("{}"))
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	if err := f.s.db.QueryRow("SELECT status FROM deliveries WHERE id=?", id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatal("failed refresh was not requeued")
	}
}

func TestConsoleBrowserFixture(t *testing.T) {
	if os.Getenv("YOLINK_UI_REVIEW") != "1" {
		t.Skip("opt-in local browser fixture")
	}
	f := setup(t)
	f.a.Name = "移动媒体盘（界面测试）"
	mustSucceed(t, f.s.saveAccount(f.a))
	f.r.Name = "影视与音乐（界面测试）"
	mustSucceed(t, f.s.saveRule(f.r))
	generated := generate(t, f, false, 0)
	mustSucceed(t, f.s.setSetting("command_targets", scanJSON([]CommandTarget{{Alias: "测试剧集", Rule: f.r.ID, Path: "/media/剧集", Enabled: true}})))
	notifyTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			jsonOut(w, 200, object{"status": "ok", "platform": "webhook"})
		} else {
			jsonOut(w, 200, object{"status": "delivered"})
		}
	}))
	defer notifyTarget.Close()
	mustSucceed(t, f.s.saveIntegration(IntegrationConfig{WebhookEnabled: true, Webhook: notifyTarget.URL + "/webhooks/yolink", WebhookSecret: "fixture-signature"}))
	channels, _ := f.s.channels()
	channels = append(channels, NotificationChannel{ID: "fixture-telegram", Name: "备用 Telegram（界面测试）", Type: "telegram", Enabled: false, Events: []string{"failure"}, Version: 1})
	sealed, err := f.s.encodeChannels(channels)
	mustSucceed(t, err)
	mustSucceed(t, f.s.setSetting("notification_channels", sealed))
	mustExec(t, f.s.db, "INSERT INTO event_clients(id,name,token_hash,enabled,rules,created,last_used) VALUES('fixture-client','整理工具（界面测试）',?,1,?,?,?)", eventTokenHash("fixture-scoped-token"), scanJSON([]string{f.r.ID}), time.Now().Unix(), time.Now().Unix())
	mustExec(t, f.s.db, "INSERT INTO event_calls(client,name,created,action,status) VALUES('fixture-client','整理工具（界面测试）',?,'/api/events',202)", time.Now().Unix())
	f.e.queueDelivery(generated.ID, "webhook", object{"title": "生成完成", "source_path": "/media/剧集"})
	mustExec(t, f.s.db, "UPDATE deliveries SET status='sent',attempts=1 WHERE job=? AND kind='webhook'", generated.ID)
	f.e.queueDelivery(generated.ID, "library", object{"output": f.output})
	mustExec(t, f.s.db, "UPDATE deliveries SET status='failed',attempts=3,error='界面测试：媒体服务器暂时不可达' WHERE job=? AND kind='library'", generated.ID)
	mustExec(t, f.s.db, "INSERT INTO ingest_events(id,rule,source_path,status,job,error,created,revision) VALUES('fixture-event',?,'/media/剧集','submitted',?,'',?,1)", f.r.ID, generated.ID, time.Now().Unix())
	legacyDir := filepath.Join(f.output, "MVStrm")
	migrationFile(t, legacyDir, "电影/别名.strm", oldMigrationURL("yun139-1", "/电影 & 测试.mkv"))
	migrationFile(t, legacyDir, "音乐/音乐.strm", oldMigrationURL("yun139-1", "/音乐.flac"))
	migrationFile(t, legacyDir, "特摄/保留.strm", oldMigrationURL("guangyapan-1", "/保留.mkv"))
	t.Log("isolated migration directory: " + filepath.ToSlash(legacyDir))
	_, h, _ := testToolsServer(t, f, f.c)
	static := http.FileServer(http.Dir("web"))
	var faults struct {
		Dashboard    bool
		Health       bool
		EventClients bool
	}
	var faultsMu sync.Mutex
	reviewHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__review/faults" && r.Method == "POST" {
			faultsMu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&faults)
			faultsMu.Unlock()
			w.WriteHeader(204)
			return
		}
		faultsMu.Lock()
		dashboardFault, healthFault, clientsFault := faults.Dashboard, faults.Health, faults.EventClients
		faultsMu.Unlock()
		if r.URL.Path == "/api/dashboard" && dashboardFault || r.URL.Path == "/api/outputs/check" && healthFault || r.URL.Path == "/api/event-clients" && r.Method == "GET" && clientsFault {
			fail(w, 503, "界面测试：模拟请求失败")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/s/") || r.URL.Path == "/healthz" {
			h.ServeHTTP(w, r)
		} else {
			static.ServeHTTP(w, r)
		}
	})
	runReviewFixture(t, reviewHandler, "isolated console review; password fixture-admin-password")
}
