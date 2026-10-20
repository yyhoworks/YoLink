package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func summaryGET(t *testing.T, h http.Handler, c *http.Cookie, params string) ([]PlaybackSummary, int, int) {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/records?kind=playback&summary=1"+params, nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var data struct {
		Items    []PlaybackSummary `json:"items"`
		Total    int               `json:"total"`
		Requests int               `json:"request_total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data.Items, data.Total, data.Requests
}
func appendPlayPair(f *fixture, stamp int64, request, ip, outcome string, cached bool) {
	a := Activity{Time: stamp, Kind: "direct", Event: "get", Outcome: "resolved", Media: "test-media", Account: f.a.ID, Name: "movie.mkv", Request: request, IP: ip, UserAgent: "Lavf/QA", Client: "Lavf", Method: "GET", Mode: "302", Cache: cached, Millis: 2}
	if outcome == "failed" {
		a.Outcome = "failed"
		a.Error = "临时错误"
	}
	f.s.appendActivity(a)
	a.Kind = "playback"
	a.Event = "play"
	a.Outcome = outcome
	a.HTTP = 302
	a.Millis = 12
	if outcome == "failed" {
		a.HTTP = 502
	}
	f.s.appendActivity(a)
}
func TestPlaybackSummaryGroupsFullRequestSetAndKeepsErrorsVisible(t *testing.T) {
	f := setup(t)
	_, h, c := testToolsServer(t, f, f.c)
	now := time.Now().UnixMilli()
	for i := 0; i < 6; i++ {
		appendPlayPair(f, now+int64(i)*300, "req"+strconv.Itoa(i), "192.0.2.1", "redirected", i > 0)
	}
	items, total, requests := summaryGET(t, h, c, "")
	if total != 1 || requests != 6 || len(items) != 1 || items[0].RequestCount != 6 || items[0].ResolveCount != 1 || items[0].ReuseCount != 5 || items[0].Millis != 12 {
		t.Fatal(total, requests, items)
	}
	group := items[0].GroupID
	appendPlayPair(f, now+2000, "failure", "192.0.2.1", "failed", false)
	appendPlayPair(f, now+2500, "other-client", "192.0.2.2", "redirected", true)
	appendPlayPair(f, now+40000, "later", "192.0.2.1", "redirected", false)
	items, total, requests = summaryGET(t, h, c, "")
	if total != 4 || requests != 9 {
		t.Fatal(total, requests, items)
	}
	found := false
	for _, item := range items {
		if item.GroupID == group && item.RequestCount == 6 {
			found = true
		}
	}
	if !found {
		t.Fatal("stable request group disappeared")
	}
	items, total, requests = summaryGET(t, h, c, "&outcome=failed")
	if total != 1 || requests != 1 || items[0].Error == "" {
		t.Fatal("failure hidden by aggregation", items)
	}
	_, total, _ = summaryGET(t, h, c, "&q=missing")
	if total != 0 {
		t.Fatal("search ignored")
	}
	items, total, _ = summaryGET(t, h, c, "&limit=1&page=2")
	if total != 4 || len(items) != 1 {
		t.Fatal("group pagination wrong")
	}
	// Raw events are still available for debugging and have not been collapsed.
	_, raw, n := recordsGET(t, h, c, "/api/records?kind=playback")
	if n != 9 || len(raw) != 9 {
		t.Fatal("raw records changed")
	}
}
func TestPlaybackGroupDetailsPaginateAndCorrelateOnlySelectedRequests(t *testing.T) {
	f := setup(t)
	_, h, c := testToolsServer(t, f, f.c)
	now := time.Now().UnixMilli()
	for i := 0; i < 26; i++ {
		appendPlayPair(f, now+int64(i), "request-"+strconv.Itoa(i), "192.0.2.1", "redirected", i > 0)
	}
	appendPlayPair(f, now+50000, "unrelated", "192.0.2.1", "redirected", false)
	items, _, _ := summaryGET(t, h, c, "")
	var group int64
	for _, item := range items {
		if item.RequestCount == 26 {
			group = item.GroupID
		}
	}
	r := httptest.NewRequest("GET", "/api/records/trace?media=test-media&group="+strconv.FormatInt(group, 10)+"&page=2", nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var data struct {
		Playback, Direct   []Activity
		Total, Page, Pages int
	}
	json.Unmarshal(w.Body.Bytes(), &data)
	if w.Code != 200 || data.Total != 26 || data.Page != 2 || data.Pages != 2 || len(data.Playback) != 6 || len(data.Direct) != 6 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, a := range append(data.Playback, data.Direct...) {
		if a.Request == "unrelated" {
			t.Fatal("unrelated session leaked into request detail")
		}
	}
}
func TestDiagnosticsExcludeAutomaticResolutionAndCollapseCheckAttempts(t *testing.T) {
	f := setup(t)
	_, h, c := testToolsServer(t, f, f.c)
	appendPlayPair(f, time.Now().UnixMilli(), "automatic", "192.0.2.1", "redirected", false)
	f.s.appendActivity(Activity{Kind: "direct", Media: "test-media", Event: "check", Outcome: "resolved", Request: "manual-check"})
	f.s.appendActivity(Activity{Kind: "direct", Media: "test-media", Event: "check", Outcome: "readable", Request: "manual-check"})
	f.s.appendActivity(Activity{Kind: "direct", Media: "test-media", Event: "resolve", Outcome: "failed", Request: "manual-resolve", Error: "test error"})
	_, items, total := recordsGET(t, h, c, "/api/records?kind=direct&scope=diagnostic")
	if total != 2 || len(items) != 2 {
		t.Fatal(total, items)
	}
	for _, item := range items {
		if item.Request == "automatic" || item.Event == "check" && item.Outcome != "readable" {
			t.Fatal("duplicate diagnostic or automatic request", item)
		}
	}
	_, _, total = recordsGET(t, h, c, "/api/records?kind=direct")
	if total != 4 {
		t.Fatal("raw direct events removed")
	}
}
