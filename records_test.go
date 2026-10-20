package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recordsGET(t *testing.T, h http.Handler, cookie *http.Cookie, query string) (*httptest.ResponseRecorder, []Activity, int) {
	t.Helper()
	r := httptest.NewRequest("GET", query, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var data struct {
		Items []Activity `json:"items"`
		Total int        `json:"total"`
	}
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
	}
	return w, data.Items, data.Total
}

func TestActivityPersistsAndCorrelatesGenerationPlaybackAndResolve(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	s, h, cookie := testToolsServer(t, f, f.c)
	mid := f.s.mediaID(f.a, "movie")
	link := "/s/" + mid + "/movie.mkv?k=" + f.s.signature(mid)
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("GET", link, nil)
		r.RemoteAddr = "192.168.8.50:51001"
		r.Header.Set("User-Agent", "PotPlayer/1.7")
		r.Header.Set("X-Forwarded-For", "203.0.113.99")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 302 {
			t.Fatal(w.Code)
		}
	}
	w, play, total := recordsGET(t, h, cookie, "/api/records?kind=playback&q=PotPlayer")
	if w.Code != 200 || total != 2 || len(play) != 2 || play[0].Outcome != "redirected" || play[0].IP != "192.168.8.50" || play[0].Client != "PotPlayer" || play[0].Request == "" {
		t.Fatalf("playback records wrong: %d %d %+v", w.Code, total, play)
	}
	for _, secret := range []string{"fixture-secret", "?k=", "https://cdn.example.com"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("record exposed credential or signed address")
		}
	}
	_, direct, n := recordsGET(t, h, cookie, "/api/records?kind=direct&request="+play[0].Request)
	if n != 1 || direct[0].Media != mid || direct[0].Outcome != "resolved" || !direct[0].Cache || direct[0].Host != "cdn.example.com" {
		t.Fatalf("resolve correlation missing %+v", direct)
	}
	r := httptest.NewRequest("GET", "/api/records/trace?media="+mid, nil)
	r.AddCookie(cookie)
	trace := httptest.NewRecorder()
	h.ServeHTTP(trace, r)
	var chain map[string][]Activity
	json.Unmarshal(trace.Body.Bytes(), &chain)
	if len(chain["generation"]) != 1 || len(chain["playback"]) != 2 || len(chain["direct"]) != 2 {
		t.Fatal("three-stage trace incomplete", trace.Code)
	}
	reopened, err := openStore(f.data)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.db.Close()
	var count int
	reopened.db.QueryRow("SELECT COUNT(*) FROM activity_events WHERE kind='playback'").Scan(&count)
	if count != 2 {
		t.Fatal("records did not survive reopen", count)
	}
	_ = s
}

func TestActivitySearchPaginationAuthenticationAndBounds(t *testing.T) {
	f := setup(t)
	_, h, cookie := testToolsServer(t, f, f.c)
	for i := 0; i < 25; i++ {
		f.s.appendActivity(Activity{Kind: "playback", Event: "play", Name: "movie", Account: "a", Outcome: "redirected", Time: time.Now().UnixMilli() + int64(i)})
	}
	f.s.appendActivity(Activity{Kind: "playback", Event: "play", Name: "100%_真实文件", Account: "b", Outcome: "failed"})
	_, items, total := recordsGET(t, h, cookie, "/api/records?kind=playback&account=a&page=2&limit=20")
	if total != 25 || len(items) != 5 {
		t.Fatal("server pagination broken", total, len(items))
	}
	_, items, total = recordsGET(t, h, cookie, "/api/records?kind=playback&q="+url.QueryEscape("%_"))
	if total != 1 || items[0].Name != "100%_真实文件" {
		t.Fatal("search wildcard was not literal")
	}
	_, _, total = recordsGET(t, h, cookie, "/api/records?kind=playback&q="+url.QueryEscape("' OR 1=1 --"))
	if total != 0 {
		t.Fatal("SQL search injection")
	}
	for _, query := range []string{"kind=bad", "kind=playback&page=0", "kind=playback&limit=101", "kind=direct&days=999", "kind=generation&q=" + url.QueryEscape(strings.Repeat("a", 201))} {
		w, _, _ := recordsGET(t, h, cookie, "/api/records?"+query)
		if w.Code != 400 {
			t.Fatal("invalid filter accepted", query, w.Code)
		}
	}
	w, _, _ := recordsGET(t, h, nil, "/api/records?kind=playback")
	if w.Code != 401 {
		t.Fatal("records must require admin login")
	}
}

