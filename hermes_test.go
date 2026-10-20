package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWebhookHermesSignatureAndRetryIdentity(t *testing.T) {
	f := setup(t)
	ids := []string{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		stamp := r.Header.Get("X-Webhook-Timestamp")
		unix, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil || time.Since(time.Unix(unix, 0)).Abs() > 10*time.Second {
			t.Error("missing or stale timestamp")
		}
		mac := hmac.New(sha256.New, []byte("fixture-signing-secret"))
		mac.Write(append([]byte(stamp+"."), raw...))
		if !hmac.Equal([]byte(r.Header.Get("X-Webhook-Signature-V2")), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			t.Error("receiver could not validate exact request bytes")
		}
		ids = append(ids, r.Header.Get("X-Request-ID"))
		if len(ids) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer target.Close()
	c := IntegrationConfig{WebhookEnabled: true, Webhook: target.URL, WebhookSecret: "fixture-signing-secret"}
	if err := f.s.saveIntegration(c); err != nil {
		t.Fatal(err)
	}
	f.e.queueDelivery("fixture-job", "webhook", object{"text": "国产剧 生成完成 <>&", "job": "fixture-job"})
	for range 2 {
		f.s.db.Exec("UPDATE deliveries SET next_at=0")
		f.e.processDeliveries()
	}
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("retry identity was not stable: %v", ids)
	}
	var status string
	f.s.db.QueryRow("SELECT status FROM deliveries WHERE job='fixture-job'").Scan(&status)
	if status != "sent" {
		t.Fatal(status)
	}
}

func TestWebhookSigningSecretHiddenPreservedAndCleared(t *testing.T) {
	f := setup(t)
	f.s.saveIntegration(IntegrationConfig{WebhookSecret: "fixture-signing-secret"})
	_, handler, cookie := testToolsServer(t, f, f.c)
	for _, body := range []string{"", `{}`, `{"clear_webhook_secret":true}`} {
		method := "GET"
		if body != "" {
			method = "POST"
		}
		req := httptest.NewRequest(method, "/api/integrations", strings.NewReader(body))
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != 200 || strings.Contains(w.Body.String(), "fixture-signing-secret") {
			t.Fatalf("secret handling failed: %d %s", w.Code, w.Body)
		}
		c, err := f.s.integrationConfig()
		if err != nil || (c.WebhookSecret == "") != strings.Contains(body, "true") {
			t.Fatalf("blank preservation / explicit clear failed: %+v %v", c, err)
		}
	}
	if strings.Contains(f.s.setting("integration_config", ""), "fixture-signing-secret") {
		t.Fatal("signing secret was stored in plaintext")
	}
}

func TestEventStatusRequiresTokenAndOmitsSensitiveJobFields(t *testing.T) {
	f := setup(t)
	f.s.saveJob(Job{ID: "status-job", Rule: f.r.ID, Status: "completed", SourcePath: "/media/国产剧", Created: time.Now().Unix(), Snapshot: &f.r, Samples: []string{"private-playback-link"}, Failures: []string{"private-details"}})
	s, handler, cookie := testToolsServer(t, f, f.c)
	for _, token := range []string{"", "wrong", s.eventToken} {
		req := httptest.NewRequest("GET", "/api/events/status", nil)
		req.AddCookie(cookie) // Administrator cookies cannot replace the automation token.
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if token != s.eventToken {
			if w.Code != 401 {
				t.Fatal("missing credential accepted")
			}
			continue
		}
		var data struct {
			Jobs []map[string]any `json:"jobs"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &data) != nil || len(data.Jobs) != 1 {
			t.Fatalf("status not available %d %s", w.Code, w.Body)
		}
		if data.Jobs[0]["source_path"] != "/media/国产剧" || strings.Contains(w.Body.String(), "private-") || strings.Contains(w.Body.String(), "snapshot") {
			t.Fatal("status exposed unnecessary data or incorrect scope")
		}
	}
}

func TestIngestCompletionNotifiesEvenWithNoChanges(t *testing.T) {
	f := setup(t)
	f.s.saveIntegration(IntegrationConfig{WebhookEnabled: true, Webhook: "http://notification.invalid/notify"})
	f.r.Notify = true
	j := Job{ID: "manual-unchanged", Rule: f.r.ID, Trigger: "ingest", Status: "completed", Skipped: 5, SourcePath: "/media/国产剧"}
	f.e.afterGeneration(j, f.r)
	var raw string
	if err := f.s.db.QueryRow("SELECT payload FROM deliveries WHERE job=?", j.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "/media/国产剧") || !strings.Contains(raw, `"skipped":5`) {
		t.Fatal("target scope or no-change result missing")
	}
	j.ID = "scheduled-unchanged"
	j.Trigger = "schedule"
	f.e.afterGeneration(j, f.r)
	var count int
	f.s.db.QueryRow("SELECT count(*) FROM deliveries WHERE job=?", j.ID).Scan(&count)
	if count != 0 {
		t.Fatal("unchanged scheduled tasks should stay quiet")
	}
	// An unsigned generic receiver remains supported.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Webhook-Signature-V2") != "" {
			t.Error("unexpected signature")
		}
		w.WriteHeader(204)
	}))
	defer target.Close()
	if err := webhookRequest(context.Background(), IntegrationConfig{Webhook: target.URL}, object{"event": "test"}, "test"); err != nil {
		t.Fatal(err)
	}
}
