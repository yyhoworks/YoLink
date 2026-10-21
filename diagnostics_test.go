package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func diagnosticMedia(t *testing.T, f *fixture) Media {
	t.Helper()
	generate(t, f, false, 0)
	m, err := f.s.media(f.s.mediaID(f.a, "movie"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDiagnosticReportStepsBoundedProbeRedactionAndExport(t *testing.T) {
	f := setup(t)
	m := diagnosticMedia(t, f)
	f.c.target = PlaybackTarget{URL: "https://cdn.example.test/secret-path?token=never-export-this", Mode: "302", Headers: http.Header{"Cookie": {"never-export-cookie"}}}
	s, h, cookie := testToolsServer(t, f, f.c)
	s.probeClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "HEAD" {
			return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
		}
		if r.Header.Get("Range") != "bytes=0-1023" {
			t.Error("unbounded media probe")
		}
		return &http.Response{StatusCode: 206, Body: io.NopCloser(strings.NewReader(strings.Repeat("v", 5000))), Header: http.Header{"Content-Type": {"video/mp4"}, "Content-Range": {"bytes 0-1023/5000"}}}, nil
	})}
	w := generationPOST(h, cookie, "/api/media/"+m.ID+"/diagnose", object{"probe": true})
	var report DiagnosticReport
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &report) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(report.Steps) != 5 || !report.Readable || !report.Range || report.Bytes != 1024 || report.HeadHTTP != 403 {
		t.Fatal("unexpected diagnosis", report)
	}
	for _, step := range report.Steps {
		if step.Status != "passed" {
			t.Fatal(step)
		}
	}
	for _, secret := range []string{"never-export", "cdn.example.test", m.Name, m.Path, "?k="} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("diagnostic contains private data", secret)
		}
	}
	get := httptest.NewRequest("GET", "/api/diagnostics/"+report.ID+"/export", nil)
	get.AddCookie(cookie)
	export := httptest.NewRecorder()
	h.ServeHTTP(export, get)
	if export.Code != 200 || export.Body.String() != w.Body.String() || !strings.HasPrefix(export.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("export mismatch", export.Code)
	}
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest("GET", get.URL.String(), nil))
	if unauth.Code != 401 {
		t.Fatal("export lacks auth")
	}
	if _, err := f.s.db.Exec("UPDATE diagnostic_reports SET created=?", time.Now().Add(-8*24*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	expired := httptest.NewRecorder()
	h.ServeHTTP(expired, get)
	if expired.Code != 404 {
		t.Fatal("expired report exported")
	}
}

func TestDiagnosticModifiedOutputRemainsFailedAndNoOverwrite(t *testing.T) {
	f := setup(t)
	m := diagnosticMedia(t, f)
	file := filepath.Join(f.output, "test/media/电影 & 测试.strm")
	if err := os.WriteFile(file, []byte("user-edited-private-content"), 0600); err != nil {
		t.Fatal(err)
	}
	s, _, _ := testToolsServer(t, f, f.c)
	report := s.runDiagnostic(context.Background(), m.ID, "", false)
	if output := diagnosticStep(t, report, "output"); output.Status != "failed" || output.Kind != "modified" {
		t.Fatal(report)
	}
	if resolve := diagnosticStep(t, report, "resolve"); resolve.Status != "passed" || report.Readable {
		t.Fatal(report)
	}
	bytes, err := os.ReadFile(file)
	if err != nil || string(bytes) != "user-edited-private-content" {
		t.Fatal("diagnosis changed output")
	}
}

func TestDiagnosticRejectsUnknownOptionsAndOutputOwnership(t *testing.T) {
	f := setup(t)
	m := diagnosticMedia(t, f)
	s, h, cookie := testToolsServer(t, f, f.c)
	w := generationPOST(h, cookie, "/api/media/"+m.ID+"/diagnose", object{"url": "http://127.0.0.1/private"})
	if w.Code != 400 {
		t.Fatal("arbitrary URL input accepted")
	}
	report := s.runDiagnostic(context.Background(), m.ID, "../../secret", true)
	if output := diagnosticStep(t, report, "output"); output.Status != "failed" {
		t.Fatal("unowned path accepted", report)
	}
	if resolve := diagnosticStep(t, report, "resolve"); resolve.Status != "skipped" {
		t.Fatal("unowned path accepted", report)
	}
	f.c.target = PlaybackTarget{URL: "http://127.0.0.1:1/private", Mode: "302"}
	report = s.runDiagnostic(context.Background(), m.ID, "", true)
	if report.Readable || diagnosticStep(t, report, "probe").Status != "failed" {
		t.Fatal("private fetch accepted", report)
	}
}

type diagnosisCloud struct {
	Clouds
	resolve func(context.Context, Media) (PlaybackTarget, error)
	list    func(context.Context, string, string) ([]Entry, error)
}

func (c diagnosisCloud) List(ctx context.Context, account, parent string) ([]Entry, error) {
	if c.list != nil {
		return c.list(ctx, account, parent)
	}
	return c.Clouds.List(ctx, account, parent)
}

func (c diagnosisCloud) Resolve(ctx context.Context, m Media) (PlaybackTarget, error) {
	return c.resolve(ctx, m)
}

func TestDiagnosticCancellationStopsResolveAndMarksStaleConfiguration(t *testing.T) {
	f := setup(t)
	m := diagnosticMedia(t, f)
	started, stopped := make(chan struct{}), make(chan struct{})
	c := diagnosisCloud{Clouds: f.c, resolve: func(ctx context.Context, _ Media) (PlaybackTarget, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return PlaybackTarget{}, ctx.Err()
	}}
	s, _, _ := testToolsServer(t, f, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan DiagnosticReport, 1)
	go func() { result <- s.runDiagnostic(ctx, m.ID, "", true) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("resolve not reached")
	}
	a := f.a
	a.Revision++
	if err := f.s.saveAccount(a); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("cancelled request still running")
	}
	report := <-result
	if report.Steps[3].Status != "cancelled" || !report.Stale || report.Steps[4].Status != "skipped" {
		t.Fatal(report)
	}
}

