package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func notificationRevision(t *testing.T, s *Store) string {
	t.Helper()
	v, err := s.channels()
	if err != nil {
		t.Fatal(err)
	}
	return contentHash([]byte(scanJSON(v)))
}
func channelPOST(t *testing.T, s *Store, h http.Handler, cookie *http.Cookie, body object) *httptest.ResponseRecorder {
	t.Helper()
	body["revision"] = notificationRevision(t, s)
	return generationPOST(h, cookie, "/api/notification-channels", body)
}

func TestNotificationChannelsPreserveLegacyAndLibraryAndMaskSecrets(t *testing.T) {
	f := setup(t)
	original := IntegrationConfig{Enabled: true, ServerType: "emby", Base: "http://emby.invalid:8096", APIKey: "fixture-library", RefreshAll: true, WebhookEnabled: true, Webhook: "http://hermes.invalid/webhooks/yolink?token=fixture-url", WebhookSecret: "fixture-signature"}
	f.s.saveIntegration(original)
	_, h, cookie := testToolsServer(t, f, f.c)
	w := channelPOST(t, f.s, h, cookie, object{"id": legacyChannelID, "name": "我的 Hermes", "type": "hermes", "enabled": true, "events": []string{"success", "failure"}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	items, err := f.s.channels()
	if err != nil || len(items) != 1 || items[0].Secret != original.WebhookSecret || items[0].Webhook != original.Webhook {
		t.Fatal("legacy connection not preserved", err)
	}
	c, _ := f.s.integrationConfig()
	if c.APIKey != original.APIKey || c.Base != original.Base || !c.Enabled || c.Webhook != "" || c.WebhookSecret != "" {
		t.Fatal("library changed or legacy credentials duplicated")
	}
	req := httptest.NewRequest("GET", "/api/notification-channels", nil)
	req.AddCookie(cookie)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	if strings.Contains(out.Body.String(), "fixture-") {
		t.Fatal("channel secrets exposed")
	}
	if strings.Contains(f.s.setting("notification_channels", ""), "fixture-") {
		t.Fatal("channel configuration stored in plaintext")
	}
	stale := generationPOST(h, cookie, "/api/notification-channels", object{"id": legacyChannelID, "name": "lost edit", "type": "hermes", "revision": "stale"})
	if stale.Code != 409 {
		t.Fatal("stale editor accepted")
	}
	w = channelPOST(t, f.s, h, cookie, object{"id": legacyChannelID, "name": "我的 Hermes", "type": "hermes", "enabled": true, "events": []string{"failure"}, "clear_secret": true})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	items, _ = f.s.channels()
	if items[0].Secret != "" || items[0].Version != 2 {
		t.Fatal("explicit signature clear not applied")
	}
}

func TestNotificationFanoutPauseDeleteAndReplacementCannotRetargetBacklog(t *testing.T) {
	f := setup(t)
	hits := map[string]int{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits[r.URL.Path]++; jsonOut(w, 200, object{"ok": true}) }))
	defer target.Close()
	_, h, cookie := testToolsServer(t, f, f.c)
	add := func(name, path string, events []string) string {
		w := channelPOST(t, f.s, h, cookie, object{"name": name, "type": "webhook", "enabled": true, "events": events, "webhook": target.URL + path})
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		var v struct {
			Item NotificationChannel `json:"item"`
		}
		json.Unmarshal(w.Body.Bytes(), &v)
		return v.Item.ID
	}
	one := add("主通知", "/one", []string{"success", "failure"})
	two := add("只收失败", "/two", []string{"failure"})
	f.e.queueNotifications("success", object{"status": "completed"})
	f.e.queueNotifications("failure", object{"status": "failed"})
	f.s.db.Exec("UPDATE deliveries SET next_at=0")
	f.e.processDeliveries()
	if hits["/one"] != 2 || hits["/two"] != 1 {
		t.Fatal("event fanout incorrect", hits)
	}
	f.e.queueNotifications("paused", object{"status": "failed"})
	w := channelPOST(t, f.s, h, cookie, object{"id": one, "name": "主通知", "type": "webhook", "enabled": false, "events": []string{"success", "failure"}})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	var status string
	f.s.db.QueryRow("SELECT status FROM deliveries WHERE job='paused' AND channel=?", one).Scan(&status)
	if status != "paused" {
		t.Fatal("pending notification not paused", status)
	}
	w = channelPOST(t, f.s, h, cookie, object{"id": one, "name": "主通知", "type": "webhook", "enabled": true, "events": []string{"success", "failure"}})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	f.s.db.QueryRow("SELECT status FROM deliveries WHERE job='paused' AND channel=?", one).Scan(&status)
	if status != "pending" {
		t.Fatal("paused notification not resumed")
	}
	w = channelPOST(t, f.s, h, cookie, object{"id": one, "name": "主通知", "type": "webhook", "enabled": true, "events": []string{"success", "failure"}, "webhook": target.URL + "/replacement"})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	f.s.db.QueryRow("SELECT status FROM deliveries WHERE job='paused' AND channel=?", one).Scan(&status)
	if status != "cancelled" {
		t.Fatal("replacement retained old backlog")
	}
	w = generationPOST(h, cookie, "/api/notification-channels/"+two+"/remove", object{"revision": notificationRevision(t, f.s)})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	f.s.db.Exec("UPDATE deliveries SET next_at=0")
	f.e.processDeliveries()
	if hits["/replacement"] != 0 || hits["/two"] != 1 {
		t.Fatal("old notification sent to changed/deleted channel", hits)
	}
	var oldID string
	f.s.db.QueryRow("SELECT id FROM deliveries WHERE job='paused' AND channel=?", one).Scan(&oldID)
	if generationPOST(h, cookie, "/api/deliveries/"+oldID+"/retry", object{}).Code != 409 {
		t.Fatal("cancelled old delivery could be replayed")
	}
	items, _ := f.s.channels()
	if len(items) != 1 || items[0].ID != one {
		t.Fatal("delete removed wrong connection")
	}
}

