package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countingCloud struct {
	*fixtureCloud
	calls atomic.Int32
	gate  <-chan struct{}
}

func (c *countingCloud) Resolve(ctx context.Context, m Media) (PlaybackTarget, error) {
	c.calls.Add(1)
	if c.gate != nil {
		select {
		case <-c.gate:
		case <-ctx.Done():
			return PlaybackTarget{}, ctx.Err()
		}
	}
	return c.fixtureCloud.Resolve(ctx, m)
}
func testToolsServer(t *testing.T, f *fixture, c Clouds) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	return testServerWithStore(t, f.s, f, c)
}
func TestPlaybackCacheSharesConcurrentRequestsAndInvalidates(t *testing.T) {
	f := setup(t)
	gate := make(chan struct{})
	cloud := &countingCloud{fixtureCloud: f.c, gate: gate}
	s, _, _ := testToolsServer(t, f, cloud)
	m := Media{ID: "cache-media", Account: f.a.ID, RemoteID: "movie", Name: "movie.mkv"}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			target, _, e := s.resolveMedia(context.Background(), m, false)
			if e != nil || target.URL != f.c.target.URL {
				t.Errorf("resolve: %v", e)
			}
		}()
	}
	deadline := time.Now().Add(time.Second)
	for cloud.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(gate)
	wg.Wait()
	if cloud.calls.Load() != 1 {
		t.Fatal("duplicate concurrent resolve", cloud.calls.Load())
	}
	_, hit, e := s.resolveMedia(context.Background(), m, false)
	if e != nil || !hit {
		t.Fatal("cache not reused")
	}
	a, _ := f.s.account(f.a.ID)
	a.Secrets["authorization"] = "changed-test-credential"
	mustSucceed(t, f.s.saveAccount(a))
	target, hit, e := s.resolveMedia(context.Background(), m, false)
	if e != nil || hit || target.URL != f.c.target.URL {
		t.Fatalf("credential change resolve did not return a fresh target: %+v hit=%v err=%v", target, hit, e)
	}
	if cloud.calls.Load() != 2 {
		t.Fatal("credential change reused old target")
	}
	s.links.clear()
	target, hit, e = s.resolveMedia(context.Background(), m, false)
	if e != nil || hit || target.URL != f.c.target.URL {
		t.Fatalf("post-clear resolve did not return a fresh target: %+v hit=%v err=%v", target, hit, e)
	}
	if cloud.calls.Load() != 3 {
		t.Fatal("clear reused old target")
	}
}
func TestPlaybackCacheExpiredTargetsAndCancelledWaiter(t *testing.T) {
	f := setup(t)
	f.c.target.URL = "https://cdn.example.com/video?expires=1"
	cloud := &countingCloud{fixtureCloud: f.c}
	s, _, _ := testToolsServer(t, f, cloud)
	m := Media{ID: "expires", Account: f.a.ID, RemoteID: "movie"}
	s.resolveMedia(context.Background(), m, false)
	s.resolveMedia(context.Background(), m, false)
	if cloud.calls.Load() != 2 {
		t.Fatal("expired link cached")
	}
	gate := make(chan struct{})
	cloud.gate = gate
	f.c.target.URL = "https://cdn.example.com/video"
	s.links.clear()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, e := s.resolveMedia(ctx, m, false)
	if e == nil {
		t.Fatal("cancelled caller succeeded")
	}
	close(gate)
	if _, _, e = s.resolveMedia(context.Background(), m, false); e != nil {
		t.Fatal("one cancelled caller poisoned shared resolve", e)
	}
}
func TestPlaybackProbeChecksDataAndRangeWithoutReadingWholeFile(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		status                   int
		contentType, rangeHeader string
		readable, rangeOK        bool
	}{{"range", 206, "video/mp4", "bytes 0-1023/50000", true, true}, {"range-ignored", 200, "video/mp4", "", true, false}, {"login-html", 200, "text/html", "", false, false}, {"bad-range", 206, "video/mp4", "bytes 0-malformed", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			s, _, _ := testToolsServer(t, f, f.c)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				if r.Method == "HEAD" {
					w.WriteHeader(405)
					return
				}
				if r.Header.Get("Range") != "bytes=0-1023" {
					t.Error("missing bounded range")
				}
				w.Header().Set("Content-Range", tc.rangeHeader)
				w.WriteHeader(tc.status)
				io.WriteString(w, strings.Repeat("x", 5000))
			}))
			defer upstream.Close()
			s.probeClient = upstream.Client()
			result := s.probeTarget(context.Background(), PlaybackTarget{URL: upstream.URL, Mode: "302"})
			if result.Readable != tc.readable || result.Range != tc.rangeOK || result.Bytes > 1024 || result.HeadStatus != 405 {
				t.Fatalf("wrong probe: %+v", result)
			}
		})
	}
	client := safeMediaClient()
	defer client.CloseIdleConnections()
	// The private-address guard must be proven against a live loopback
	// listener: a dead port would fail with connection refused even if the
	// guard in safeMediaClient were deleted, so it cannot distinguish
	// "refused by policy" from "nothing listening".
	var reached atomic.Bool
	loopback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		w.Header().Set("Content-Type", "video/mp4")
		io.WriteString(w, "media")
	}))
	defer loopback.Close()
	r, e := client.Get(loopback.URL + "/media")
	if e == nil {
		r.Body.Close()
		t.Fatal("probe allowed private target")
	}
	if reached.Load() {
		t.Fatal("private target handler was reached despite the guard")
	}
	if !strings.Contains(e.Error(), "内网") {
		t.Fatalf("request failed for a reason other than the private-address guard: %v", e)
	}
}
func TestIngestWaitsForBusyRuleAndScansOnlyFileParent(t *testing.T) {
	f := setup(t)
	s, h, cookie := testToolsServer(t, f, f.c)
	_ = cookie
	f.s.saveJob(Job{ID: "busy-ingest", Rule: f.r.ID, Status: "queued", Created: time.Now().Unix()})
	submit := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/events", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+s.eventToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for i := 0; i < 2; i++ {
		w := submit(`{"rule":"rule","path":"/media/剧集/new.mkv","kind":"file"}`)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	events, _ := f.s.ingestEvents()
	if len(events) != 1 || events[0].Revision != 2 {
		t.Fatal("duplicate directory events not merged", events)
	}
	f.e.processIngest()
	events, _ = f.s.ingestEvents()
	if events[0].Status != "pending" {
		t.Fatal("busy event lost")
	}
	f.s.saveJob(Job{ID: "busy-ingest", Rule: f.r.ID, Status: "cancelled", Created: time.Now().Unix()})
	f.e.processIngest()
	events, _ = f.s.ingestEvents()
	if events[0].Job == "" {
		t.Fatal("event not dispatched", events)
	}
	j, _ := f.s.job(events[0].Job)
	j = finish(t, f, j)
	if j.SourcePath != "/media/剧集" || j.CreatedFiles != 1 {
		t.Fatal("event scanned whole rule", j)
	}
	if _, e := os.Stat(filepath.Join(f.output, "test/media/电影 & 测试.strm")); !os.IsNotExist(e) {
		t.Fatal("parent event generated unrelated root file")
	}
	if w := submit(`{"rule":"rule","path":"/outside/new.mkv","kind":"file"}`); w.Code != 400 {
		t.Fatal("out of scope event accepted")
	}
	if w := submit(`{"rule":"rule","path":"/media/new.mkv","kind":"file","dry_run":true}`); w.Code != 400 {
		t.Fatal("event silently ignored dry run")
	}
}
func TestOwnedLinkUpdateBacksUpAndRollbackPreservesExternalChanges(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	_, h, cookie := testToolsServer(t, f, f.c)
	first := filepath.Join(f.output, "test/media/电影 & 测试.strm")
	old, _ := os.ReadFile(first)
	f.s.setSetting("public_base", "http://new.example:8796")
	w := generationPOST(h, cookie, "/api/maintenance/links", map[string]any{})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var plan struct {
		ID      string `json:"id"`
		Ready   int    `json:"ready"`
		Applied int    `json:"applied"`
	}
	json.Unmarshal(w.Body.Bytes(), &plan)
	if plan.Ready != 3 {
		t.Fatal(w.Body.String())
	}
	current, _ := os.ReadFile(first)
	if string(current) != string(old) {
		t.Fatal("preview wrote file")
	}
	other := filepath.Join(f.output, "test/media/音乐.strm")
	os.WriteFile(other, []byte("manually changed"), 0644)
	w = generationPOST(h, cookie, "/api/maintenance/"+plan.ID+"/apply", map[string]any{})
	json.Unmarshal(w.Body.Bytes(), &plan)
	if w.Code != 200 || plan.Applied != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	updated, _ := os.ReadFile(first)
	if !strings.HasPrefix(string(updated), "http://new.example:8796/") {
		t.Fatal("link not updated")
	}
	backupCount := 0
	e := filepath.WalkDir(filepath.Join(f.data, "strm-backups", plan.ID), func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() && strings.HasSuffix(p, ".strm") {
			backupCount++
		}
		return nil
	})
	if e != nil || backupCount != 2 {
		t.Fatal("backup missing", backupCount, e)
	}
	w = generationPOST(h, cookie, "/api/maintenance/"+plan.ID+"/rollback", map[string]any{})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	restored, _ := os.ReadFile(first)
	if string(restored) != string(old) {
		t.Fatal("rollback failed")
	}
	external, _ := os.ReadFile(other)
	if string(external) != "manually changed" {
		t.Fatal("external change overwritten")
	}
}
func TestMigrationCreatesNewCopiesAndResolvesPathOnFirstPlayback(t *testing.T) {
	f := setup(t)
	f.c.dirs["global-root"] = []Entry{{ID: "root", Name: "media", Dir: true}}
	f.a.Root = "global-root"
	f.s.saveAccount(f.a)
	s, h, cookie := testToolsServer(t, f, f.c)
	original := []byte("http://old-muvyo:7811/redirect?path=%2Fmedia%2F%E5%89%A7%E9%9B%86%2FS01E01.mp4&storage=yun139-1\n")
	source := filepath.Join(t.TempDir(), "old.strm")
	os.WriteFile(source, original, 0644)
	payload := map[string]any{"mapping": map[string]string{"yun139-1": f.a.ID}, "files": []map[string]string{{"path": "MVStrm/剧集/S01E01.strm", "content": string(original)}}}
	w := generationPOST(h, cookie, "/api/maintenance/migration", payload)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var summary struct {
		ID    string `json:"id"`
		Ready int    `json:"ready"`
	}
	json.Unmarshal(w.Body.Bytes(), &summary)
	if summary.Ready != 1 {
		t.Fatal(w.Body.String())
	}
	p, _ := s.loadPlan(summary.ID)
	var count int
	f.s.db.QueryRow("SELECT COUNT(*) FROM media").Scan(&count)
	if count != 0 {
		t.Fatal("preview indexed migration")
	}
	w = generationPOST(h, cookie, "/api/maintenance/"+p.ID+"/apply", map[string]any{})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	copyBytes, e := os.ReadFile(filepath.Join(f.output, filepath.FromSlash(p.Items[0].Path)))
	if e != nil || !strings.Contains(string(copyBytes), "/s/") {
		t.Fatal("new migration file missing", e)
	}
	old, _ := os.ReadFile(source)
	if string(old) != string(original) {
		t.Fatal("original migration source changed")
	}
	m, _ := f.s.media(p.Items[0].Media.ID)
	if _, _, e = s.resolveMedia(context.Background(), m, false); e != nil {
		t.Fatal(e)
	}
	m, _ = f.s.media(m.ID)
	if m.RemoteID != "ep1" {
		t.Fatal("legacy file identity not resolved", m.RemoteID)
	}
	payload["files"] = []map[string]string{{"path": "../outside.strm", "content": string(original)}}
	w = generationPOST(h, cookie, "/api/maintenance/migration", payload)
	if w.Code != 400 {
		t.Fatal("migration path traversal accepted")
	}
}
func TestSubtitleCopyMatchesMediaAndKeepsSTRMCountAccurate(t *testing.T) {
	f := setup(t)
	f.r.CopySubtitles = true
	f.s.saveRule(f.r)
	f.c.dirs["root"] = append(f.c.dirs["root"], Entry{ID: "sub-cn", Name: "电影 & 测试.zh-CN.srt"}, Entry{ID: "other-sub", Name: "别的电影.srt"})
	f.e.subtitleDownload = func(context.Context, PlaybackTarget) ([]byte, error) {
		return []byte("1\n00:00:00,000 --> 00:00:01,000\n测试字幕\n"), nil
	}
	j := generate(t, f, false, 0)
	if j.Sidecars != 1 || j.CreatedFiles != 3 {
		t.Fatalf("subtitle counts wrong: %+v", j)
	}
	if _, e := os.Stat(filepath.Join(f.output, "test/media/电影 & 测试.zh-CN.srt")); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(f.output, "test/media/别的电影.srt")); !os.IsNotExist(e) {
		t.Fatal("unrelated subtitle copied")
	}
	overview, _ := f.s.generationOverview()
	if overview.Outputs != 3 {
		t.Fatal("subtitle inflated STRM count", overview)
	}
}

func TestServeGlassToolsReview(t *testing.T) {
	if os.Getenv("STRMLINK_GLASS_UI_REVIEW") != "1" {
		t.Skip("local review fixture")
	}
	f := setup(t)
	f.a.Name = "移动媒体盘（测试账号）"
	f.s.saveAccount(f.a)
	f.r.Name = "电影与剧集（测试目录）"
	f.s.saveRule(f.r)
	generate(t, f, false, 0)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		if r.Method == "HEAD" {
			w.Header().Set("Content-Length", "12000000000")
			return
		}
		w.Header().Set("Content-Range", "bytes 0-1023/12000000000")
		w.WriteHeader(206)
		io.WriteString(w, strings.Repeat("x", 1024))
	}))
	defer upstream.Close()
	f.c.target = PlaybackTarget{URL: upstream.URL + "/test-only.mp4", Mode: "302"}
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "StrmLink-Glass-QA")
	s, e := newServer(f.s, f.e, reviewClouds{f.c}, f.data, f.output)
	if e != nil {
		t.Fatal(e)
	}
	s.probeClient = upstream.Client()
	runReviewFixture(t, s.handler(), "glass tools; synthetic media and accounts only")
}
