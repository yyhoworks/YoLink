package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGenerationOverviewCountsRecordsOnce(t *testing.T) {
	f := setup(t)
	preview := generate(t, f, true, 0)
	if preview.Status != "completed" {
		t.Fatal(preview)
	}
	o, e := f.s.generationOverview()
	if e != nil || o.Media != 0 || o.Outputs != 0 {
		t.Fatalf("preview polluted overview: %+v %v", o, e)
	}
	generate(t, f, false, 0)
	generate(t, f, false, 0)
	o, e = f.s.generationOverview()
	if e != nil || o.Media != 3 || o.Outputs != 3 || o.Running != 0 || o.Queued != 0 {
		t.Fatalf("overview double counted: %+v %v", o, e)
	}
}
func TestFailedJobRetryPreservesScopeAndReplacesFailure(t *testing.T) {
	f := setup(t)
	f.c.fail = "sub"
	original, e := f.e.enqueue(f.r.ID, "sub", "/media/剧集", false, 1)
	if e != nil {
		t.Fatal(e)
	}
	original = finish(t, f, original)
	if original.Status != "failed" {
		t.Fatal(original)
	}
	f.c.fail = ""
	next, e := f.e.retry(original.ID)
	if e != nil {
		t.Fatal(e)
	}
	next = finish(t, f, next)
	if next.Status != "completed" || next.RetryOf != original.ID || next.Limit != 1 || next.Folder != "sub" || next.SourcePath != "/media/剧集" || next.CreatedFiles != 1 {
		t.Fatalf("retry scope changed: %+v", next)
	}
	old, _ := f.s.job(original.ID)
	if old.RetriedBy != next.ID {
		t.Fatal("original failure not linked")
	}
	left, e := f.s.unresolvedJobs()
	if e != nil || len(left) != 0 {
		t.Fatalf("stale failures: %v %v", left, e)
	}
	if _, e = f.e.retry(original.ID); e == nil {
		t.Fatal("same failure retried twice")
	}
	if _, e = f.e.retry(next.ID); e == nil {
		t.Fatal("successful task accepted for retry")
	}
}
func TestRetryFailureRemainsVisibleOnlyAsLatestAttempt(t *testing.T) {
	f := setup(t)
	f.c.fail = "root"
	original := generate(t, f, false, 0)
	next, e := f.e.retry(original.ID)
	if e != nil {
		t.Fatal(e)
	}
	next = finish(t, f, next)
	if next.Status != "failed" {
		t.Fatal(next)
	}
	failures, e := f.s.unresolvedJobs()
	if e != nil || len(failures) != 1 || failures[0].ID != next.ID {
		t.Fatalf("wrong current failure: %+v %v", failures, e)
	}
	o, e := f.s.generationOverview()
	if e != nil || o.Failed != 1 {
		t.Fatalf("failure count: %+v %v", o, e)
	}
}
func generationHandler(t *testing.T, f *fixture) (http.Handler, *http.Cookie) {
	t.Helper()
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "fixture-admin-password")
	s, e := newServer(f.s, f.e, f.c, f.data, f.output)
	if e != nil {
		t.Fatal(e)
	}
	h := s.handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"fixture-admin-password"}`)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return h, w.Result().Cookies()[0]
}
func generationPOST(h http.Handler, cookie *http.Cookie, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestRuleCanBeEditedWithoutRecreatingAndCannotChangeActiveScope(t *testing.T) {
	f := setup(t)
	h, cookie := generationHandler(t, f)
	r := f.r
	r.Output = "test/new-library"
	w := generationPOST(h, cookie, "/api/rules", r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	saved, _ := f.s.rule(r.ID)
	if saved.Output != r.Output {
		t.Fatal("mapping was not updated")
	}
	f.s.saveJob(Job{ID: "active", Rule: r.ID, Status: "queued", Created: time.Now().Unix()})
	r.Output = "test/changed-again"
	w = generationPOST(h, cookie, "/api/rules", r)
	if w.Code != 409 {
		t.Fatal("active mapping changed", w.Code, w.Body.String())
	}
	after, _ := f.s.rule(r.ID)
	if after.Output != saved.Output {
		t.Fatal("blocked mapping still changed")
	}
}
func TestRemovingRulePreservesGeneratedMediaAndFiles(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	h, cookie := generationHandler(t, f)
	w := generationPOST(h, cookie, "/api/rules/"+f.r.ID+"/remove", map[string]any{})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, e := f.s.rule(f.r.ID); e == nil {
		t.Fatal("rule still exists")
	}
	o, e := f.s.generationOverview()
	if e != nil || o.Outputs != 3 || o.Media != 3 {
		t.Fatalf("removed media records: %+v %v", o, e)
	}
	if _, e = os.Stat(filepath.Join(f.output, "test/media/音乐.strm")); e != nil {
		t.Fatal("removed generated file", e)
	}
}
func TestBulkRetryReportsDeferredScopes(t *testing.T) {
	f := setup(t)
	h, cookie := generationHandler(t, f)
	for _, id := range []string{"failed-a", "failed-b"} {
		f.s.saveJob(Job{ID: id, Rule: f.r.ID, Snapshot: &f.r, Folder: "root", SourcePath: f.r.SourcePath, Status: "failed", Created: time.Now().Unix()})
	}
	w := generationPOST(h, cookie, "/api/jobs/retry-failed", map[string]any{})
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Started  []Job    `json:"started"`
		Deferred []object `json:"deferred"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if len(result.Started)+len(result.Deferred) != 2 || len(result.Started) == 0 {
		t.Fatalf("bulk retry lost scopes: %s", w.Body.String())
	}
	// Both failed jobs share one rule+folder scope, and only one retried
	// scope per rule may be active: exactly one must have started as a retry
	// of one original, and the other must be reported deferred by ID with a
	// reason — never silently dropped or double-started.
	if len(result.Started) != 1 || len(result.Deferred) != 1 {
		t.Fatalf("bulk retry split = %d started / %d deferred, want 1/1: %s", len(result.Started), len(result.Deferred), w.Body.String())
	}
	started := result.Started[0]
	if started.RetryOf != "failed-a" && started.RetryOf != "failed-b" {
		t.Fatalf("started job is not a retry of an original failure: %+v", started)
	}
	wantDeferred := "failed-a"
	if started.RetryOf == "failed-a" {
		wantDeferred = "failed-b"
	}
	if got := result.Deferred[0]["id"]; got != wantDeferred {
		t.Fatalf("deferred scope = %v, want %s", got, wantDeferred)
	}
	if reason, _ := result.Deferred[0]["reason"].(string); reason == "" {
		t.Fatalf("deferred scope carries no reason: %v", result.Deferred[0])
	}
	for _, j := range result.Started {
		finish(t, f, j)
	}
}

// A gated local-only browser fixture. It is excluded from the service binary.
func TestServeGenerationReview(t *testing.T) {
	if os.Getenv("STRMLINK_UI_REVIEW") != "1" {
		t.Skip("local browser fixture")
	}
	f := setup(t)
	f.a.Name = "测试账号（界面验证）"
	if e := f.s.saveAccount(f.a); e != nil {
		t.Fatal(e)
	}
	f.r.Name = "测试媒体目录"
	f.s.saveRule(f.r)
	generate(t, f, false, 0)
	f.s.saveJob(Job{ID: "review-failure", Rule: f.r.ID, Folder: f.r.Folder, SourcePath: f.r.SourcePath, Status: "failed", Created: time.Now().Unix(), Errors: 1, Message: "测试失败记录：临时输出权限错误", Failures: []string{"本地测试数据，未连接真实网盘"}})
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "StrmLink-Workflow-QA")
	s, e := newServer(f.s, f.e, f.c, f.data, f.output)
	if e != nil {
		t.Fatal(e)
	}
	runReviewFixture(t, s.handler(), "generation review; fixture data only")
}