func TestActivityLegacyIndexBackfillKeepsUnknownTimeAndFiles(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	file := filepath.Join(f.output, "test/media/电影 & 测试.strm")
	before, _ := os.ReadFile(file)
	f.s.db.Exec("DELETE FROM activity_events")
	f.s.db.Exec("DELETE FROM settings WHERE id='activity_backfill_v1'")
	other, err := openStore(f.data)
	if err != nil {
		t.Fatal(err)
	}
	defer other.db.Close()
	var count int
	other.db.QueryRow("SELECT COUNT(*) FROM activity_events WHERE kind='generation' AND outcome='existing' AND time=0").Scan(&count)
	if count != 3 {
		t.Fatal("legacy time fabricated or incomplete", count)
	}
	third, err := openStore(f.data)
	if err != nil {
		t.Fatal(err)
	}
	defer third.db.Close()
	third.db.QueryRow("SELECT COUNT(*) FROM activity_events").Scan(&count)
	if count != 3 {
		t.Fatal("backfill duplicated")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("legacy backfill changed STRM")
	}
}

func TestActivityGenerationTracksSkipAndConflict(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	os.WriteFile(filepath.Join(f.output, "test/media/电影 & 测试.strm"), []byte("user edited"), 0644)
	generate(t, f, false, 0)
	_, h, cookie := testToolsServer(t, f, f.c)
	_, items, total := recordsGET(t, h, cookie, "/api/records?kind=generation&event=strm&outcome=failed")
	if total != 1 || items[0].Error == "" || items[0].Job == "" || items[0].Output == "" {
		t.Fatal("per-file conflict record missing")
	}
	_, _, total = recordsGET(t, h, cookie, "/api/records?kind=generation&event=strm&outcome=skipped")
	if total != 2 {
		t.Fatal("skipped-file records wrong", total)
	}
}

func TestActivityPreviewTaskDoesNotClaimFilesWritten(t *testing.T) {
	f := setup(t)
	generate(t, f, true, 0)
	_, h, cookie := testToolsServer(t, f, f.c)
	_, items, total := recordsGET(t, h, cookie, "/api/records?kind=generation&event=task&outcome=preview")
	if total != 1 || !strings.Contains(items[0].Note, "未写入文件") || strings.Contains(items[0].Note, "新增") {
		t.Fatalf("preview task claimed files written: %+v", items)
	}
}