type telegramTestTransport func(*http.Request) (*http.Response, error)

func (fn telegramTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func TestTelegramNotificationOnlySendsMessagesAndSanitizesErrors(t *testing.T) {
	original := telegramHTTPClient
	t.Cleanup(func() { telegramHTTPClient = original })
	count := 0
	telegramHTTPClient = &http.Client{Transport: telegramTestTransport(func(r *http.Request) (*http.Response, error) {
		count++
		if r.URL.Host != "api.telegram.org" || r.URL.Scheme != "https" || !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Fatal("telegram used unexpected endpoint")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["chat_id"] != "-100123" || body["text"] == "" || body["parse_mode"] != nil {
			t.Fatal("telegram payload invalid")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: http.Header{}}, nil
	})}
	f := setup(t)
	_, h, cookie := testToolsServer(t, f, f.c)
	w := channelPOST(t, f.s, h, cookie, object{"name": "我的 Telegram", "type": "telegram", "enabled": true, "events": []string{"success"}, "bot_token": "123:fixture_token", "chat_id": "-100123"})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	items, _ := f.s.channels()
	w = generationPOST(h, cookie, "/api/notification-channels/"+items[0].ID+"/test", object{})
	if w.Code != 200 || count != 1 {
		t.Fatal(w.Code, w.Body, count)
	}
	telegramHTTPClient = &http.Client{Transport: telegramTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":401,"description":"fixture_token must not leak"}`)), Header: http.Header{}}, nil
	})}
	w = generationPOST(h, cookie, "/api/notification-channels/"+items[0].ID+"/test", object{})
	if w.Code != 400 || strings.Contains(w.Body.String(), "fixture_token") {
		t.Fatal("telegram token exposed or failure accepted", w.Body)
	}
}

func TestNoEnabledNotificationChannelDoesNotCreateFailureQueue(t *testing.T) {
	f := setup(t)
	f.r.Notify = true
	f.e.afterGeneration(Job{ID: "no-channel", Trigger: "ingest", Status: "completed"}, f.r)
	var count int
	f.s.db.QueryRow("SELECT count(*) FROM deliveries").Scan(&count)
	if count != 0 {
		t.Fatal("no channel created a failing delivery")
	}
}