func TestDiagnosticErrorsNeverEchoProviderText(t *testing.T) {
	f := setup(t)
	m := diagnosticMedia(t, f)
	c := diagnosisCloud{Clouds: f.c, resolve: func(context.Context, Media) (PlaybackTarget, error) {
		return PlaybackTarget{}, errors.New("cookie=do-not-export-raw")
	}}
	s, _, _ := testToolsServer(t, f, c)
	report := s.runDiagnostic(context.Background(), m.ID, "", true)
	raw, _ := json.Marshal(report)
	if report.Steps[3].Status != "failed" || strings.Contains(string(raw), "do-not-export") || report.Readable {
		t.Fatal("provider text leaked")
	}
}

func TestLegacyDiagnosisDoesNotPersistLookupEvenWhenConfigurationChanges(t *testing.T) {
	for _, change := range []bool{false, true} {
		f := setup(t)
		m := Media{ID: "legacy-diagnostic", Account: f.a.ID, Legacy: true, Name: "movie.mkv", Path: "/movie.mkv"}
		if err := f.s.saveMedia(m); err != nil {
			t.Fatal(err)
		}
		cloud := diagnosisCloud{Clouds: f.c,
			list: func(context.Context, string, string) ([]Entry, error) {
				if change {
					a := f.a
					a.Revision++
					if err := f.s.saveAccount(a); err != nil {
						t.Fatal(err)
					}
				}
				return []Entry{{ID: "discovered", Name: m.Name, PickCode: "fixture-pick", Size: 123}}, nil
			},
			resolve: func(ctx context.Context, m Media) (PlaybackTarget, error) {
				if m.RemoteID != "discovered" {
					t.Fatal("lookup result not used in memory")
				}
				return f.c.target, nil
			},
		}
		s, _, _ := testToolsServer(t, f, cloud)
		report := s.runDiagnostic(context.Background(), m.ID, "", false)
		if report.Stale != change || report.Steps[3].Status != "passed" {
			t.Fatal(report)
		}
		saved, err := f.s.media(m.ID)
		if err != nil || saved.RemoteID != "" || saved.PickCode != "" || saved.Size != 0 {
			t.Fatal("diagnosis changed legacy index", saved, err)
		}
	}
}

func TestDiagnosticExpectedRevisionStopsProviderBeforeNetwork(t *testing.T) {
	f := setup(t)
	a := f.a
	a.Revision = 2
	if err := f.s.saveAccount(a); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := f.s.observeAccount(a, "resolve", now, now, nil); err != nil {
		t.Fatal(err)
	}
	p := newProviders(f.s)
	p.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("stale diagnostic contacted network")
		return nil, nil
	})
	ctx := context.WithValue(context.Background(), expectedAccountRevisionKey{}, expectedAccountRevision{ID: a.ID, Revision: 1})
	_, err := p.Resolve(ctx, Media{Account: f.a.ID, RemoteID: "movie"})
	if classifyError(err) != "configuration" {
		t.Fatal("expected revision not enforced", err)
	}
	health, err := f.s.accountHealth(a)
	if err != nil || health[1].State != "healthy" || health[1].Checked != now.UnixMilli() {
		t.Fatal("stale request corrupted current health", health, err)
	}
	var n int
	if err = f.s.db.QueryRow("SELECT count(*) FROM notifications").Scan(&n); err != nil || n != 0 {
		t.Fatal("stale request raised health notification", n, err)
	}
}

func TestDiagnosticProbeRejectsXMLErrorsAndInvalidRangeTotals(t *testing.T) {
	for _, tc := range []struct {
		kind, rangeValue     string
		readable, validRange bool
	}{
		{"application/xml", "bytes 0-15/100", false, false},
		{"video/mp4", "bytes 0-15/bad", true, false},
		{"video/mp4", "bytes 0-15/8", true, false},
		{"video/mp4", "bytes 0-15/100", true, true},
	} {
		f := setup(t)
		s, _, _ := testToolsServer(t, f, f.c)
		s.probeClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 206, Header: http.Header{"Content-Type": {tc.kind}, "Content-Range": {tc.rangeValue}}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 16)))}, nil
		})}
		result := s.probeTarget(context.Background(), f.c.target)
		if result.Readable != tc.readable || result.Range != tc.validRange {
			t.Fatal("invalid probe conclusion", tc, result)
		}
	}
}
