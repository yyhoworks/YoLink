package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCommandCatalogUsesLiveEnabledMappingsAndRejectsStaleWrites(t *testing.T) {
	f := setup(t)
	s, h, cookie := testToolsServer(t, f, f.c)
	_, rev, _ := f.s.commandTargets()
	items := []CommandTarget{{Alias: "剧集", Rule: f.r.ID, Path: "/media/剧集", Enabled: true}, {Alias: "临时", Rule: f.r.ID, Path: "/media", Enabled: false}}
	w := generationPOST(h, cookie, "/api/automation/targets", object{"revision": rev, "items": items})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	read := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/events/catalog", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if read("").Code != 401 {
		t.Fatal("catalog accepted unauthenticated access")
	}
	w = read(s.eventToken)
	var catalog struct {
		Items []commandTargetView `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || len(catalog.Items) != 1 || catalog.Items[0].Alias != "剧集" || !strings.HasSuffix(catalog.Items[0].Output, "/剧集") {
		t.Fatal(w.Code, w.Body)
	}
	if w = generationPOST(h, cookie, "/api/automation/targets", object{"revision": rev, "items": items}); w.Code != 409 {
		t.Fatal("stale update overwrote catalog", w.Code)
	}
	_, rev, _ = f.s.commandTargets()
	items[0].Path = "/outside"
	if w = generationPOST(h, cookie, "/api/automation/targets", object{"revision": rev, "items": items}); w.Code != 400 {
		t.Fatal("scope escape accepted", w.Code)
	}
	items[0].Path = "/media/剧集"
	items[0].Enabled = false
	if w = generationPOST(h, cookie, "/api/automation/targets", object{"revision": rev, "items": items}); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = read(s.eventToken)
	json.Unmarshal(w.Body.Bytes(), &catalog)
	if len(catalog.Items) != 0 {
		t.Fatal("disabled command remained usable")
	}
}

func TestDirectoryFailureNotifiesAndRetryCompletesSameScope(t *testing.T) {
	f := setup(t)
	f.s.saveIntegration(IntegrationConfig{WebhookEnabled: true, Webhook: "http://notification.invalid/notify"})
	f.r.Notify = true
	f.s.saveRule(f.r)
	s, h, cookie := testToolsServer(t, f, f.c)
	req := httptest.NewRequest("POST", "/api/events", strings.NewReader(`{"rule":"rule","path":"/media/missing","kind":"directory"}`))
	req.Header.Set("Authorization", "Bearer "+s.eventToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 202 {
		t.Fatal(w.Body)
	}
	f.e.processIngest()
	events, _ := f.s.ingestEvents()
	v := events[0]
	if v.Status != "failed" {
		t.Fatal(v)
	}
	var count int
	f.s.db.QueryRow("SELECT count(*) FROM deliveries WHERE job=?", "ingest:"+v.ID+":1").Scan(&count)
	if count != 1 {
		t.Fatal("directory failure was not queued for notification")
	}
	if w = generationPOST(h, cookie, "/api/ingest/"+v.ID+"/retry", object{}); w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	f.e.ingestFail(v, "stale failure")
	events, _ = f.s.ingestEvents()
	if events[0].Status != "pending" {
		t.Fatal("old worker overwrote retried event", events)
	}
	f.c.dirs["root"] = append(f.c.dirs["root"], Entry{ID: "found", Name: "missing", Dir: true})
	f.c.dirs["found"] = []Entry{}
	f.e.processIngest()
	events, _ = f.s.ingestEvents()
	if events[0].Job == "" {
		t.Fatal("retry did not submit")
	}
	j, _ := f.s.job(events[0].Job)
	j = finish(t, f, j)
	if j.Status != "completed" || j.SourcePath != "/media/missing" {
		t.Fatal(j)
	}
}

func TestAutomationPipelineFollowsRetryAndShowsDeliveryFailure(t *testing.T) {
	f := setup(t)
	f.s.saveJob(Job{ID: "original", Rule: f.r.ID, Status: "partial", RetriedBy: "retry", Created: time.Now().Unix()})
	f.s.saveJob(Job{ID: "retry", Rule: f.r.ID, Status: "completed", SourcePath: "/media/剧集", Skipped: 4, Created: time.Now().Unix()})
	f.s.db.Exec("INSERT INTO ingest_events(id,rule,source_path,status,job,error,created,revision) VALUES('event',?,'/media/剧集','submitted','original','',?,1)", f.r.ID, time.Now().Unix())
	f.e.queueDelivery("retry", "webhook", object{"text": "done"})
	f.s.db.Exec("UPDATE deliveries SET status='failed',attempts=3,error='connection unavailable' WHERE job='retry'")
	s, _, _ := testToolsServer(t, f, f.c)
	items, err := s.automationPipeline()
	if err != nil || len(items) != 1 || items[0]["job"] != "retry" || items[0]["status"] != "completed" || items[0]["notify_status"] != "failed" {
		t.Fatal(items, err)
	}
}

func TestHermesHealthCheckStripsWebhookSecretsAndRequiresExpectedService(t *testing.T) {
	f := setup(t)
	expected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" || r.URL.RawQuery != "" {
			t.Error("health request leaked route/query")
		}
		if expected {
			jsonOut(w, 200, object{"status": "ok", "platform": "webhook"})
		} else {
			jsonOut(w, 200, object{"status": "ok", "platform": "unrelated"})
		}
	}))
	defer target.Close()
	f.s.saveIntegration(IntegrationConfig{WebhookEnabled: true, Webhook: target.URL + "/webhooks/yolink?secret=fixture", WebhookSecret: "fixture-secret"})
	_, h, cookie := testToolsServer(t, f, f.c)
	for _, want := range []bool{false, true} {
		expected = want
		w := generationPOST(h, cookie, "/api/automation/check", object{})
		var result struct {
			OK bool `json:"ok"`
		}
		json.Unmarshal(w.Body.Bytes(), &result)
		if w.Code != 200 || result.OK != want || strings.Contains(w.Body.String(), "fixture") {
			t.Fatal(w.Code, w.Body)
		}
	}
}

func TestIngestRetryKeepsNotificationOrigin(t *testing.T) {
	f := setup(t)
	f.r.Notify = true
	f.s.saveRule(f.r)
	old := Job{ID: "failed-ingest", Rule: f.r.ID, Snapshot: &f.r, Folder: f.r.Folder, SourcePath: f.r.SourcePath, Status: "failed", Trigger: "ingest", Created: time.Now().Unix()}
	f.s.saveJob(old)
	next, err := f.e.retry(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Trigger != "ingest" {
		t.Fatal("retry lost completion-notification origin", next)
	}
	finish(t, f, next)
}