func TestActivityRetentionAndLogFailureDoesNotBreak302(t *testing.T) {
	f := setup(t)
	raw, _ := json.Marshal(Activity{Kind: "direct", Name: "retention fixture", Outcome: "resolved"})
	_, err := f.s.db.Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<20095) INSERT INTO activity_events(kind,time,account,media,outcome,request,payload) SELECT 'direct',CASE WHEN n<=5 THEN ? ELSE ? END,'','','resolved','',? FROM seq`, time.Now().AddDate(0, 0, -91).UnixMilli(), time.Now().UnixMilli(), string(raw))
	if err != nil {
		t.Fatal(err)
	}
	f.s.appendActivity(Activity{Kind: "direct", Outcome: "resolved", Name: "new"})
	var count int
	f.s.db.QueryRow("SELECT COUNT(*) FROM activity_events WHERE kind='direct'").Scan(&count)
	if count != 20000 {
		t.Fatal("row retention not bounded", count)
	}
	f.s.db.QueryRow("SELECT COUNT(*) FROM activity_events WHERE time>0 AND time<?", time.Now().AddDate(0, 0, -90).UnixMilli()).Scan(&count)
	if count != 0 {
		t.Fatal("age retention not applied")
	}
	generate(t, f, false, 0)
	s, h, _ := testToolsServer(t, f, f.c)
	f.s.db.Exec("DROP TABLE activity_events")
	mid := f.s.mediaID(f.a, "movie")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/s/"+mid+"/movie.mkv?k="+f.s.signature(mid), nil))
	if w.Code != 302 {
		t.Fatal("failed log write broke playback", w.Code)
	}
	s.s.activityMu.Lock()
	failed := s.s.activityError != ""
	s.s.activityMu.Unlock()
	if !failed {
		t.Fatal("log persistence failure invisible")
	}
}

func TestServeRecordsReview(t *testing.T) {
	if os.Getenv("YOLINK_RECORDS_UI_REVIEW") != "1" {
		t.Skip("local records UI fixture")
	}
	f := setup(t)
	f.a.Name = "移动媒体盘（界面测试）"
	f.s.saveAccount(f.a)
	f.r.Name = "影视目录（界面测试）"
	f.s.saveRule(f.r)
	generate(t, f, false, 0)
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "YoLink-Records-QA")
	s, err := newServer(f.s, f.e, f.c, f.data, f.output)
	if err != nil {
		t.Fatal(err)
	}
	h := s.handler()
	mid := f.s.mediaID(f.a, "movie")
	for i := 0; i < 26; i++ {
		r := httptest.NewRequest("GET", "/s/"+mid+"/movie.mkv?k="+f.s.signature(mid), nil)
		r.RemoteAddr = "192.0.2.50:5010"
		r.Header.Set("User-Agent", "PotPlayer/1.7 (界面测试)")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	f.s.appendActivity(Activity{Kind: "direct", Outcome: "failed", Event: "get", Media: mid, Name: "电影 & 测试.mkv", Account: f.a.ID, AccountName: f.a.Name, Provider: f.a.Provider, Path: "/media/电影 & 测试.mkv", IP: "192.0.2.51", Client: "Lavf", Error: "测试记录：临时网盘地址不可用，请刷新后重试", Request: "fixture-failed-request"})
	f.s.appendActivity(Activity{Kind: "playback", Outcome: "failed", Event: "play", HTTP: 502, Media: mid, Name: "电影 & 测试.mkv", Account: f.a.ID, AccountName: f.a.Name, Provider: f.a.Provider, IP: "192.0.2.51", Client: "Lavf", Error: "界面测试：模拟解析失败", Request: "fixture-failed-request"})
	f.s.appendActivity(Activity{Kind: "direct", Outcome: "resolved", Event: "check", Media: mid, Name: "电影 & 测试.mkv", Account: f.a.ID, AccountName: f.a.Name, Request: "fixture-check", Host: "cdn.example.com", Millis: 10})
	f.s.appendActivity(Activity{Kind: "direct", Outcome: "readable", Event: "check", Media: mid, Name: "电影 & 测试.mkv", Account: f.a.ID, AccountName: f.a.Name, Request: "fixture-check", Host: "cdn.example.com", Bytes: 1024, Millis: 30})
	f.s.appendActivity(Activity{Kind: "direct", Outcome: "resolved", Event: "resolve", Media: mid, Name: "电影 & 测试.mkv", Account: f.a.ID, AccountName: f.a.Name, Request: "fixture-resolve", Host: "cdn.example.com", Millis: 12})
	f.s.appendActivity(Activity{Kind: "generation", Outcome: "failed", Event: "strm", Media: mid, Name: "电影 & 测试.mkv", Account: f.a.ID, AccountName: f.a.Name, Provider: f.a.Provider, Path: "/media/电影 & 测试.mkv", Output: "/CloudNAS/测试目录/电影 & 测试.strm", Rule: f.r.ID, RuleName: f.r.Name, Job: "fixture-conflict", Error: "测试记录：输出内容不同，保留已有文件"})
	server := &http.Server{Addr: "127.0.0.1:18796", Handler: h, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { server.Close() })
	t.Log("Records UI fixture http://127.0.0.1:18796; fake accounts only")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
