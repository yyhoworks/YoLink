package main

import (
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mustSucceed fails the test immediately when a fixture-setup call errors,
// so setup failures surface at their cause instead of as a misleading
// assertion failure later.
func mustSucceed(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// mustExec is mustSucceed for a setup-time SQL statement.
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

// testServerWithStore is testToolsServer with an explicit Store, for tests
// that need a Server bound to a Store other than the fixture's own (for
// example a shell Store that only carries a data directory for the restore
// status handler). The login still happens against the fixture's data dir.
func testServerWithStore(t *testing.T, store *Store, f *fixture, c Clouds) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "fixture-admin-password")
	s, e := newServer(store, f.e, c, f.data, f.output)
	if e != nil {
		t.Fatal(e)
	}
	h := s.handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"fixture-admin-password"}`)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return s, h, w.Result().Cookies()[0]
}

// runReviewFixture serves a manual, human-driven UI review fixture on an
// ephemeral loopback port (never a shared fixed port, so fixtures cannot
// collide with each other or with a developer's service). It blocks until
// the 15-minute review window expires; a listen failure is fatal rather
// than silently ignored. These fixtures exist only for eyeballing the UI —
// they assert nothing and are all gated behind opt-in environment flags.
func runReviewFixture(t *testing.T, handler http.Handler, note string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	timer := time.AfterFunc(15*time.Minute, func() { server.Close() })
	defer timer.Stop()
	t.Cleanup(func() { server.Close() })
	t.Logf("manual review fixture http://%s (%s); auto-exits after 15m", ln.Addr(), note)
	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

// incrementalScanFixture is the shared base for the incremental-scan
// fixtures: a setup fixture whose rule is incremental, optionally with the
// cloud directory listing replaced.
func incrementalScanFixture(t *testing.T, dirs map[string][]Entry) *fixture {
	t.Helper()
	f := setup(t)
	f.r.Incremental = true
	if dirs != nil {
		f.c.dirs = dirs
	}
	if err := f.s.saveRule(f.r); err != nil {
		t.Fatal(err)
	}
	return f
}

func scanHistoryFixture(t *testing.T) *fixture {
	t.Helper()
	return incrementalScanFixture(t, nil)
}

func strongScanFixture(t *testing.T) *fixture {
	t.Helper()
	return incrementalScanFixture(t, map[string][]Entry{"root": {{ID: "first", Name: "first.mkv", Size: 12, Modified: "v1"}, {ID: "second", Name: "second.mkv", Size: 12, Modified: "v1"}}})
}

// diagnosticStep returns the diagnostic step with the given stable ID,
// failing the test if the report does not contain it. Tests must address
// steps by ID, not by position, so adding or reordering a step cannot
// silently retarget an assertion.
func diagnosticStep(t *testing.T, report DiagnosticReport, id string) DiagnosticStep {
	t.Helper()
	for _, step := range report.Steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("diagnostic step %q missing from report %+v", id, report)
	return DiagnosticStep{}
}
