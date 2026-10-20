package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompanionCopyOptionsAreIndependentAndIdempotent(t *testing.T) {
	for _, options := range []struct{ nonmedia, subtitles bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		f := setup(t)
		f.r.CopyNonMedia, f.r.CopySubtitles = options.nonmedia, options.subtitles
		f.s.saveRule(f.r)
		f.c.dirs["root"] = append(f.c.dirs["root"], Entry{ID: "poster", Name: "poster.jpg"}, Entry{ID: "subtitle", Name: "电影 & 测试.zh.srt"}, Entry{ID: "playlist", Name: "old.strm"})
		f.e.companionDownload = func(context.Context, PlaybackTarget) ([]byte, error) { return []byte(strings.Repeat("p", 100000)), nil }
		f.e.subtitleDownload = func(context.Context, PlaybackTarget) ([]byte, error) { return []byte("subtitle"), nil }
		first := generate(t, f, false, 0)
		if first.Status != "completed" || first.CreatedFiles != 3 {
			t.Fatalf("failed: %+v", first)
		}
		if (first.NonMediaFiles == 2) != options.nonmedia || (first.Sidecars == 1) != options.subtitles {
			t.Fatalf("independent switches: %+v", first)
		}
		if _, err := os.Stat(filepath.Join(f.output, "test/media/old.strm")); !os.IsNotExist(err) {
			t.Fatal("copied a source STRM")
		}
		second := generate(t, f, false, 0)
		if second.Status != "completed" || second.NonMediaFiles != 0 || second.Sidecars != 0 || options.nonmedia && second.NonMediaSkipped != 2 {
			t.Fatalf("not idempotent beyond 64KB: %+v", second)
		}
		o, _ := f.s.generationOverview()
		if o.Outputs != 3 {
			t.Fatal("sidecars inflated STRM count")
		}
	}
}

func TestCompanionCopyPreviewLimitsAndExistingProtection(t *testing.T) {
	f := setup(t)
	f.r.CopyNonMedia = true
	f.s.saveRule(f.r)
	f.c.dirs["root"] = append(f.c.dirs["root"], Entry{ID: "poster", Name: "poster.jpg"}, Entry{ID: "large", Name: "large.nfo", Size: 33 << 20})
	calls := 0
	f.e.companionDownload = func(context.Context, PlaybackTarget) ([]byte, error) { calls++; return []byte("new"), nil }
	preview := generate(t, f, true, 0)
	if calls != 0 || preview.NonMediaFiles != 0 {
		t.Fatal("preview downloaded sidecars")
	}
	if err := os.MkdirAll(filepath.Join(f.output, "test/media"), 0755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(f.output, "test/media/poster.jpg")
	os.WriteFile(p, []byte("keep"), 0644)
	j := generate(t, f, false, 0)
	if j.Status != "partial" || j.Errors != 2 || j.NonMediaFiles != 1 || calls != 2 {
		t.Fatalf("wrong limits/protection: %+v, calls %d", j, calls)
	}
	if b, _ := os.ReadFile(p); string(b) != "keep" {
		t.Fatal("existing sidecar overwritten")
	}
}

func TestPlaylistExportUsesStableLinksAndScopesRule(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	s, h, cookie := testToolsServer(t, f, f.c)
	_ = s
	get := func(url string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", url, nil)
		if auth {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w := get("/api/rules/rule/playlist", true)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "#EXTM3U\n") || strings.Count(w.Body.String(), "#EXTINF:") != 3 || strings.Contains(w.Body.String(), "cdn.example.com") || strings.Contains(w.Body.String(), "fixture-secret") {
		t.Fatalf("invalid playlist: %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("not a downloadable playlist")
	}
	mid := f.s.mediaID(f.a, "movie")
	w = get("/api/media/"+mid+"/playlist", true)
	if w.Code != 200 || strings.Count(w.Body.String(), "#EXTINF:") != 1 || !strings.Contains(w.Body.String(), f.s.signature(mid)) {
		t.Fatal("single stable URL missing")
	}
	if get("/api/rules/rule/playlist", false).Code != 401 {
		t.Fatal("unauthenticated export")
	}
	other := f.r
	other.ID = "other"
	other.Output = "other"
	f.s.saveRule(other)
	if get("/api/rules/other/playlist", true).Code != 404 {
		t.Fatal("exported a different output mapping")
	}
	f.a.Disabled = true
	f.s.saveAccount(f.a)
	if get("/api/rules/rule/playlist", true).Code != 409 {
		t.Fatal("exported disabled account")
	}
}

func TestPlayerHeadRejectsErrorDocumentsAndBadRanges(t *testing.T) {
	for _, tc := range []struct {
		status           int
		kind, rangeValue string
		want             int
	}{{403, "application/xml", "", 502}, {200, "text/html", "", 502}, {206, "video/mp4", "bytes 0-0/invalid", 502}, {200, "video/mp4", "", 200}} {
		f := setup(t)
		s, _, _ := testToolsServer(t, f, f.c)
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", tc.kind)
			if tc.rangeValue != "" {
				w.Header().Set("Content-Range", tc.rangeValue)
			}
			w.WriteHeader(tc.status)
			io.WriteString(w, "some-data")
		}))
		s.probeClient = up.Client()
		w := httptest.NewRecorder()
		status := s.playbackHead(w, httptest.NewRequest("HEAD", "/", nil), PlaybackTarget{URL: up.URL}, 999)
		up.Close()
		if status != tc.want {
			t.Fatalf("status %d want %d", status, tc.want)
		}
	}
}
