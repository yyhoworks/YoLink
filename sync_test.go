package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixtureCloud struct {
	dirs   map[string][]Entry
	fail   string
	target PlaybackTarget
}

func (f *fixtureCloud) List(ctx context.Context, a, parent string) ([]Entry, error) {
	if parent == f.fail {
		return nil, errors.New("模拟分页失败")
	}
	// Real providers return either entries or an error, never both; in
	// particular a cancelled context yields no entries.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.dirs[parent], nil
}
func (f *fixtureCloud) Resolve(ctx context.Context, m Media) (PlaybackTarget, error) {
	return f.target, nil
}

type fixture struct {
	s            *Store
	e            *Engine
	c            *fixtureCloud
	a            Account
	r            Rule
	output, data string
	cancel       context.CancelFunc
}

func setup(t *testing.T) *fixture {
	t.Helper()
	data := t.TempDir()
	s, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	c := &fixtureCloud{dirs: map[string][]Entry{"root": {{ID: "movie", Name: "电影 & 测试.mkv"}, {ID: "audio", Name: "音乐.flac"}, {ID: "nfo", Name: "movie.nfo"}, {ID: "sub", Name: "剧集", Dir: true}}, "sub": {{ID: "ep1", Name: "S01E01.mp4"}}}, target: PlaybackTarget{URL: "https://cdn.example.com/media.mkv", Mode: "302"}}
	en, err := newEngine(ctx, s, c, output)
	if err != nil {
		t.Fatal(err)
	}
	a := Account{ID: "account", Name: "移动", Provider: "139", Root: "root", Secrets: map[string]string{"authorization": "fixture-secret"}}
	if err = s.saveAccount(a); err != nil {
		t.Fatal(err)
	}
	r := Rule{ID: "rule", Name: "测试", Account: a.ID, Folder: "root", SourcePath: "/media", Output: "test/media", Interval: 1800}
	if err = s.saveRule(r); err != nil {
		t.Fatal(err)
	}
	if err = s.setSetting("public_base", "http://nas.example:8796"); err != nil {
		t.Fatal(err)
	}
	f := &fixture{s, en, c, a, r, output, data, cancel}
	t.Cleanup(func() { cancel(); en.Close(); s.db.Close() })
	return f
}
func finish(t *testing.T, f *fixture, j Job) Job {
	return finishWithin(t, f, j, 5*time.Second)
}
func finishWithin(t *testing.T, f *fixture, j Job, budget time.Duration) Job {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		v, e := f.s.job(j.ID)
		if e != nil {
			t.Fatal(e)
		}
		if v.Status != "running" && v.Status != "queued" {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job timed out")
	return Job{}
}
func generate(t *testing.T, f *fixture, dry bool, limit int) Job {
	t.Helper()
	j, e := f.e.enqueue(f.r.ID, "", "", dry, limit)
	if e != nil {
		t.Fatal(e)
	}
	return finish(t, f, j)
}
func TestGenerateIsIdempotentAndIncludesAudio(t *testing.T) {
	f := setup(t)
	j := generate(t, f, false, 0)
	if j.Status != "completed" || j.CreatedFiles != 3 {
		t.Fatalf("first: %+v", j)
	}
	p := filepath.Join(f.output, "test/media/电影 & 测试.strm")
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(strings.TrimSpace(string(b)))
	if e != nil || u.Host != "nas.example:8796" || u.Query().Get("k") == "" {
		t.Fatalf("unstable playback entry: %s", b)
	}
	if _, e = os.Stat(filepath.Join(f.output, "test/media/音乐.strm")); e != nil {
		t.Fatal(e)
	}
	second := generate(t, f, false, 0)
	if second.CreatedFiles != 0 || second.Skipped != 3 || second.Errors != 0 {
		t.Fatalf("repeat: %+v", second)
	}
}
func TestPreviewDoesNotWriteFilesOrMedia(t *testing.T) {
	f := setup(t)
	j := generate(t, f, true, 0)
	if j.Seen != 3 || j.CreatedFiles != 3 {
		t.Fatalf("preview: %+v", j)
	}
	entries, _ := os.ReadDir(f.output)
	if len(entries) != 0 {
		t.Fatal("preview wrote files")
	}
	var count int
	f.s.db.QueryRow("SELECT COUNT(*) FROM media").Scan(&count)
	if count != 0 {
		t.Fatal("preview wrote playback mappings")
	}
}
func TestGenerationLimit(t *testing.T) {
	f := setup(t)
	j := generate(t, f, false, 1)
	if j.CreatedFiles != 1 || j.Seen != 1 {
		t.Fatalf("limit: %+v", j)
	}
}
func TestExistingDifferentFileIsPreserved(t *testing.T) {
	f := setup(t)
	p := filepath.Join(f.output, "test/media/电影 & 测试.strm")
	os.MkdirAll(filepath.Dir(p), 0755)
	original := []byte("http://old-muvyo:7811/redirect?path=x\n")
	os.WriteFile(p, original, 0644)
	j := generate(t, f, false, 0)
	if j.Errors != 1 || j.Status != "partial" {
		t.Fatalf("collision: %+v", j)
	}
	b, _ := os.ReadFile(p)
	if string(b) != string(original) {
		t.Fatal("original overwritten")
	}
}
func TestFailedScanKeepsExistingFiles(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	f.c.fail = "sub"
	j := generate(t, f, false, 0)
	if j.Status != "failed" {
		t.Fatalf("expected scan failure: %+v", j)
	}
	if _, e := os.Stat(filepath.Join(f.output, "test/media/剧集/S01E01.strm")); e != nil {
		t.Fatal("existing file was removed")
	}
}
func TestDirectoryScopeUsesRuleRelativeOutput(t *testing.T) {
	f := setup(t)
	j, e := f.e.enqueue(f.r.ID, "sub", "/media/剧集", false, 0)
	if e != nil {
		t.Fatal(e)
	}
	v := finish(t, f, j)
	if v.CreatedFiles != 1 {
		t.Fatalf("scope: %+v", v)
	}
	if _, e = os.Stat(filepath.Join(f.output, "test/media/剧集/S01E01.strm")); e != nil {
		t.Fatal(e)
	}
	bad, e := f.e.enqueue(f.r.ID, "sub", "/media/fake", false, 0)
	if e != nil {
		t.Fatal(e)
	}
	if finish(t, f, bad).Status != "failed" {
		t.Fatal("unverified folder identity accepted")
	}
}
func TestPathTraversalRejected(t *testing.T) {
	f := setup(t)
	for _, p := range []string{"../outside", "/outside", "C:/outside", "test/../outside", "test//outside"} {
		if _, e := safeRelative(p); e == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	if _, e := f.e.enqueue(f.r.ID, "sub", "/other/dir", false, 0); e == nil {
		t.Fatal("accepted external scope")
	}
	f.c.dirs["root"] = []Entry{{ID: "bad", Name: "../outside.mkv"}}
	j := generate(t, f, false, 0)
	if j.Errors != 1 {
		t.Fatalf("bad name: %+v", j)
	}
}
func TestNormalizedNameCollisionIsReported(t *testing.T) {
	f := setup(t)
	f.c.dirs["root"] = []Entry{{ID: "one", Name: "Movie.mkv"}, {ID: "two", Name: "Movie.mp4"}}
	j := generate(t, f, false, 0)
	if j.CreatedFiles != 1 || j.Errors != 1 {
		t.Fatalf("collision: %+v", j)
	}
}
func TestCredentialsEncryptedAndRedacted(t *testing.T) {
	f := setup(t)
	var secret []byte
	var payload string
	if e := f.s.db.QueryRow("SELECT secret,payload FROM accounts").Scan(&secret, &payload); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(secret), "fixture-secret") || strings.Contains(payload, "fixture-secret") {
		t.Fatal("credential leaked")
	}
	a, e := f.s.account(f.a.ID)
	if e != nil || a.Secrets["authorization"] != "fixture-secret" {
		t.Fatal("credential decrypt failed")
	}
	as, e := f.s.accounts()
	raw, _ := json.Marshal(as)
	if e != nil || strings.Contains(string(raw), "fixture-secret") {
		t.Fatal("account API returned secrets")
	}
}
func TestPlaybackSignature302AndHead(t *testing.T) {
	f := setup(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Range") != "bytes=0-0" {
			t.Error("player metadata must use a bounded GET, not cloud HEAD")
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 0-0/9000")
		w.WriteHeader(206)
		io.WriteString(w, "x")
	}))
	defer upstream.Close()
	f.c.target.URL = upstream.URL
	m := Media{ID: "media123", Account: f.a.ID, RemoteID: "movie", Name: "movie.mkv"}
	f.s.saveMedia(m)
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "fixture-admin-password")
	s, e := newServer(f.s, f.e, f.c, f.data, f.output)
	if e != nil {
		t.Fatal(e)
	}
	h := s.handler()
	s.probeClient = upstream.Client()
	for _, method := range []string{"GET", "HEAD"} {
		r := httptest.NewRequest(method, "/s/media123/movie.mkv?k="+f.s.signature(m.ID), nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if method == "GET" && (w.Code != 302 || w.Header().Get("Location") != f.c.target.URL) || method == "HEAD" && (w.Code != 200 || w.Header().Get("Content-Length") != "9000" || w.Body.Len() != 0 || w.Header().Get("Location") != "") {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/s/media123/movie.mkv?k=wrong", nil))
	if w.Code != 403 {
		t.Fatal("unsigned playback accepted")
	}
	f.c.target.Mode = "proxy-required"
	s.links.clear() // The provider response changed; force a fresh resolution.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/s/media123/movie.mkv?k="+f.s.signature(m.ID), nil))
	if w.Code != 409 {
		t.Fatal("cookie target disguised as 302")
	}
}
func TestAdminAuthAndCSRF(t *testing.T) {
	f := setup(t)
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "fixture-admin-password")
	s, _ := newServer(f.s, f.e, f.c, f.data, f.output)
	h := s.handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/state", nil))
	if w.Code != 401 {
		t.Fatal("anonymous state access")
	}
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"fixture-admin-password"}`))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	r = httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"public_base":"http://nas:8796"}`))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross origin write accepted")
	}
	r = httptest.NewRequest("GET", "/api/state", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "fixture-secret") {
		t.Fatal("state leaked credentials")
	}
}
func TestRestartMarksJobsInterrupted(t *testing.T) {
	dir := t.TempDir()
	s, e := openStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	j := Job{ID: "unfinished", Status: "running", Created: time.Now().Unix()}
	s.saveJob(j)
	s.db.Close()
	s, e = openStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	j, e = s.job(j.ID)
	if e != nil || j.Status != "interrupted" {
		t.Fatalf("restart: %+v %v", j, e)
	}
}
func TestRootConfinesSymlinks(t *testing.T) {
	f := setup(t)
	outside := t.TempDir()
	link := filepath.Join(f.output, "escape")
	if e := os.Symlink(outside, link); e != nil {
		t.Skip("symlink creation unavailable on this host")
	}
	if _, e := f.e.publish("escape/bad.strm", "m", []byte("data")); e == nil {
		t.Fatal("output escaped root")
	}
	if _, e := os.Stat(filepath.Join(outside, "bad.strm")); !os.IsNotExist(e) {
		t.Fatal("external output written")
	}
}
func TestHTTPRangeProxyBlocksPrivateDestination(t *testing.T) {
	f := setup(t)
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "fixture-admin-password")
	s, _ := newServer(f.s, f.e, f.c, f.data, f.output)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("private upstream must not be reached") }))
	defer up.Close()
	w := httptest.NewRecorder()
	s.proxy(w, httptest.NewRequest("GET", "/", nil), PlaybackTarget{URL: up.URL, Headers: http.Header{}})
	if w.Code != 502 {
		t.Fatal("private target accepted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAliPaginationUsesFreshMarker(t *testing.T) {
	f := setup(t)
	a := Account{ID: "ali", Name: "Ali", Provider: "ali", Drive: "drive", Root: "root", Secrets: map[string]string{"access_token": "token"}}
	f.s.saveAccount(a)
	p := newProviders(f.s)
	calls := 0
	p.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		var body object
		json.Unmarshal(b, &body)
		calls++
		result := `{"items":[{"file_id":"1","name":"one.mkv","type":"file"}],"next_marker":"page2"}`
		if calls == 2 {
			if body["marker"] != "page2" {
				t.Error("cursor was not forwarded")
			}
			result = `{"items":[{"file_id":"2","name":"two.flac","type":"file"}],"next_marker":""}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(result)), Header: http.Header{}}, nil
	})
	files, e := p.List(context.Background(), a.ID, "root")
	if e != nil || len(files) != 2 || calls != 2 {
		t.Fatalf("pagination: %v %v %d", files, e, calls)
	}
}