func TestOldDeliverySchemaUpgradePreservesHistoryAndAllowsIndependentTargets(t *testing.T) {
	f := setup(t)
	_, err := f.s.db.Exec(`DROP TABLE deliveries;CREATE TABLE deliveries(id TEXT PRIMARY KEY,job TEXT,kind TEXT,status TEXT,attempts INTEGER DEFAULT 0,next_at INTEGER,payload TEXT,error TEXT DEFAULT '',UNIQUE(job,kind));INSERT INTO deliveries(id,job,kind,status,attempts,next_at,payload,error) VALUES('old','old-job','webhook','sent',2,0,'{"title":"old"}','');`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = f.s.initConnectionSchema(); err != nil {
			t.Fatal(err)
		}
	}
	var status, channel, raw string
	var attempts int
	if err = f.s.db.QueryRow("SELECT status,attempts,payload,channel FROM deliveries WHERE id='old'").Scan(&status, &attempts, &raw, &channel); err != nil || status != "sent" || attempts != 2 || raw != `{"title":"old"}` || channel != legacyChannelID {
		t.Fatal("history migration changed original content", err)
	}
	if _, err = f.s.db.Exec("INSERT INTO deliveries(id,job,kind,status,channel) VALUES('second','old-job','webhook','pending','second-channel')"); err != nil {
		t.Fatal("independent target blocked by old unique constraint", err)
	}
}

func TestApplicationCredentialsAreScopedRevocableAndCannotManage(t *testing.T) {
	f := setup(t)
	other := f.r
	other.ID = "other-rule"
	other.Name = "other"
	f.s.saveRule(other)
	s, h, cookie := testToolsServer(t, f, f.c)
	w := generationPOST(h, cookie, "/api/event-clients", object{"name": "我的脚本", "enabled": true, "rules": []string{f.r.ID}})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	if created.Token == "" {
		t.Fatal("new credential not issued")
	}
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		return out
	}
	if call("GET", "/api/state", created.Token, "").Code != 401 {
		t.Fatal("event token gained admin access")
	}
	w = call("POST", "/api/events", created.Token, `{"rule":"other-rule","path":"/media","kind":"directory"}`)
	if w.Code != 403 {
		t.Fatal("unallowed rule accepted", w.Body)
	}
	w = call("POST", "/api/events", created.Token, `{"rule":"rule","path":"/media/剧集","kind":"directory"}`)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	f.s.saveJob(Job{ID: "hidden-job", Rule: other.ID, Status: "completed", Created: time.Now().Unix(), SourcePath: "/private-scope"})
	w = call("GET", "/api/events/status", created.Token, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-scope") {
		t.Fatal("task scope leaked")
	}
	req := httptest.NewRequest("GET", "/api/event-clients", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if bytes.Contains(w.Body.Bytes(), []byte(created.Token)) || !bytes.Contains(w.Body.Bytes(), []byte("我的脚本")) {
		t.Fatal("credential listing leaked token or omitted source")
	}
	w = generationPOST(h, cookie, "/api/event-clients/"+created.ID+"/rotate", object{})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	var next struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &next)
	if call("GET", "/api/events/status", created.Token, "").Code != 401 || call("GET", "/api/events/status", next.Token, "").Code != 200 {
		t.Fatal("token rotation ineffective")
	}
	if generationPOST(h, cookie, "/api/event-clients/legacy", object{"enabled": false}).Code != 200 || call("GET", "/api/events/status", s.eventToken, "").Code != 401 {
		t.Fatal("legacy disable ineffective")
	}
	if call("GET", "/api/events/status", next.Token, "").Code != 200 {
		t.Fatal("legacy disable affected independent app")
	}
	if generationPOST(h, cookie, "/api/event-clients/"+created.ID+"/remove", object{}).Code != 200 || call("GET", "/api/events/status", next.Token, "").Code != 401 {
		t.Fatal("app revoke ineffective")
	}
	var events int
	f.s.db.QueryRow("SELECT count(*) FROM ingest_events").Scan(&events)
	if events != 1 {
		t.Fatal("revocation modified already submitted tasks")
	}
}
