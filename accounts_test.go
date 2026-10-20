package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAccountLifecycleProtectsRulesCredentialsAndPlayback(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	h, cookie := generationHandler(t, f)
	var before []byte
	f.s.db.QueryRow("SELECT secret FROM accounts WHERE id=?", f.a.ID).Scan(&before)
	w := generationPOST(h, cookie, "/api/accounts/"+f.a.ID+"/enabled", map[string]bool{"enabled": false})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a, e := f.s.account(f.a.ID)
	if e != nil || !a.Disabled || a.Secrets["authorization"] != "fixture-secret" {
		t.Fatal("disable did not preserve credentials")
	}
	var after []byte
	f.s.db.QueryRow("SELECT secret FROM accounts WHERE id=?", f.a.ID).Scan(&after)
	if string(before) != string(after) {
		t.Fatal("toggle rewrote credentials")
	}
	if _, e = f.e.enqueue(f.r.ID, "", "", false, 0); e == nil {
		t.Fatal("disabled account generated")
	}
	req := httptest.NewRequest("GET", "/api/browse?account="+a.ID, nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 409 {
		t.Fatal("disabled account browsed", w.Code)
	}
	rows, _ := f.s.db.Query("SELECT id FROM media LIMIT 1")
	var mid string
	if rows.Next() {
		rows.Scan(&mid)
	}
	rows.Close()
	req = httptest.NewRequest("GET", "/s/"+mid+"/movie.mkv?k="+f.s.signature(mid), nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 409 {
		t.Fatal("disabled playback resolved", w.Code)
	}
	w = generationPOST(h, cookie, "/api/accounts/"+a.ID+"/remove", map[string]any{})
	if w.Code != 409 {
		t.Fatal("referenced account removed", w.Code)
	}
	w = generationPOST(h, cookie, "/api/accounts/"+a.ID+"/enabled", map[string]bool{"enabled": true})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a, _ = f.s.account(a.ID)
	if a.Disabled {
		t.Fatal("reenable failed")
	}
}
func TestAccountChangeBlockedDuringGenerationAndUnusedRemoval(t *testing.T) {
	f := setup(t)
	h, cookie := generationHandler(t, f)
	f.s.saveJob(Job{ID: "active-account", Rule: f.r.ID, Status: "queued", Created: time.Now().Unix()})
	w := generationPOST(h, cookie, "/api/accounts/"+f.a.ID+"/enabled", map[string]bool{"enabled": false})
	if w.Code != 409 {
		t.Fatal("disabled active generation", w.Code)
	}
	unused := Account{ID: "unused", Name: "Unused", Provider: "ali", Root: "root", Drive: "d", Secrets: map[string]string{"access_token": "test-only"}}
	if e := f.s.saveAccount(unused); e != nil {
		t.Fatal(e)
	}
	f.s.updateAccountReport(unused.ID, func(v *AccountReport) { v.Checked = 1; v.Reachable = true })
	w = generationPOST(h, cookie, "/api/accounts/unused/remove", map[string]any{})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, e := f.s.account("unused"); e == nil {
		t.Fatal("unused account remains")
	}
	reports, _ := f.s.accountReports()
	if _, ok := reports["unused"]; ok {
		t.Fatal("removed account report remains")
	}
}
func TestConnectionReportAndBrowseDoNotGenerate(t *testing.T) {
	f := setup(t)
	h, cookie := generationHandler(t, f)
	for _, want := range []bool{true, false} {
		if !want {
			f.c.fail = f.a.Root
		}
		w := generationPOST(h, cookie, "/api/accounts/"+f.a.ID+"/check", map[string]any{})
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var report AccountReport
		if e := json.Unmarshal(w.Body.Bytes(), &report); e != nil || report.Reachable != want || report.Checked == 0 {
			t.Fatal("incorrect connection status", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "fixture-secret") {
			t.Fatal("credentials in status")
		}
	}
	var indexed int
	f.s.db.QueryRow("SELECT COUNT(*) FROM media").Scan(&indexed)
	if indexed != 0 {
		t.Fatal("connection test indexed media")
	}
	w := generationPOST(h, cookie, "/api/accounts/"+f.a.ID+"/quota", map[string]any{})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "quota_error") {
		t.Fatal("unsupported quota presented as usage", w.Body.String())
	}
}

type cloudRoundTrip func(*http.Request) (*http.Response, error)

func (f cloudRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestQuotaParsesProviderUnitsAndRejectsMissingUsage(t *testing.T) {
	cases := []struct {
		provider, path, body string
		total, used          int64
		valid                bool
	}{
		{"139", "/user/disk/quota/detail", `{"success":true,"data":{"diskSize":200,"freeDiskSize":50}}`, 200 * 1024 * 1024, 150 * 1024 * 1024, true},
		{"quark", "/1/clouddrive/member", `{"status":200,"data":{"total_capacity":2000,"use_capacity":500}}`, 2000, 500, true},
		{"ali", "/adrive/v1.0/user/getSpaceInfo", `{"personal_space_info":{"total_size":10000,"used_size":3000}}`, 10000, 3000, true},
		{"139", "/user/disk/quota/detail", `{"success":true,"data":{"diskSize":200}}`, 0, 0, false},
		{"quark", "/1/clouddrive/member", `{"status":200,"data":{"total_capacity":2000}}`, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.provider+tc.body, func(t *testing.T) {
			f := setup(t)
			a := f.a
			a.Provider = tc.provider
			a.Drive = "fixture-drive"
			a.Secrets = map[string]string{"authorization": "cGM6MTM4MDAwMDAwMDA6dG9rZW4=", "api_host": "https://example.yun.139.com", "cookie": "test-cookie", "access_token": "test-token"}
			f.s.saveAccount(a)
			p := newProviders(f.s)
			p.client = &http.Client{Transport: cloudRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != tc.path {
					t.Fatalf("unexpected quota endpoint: %s", r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{}}, nil
			})}
			q, e := p.Quota(context.Background(), a.ID)
			if tc.valid && (e != nil || q.Total != tc.total || q.Used != tc.used) {
				t.Fatalf("wrong quota: %+v %v", q, e)
			}
			if !tc.valid && e == nil {
				t.Fatal("missing usage presented as zero")
			}
		})
	}
}

type reviewClouds struct{ *fixtureCloud }

func (r reviewClouds) Quota(ctx context.Context, id string) (QuotaUsage, error) {
	if id == "review-quark" {
		return QuotaUsage{}, errors.New("此 TV 通道暂未提供容量，文件浏览仍可使用")
	}
	return QuotaUsage{Total: 6 * 1024 * 1024 * 1024 * 1024, Used: 3 * 1024 * 1024 * 1024 * 1024}, nil
}
func TestServeCloudManagementReview(t *testing.T) {
	if os.Getenv("STRMLINK_CLOUD_UI_REVIEW") != "1" {
		t.Skip("local browser fixture")
	}
	f := setup(t)
	f.a.Name = "移动媒体盘（测试账号）"
	f.s.saveAccount(f.a)
	for _, a := range []Account{{ID: "review-ali", Name: "阿里资源库（测试账号）", Provider: "ali", Root: "root", Drive: "d", Secrets: map[string]string{"access_token": "fixture-only"}}, {ID: "review-quark", Name: "夸克影音（测试账号）", Provider: "quarktv", Root: "root", Secrets: map[string]string{"access_token": "fixture-only", "device_id": "fixture"}}} {
		f.s.saveAccount(a)
	}
	f.c.dirs["root"] = []Entry{{ID: "movies", Name: "电影", Dir: true, Modified: "2026-10-18T11:00:00Z"}, {ID: "series", Name: "剧集", Dir: true, Modified: "2026-10-17T12:00:00Z"}, {ID: "music", Name: "音乐", Dir: true, Modified: "2026-10-16T12:00:00Z"}, {ID: "sample", Name: "测试电影.mkv", Size: 12 * 1024 * 1024 * 1024, Modified: "2026-10-18T10:00:00Z"}, {ID: "audio", Name: "测试音轨.flac", Size: 500 * 1024 * 1024, Modified: "2026-10-18T10:00:00Z"}, {ID: "subtitle", Name: "中文字幕.srt", Size: 23000, Modified: "2026-10-17T10:00:00Z"}}
	f.c.dirs["movies"] = []Entry{{ID: "family", Name: "华语电影", Dir: true}, {ID: "sample2", Name: "测试电影 02.mkv", Size: 8 * 1024 * 1024 * 1024, Modified: "2026-10-18T10:00:00Z"}}
	f.c.dirs["family"] = []Entry{}
	f.s.updateAccountReport(f.a.ID, func(v *AccountReport) {
		v.Checked = time.Now().Unix()
		v.Reachable = true
		v.Entries = 6
		v.Quota = &QuotaUsage{Total: 26 * 1024 * 1024 * 1024 * 1024, Used: 20 * 1024 * 1024 * 1024 * 1024, Checked: time.Now().Unix()}
	})
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "StrmLink-Clouds-QA")
	s, e := newServer(f.s, f.e, reviewClouds{f.c}, f.data, f.output)
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{Addr: "127.0.0.1:18796", Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { server.Shutdown(context.Background()) })
	t.Log("Local fixture http://127.0.0.1:18796; no real clouds")
	if e = server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		t.Fatal(e)
	}
}
