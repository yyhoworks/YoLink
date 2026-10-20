package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackupCanonicalQuarantineUsesStartupFieldSemantics(t *testing.T) {
	_, _, dir := backupFixture(t)
	db, err := sql.Open("sqlite", filepath.Join(dir, "strmlink.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE rules SET payload='{"ID":"rule","ENABLED":true,"account":"account"}'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 35; i++ {
		id := fmt.Sprintf("job-%02d", i)
		_, err = db.Exec("INSERT INTO jobs(id,created,status,payload) VALUES(?,1,'queued',?)", id, fmt.Sprintf(`{"ID":%q,"STATUS":"queued","RESUMABLE":true}`, id))
		if err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if _, err = validateBackupSnapshot(context.Background(), dir, snapshotManifestForTest(t, dir)); err != nil {
		t.Fatal(err)
	}
	if err = pauseRestoredWork(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	r, err := s.rule("rule")
	if err != nil || r.Enabled {
		t.Fatal("folded rule remained enabled", err)
	}
	jobs, err := s.jobs(100)
	if err != nil || len(jobs) != 36 { // fixture also contains its completed generation job
		t.Fatal("lost jobs", err)
	}
	for _, j := range jobs {
		if !strings.HasPrefix(j.ID, "job-") {
			if j.Status != "completed" || j.Resumable {
				t.Fatal("completed history changed", j.ID)
			}
			continue
		}
		if j.Status != "interrupted" || j.Resumable {
			t.Fatal("folded job replayed", j.ID)
		}
	}
}

type backupDenyTransport struct{ calls atomic.Int64 }

func (d *backupDenyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	d.calls.Add(1)
	return nil, errors.New("isolated drill denies outbound HTTP")
}

type backupDenyCloud struct{ calls atomic.Int64 }

func (d *backupDenyCloud) List(context.Context, string, string) ([]Entry, error) {
	d.calls.Add(1)
	return nil, errors.New("isolated drill denies providers")
}
func (d *backupDenyCloud) Resolve(context.Context, Media) (PlaybackTarget, error) {
	d.calls.Add(1)
	return PlaybackTarget{}, errors.New("isolated drill denies providers")
}

// Separate process: no inherited Store/engine/cache/test fixture globals. HTTP
// transports deny all outbound requests and count attempts. This is not an OS
// network namespace test; the Linux --network=none release drill is separate.
func TestBackupIsolatedStartupHelper(t *testing.T) {
	if os.Getenv("YOLINK_RESTORE_DRILL") != "1" {
		t.Skip("subprocess only")
	}
	args := os.Args
	dir, output := args[len(args)-2], args[len(args)-1]
	transport := &backupDenyTransport{}
	http.DefaultTransport = transport
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if !s.restoreRestricted {
		t.Fatal("restore gate not loaded")
	}
	cloud := &backupDenyCloud{}
	e, err := newEngine(context.Background(), s, cloud, output)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	server, err := newServer(s, e, cloud, dir, output)
	if err != nil {
		t.Fatal(err)
	}
	if !server.passwordMatches("fixture-admin-password") {
		t.Fatal("login verifier changed")
	}
	w := httptest.NewRecorder()
	server.handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"fixture-admin-password"}`)))
	if w.Code != 200 || len(w.Result().Cookies()) == 0 {
		t.Fatal("restored login rejected", w.Code)
	}
	if _, err = e.enqueue("rule", "", "", false, 0); err == nil {
		t.Fatal("restore gate allowed queue")
	}
	e.run("residual")
	e.processIngest()
	e.processDeliveries()
	w = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/events", strings.NewReader(`{"rule":"rule"}`))
	req.Header.Set("Authorization", "Bearer "+server.eventToken)
	server.handler().ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatal("event gate missing", w.Code)
	}
	// Exercise notification/ingest timer periods if a worker starts by mistake.
	timer := time.NewTimer(2200 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if transport.calls.Load() != 0 || cloud.calls.Load() != 0 {
		t.Fatal("restore attempted network/provider work")
	}
	j, err := s.job("residual")
	if err != nil || j.Status != "queued" || j.Resumes != 0 {
		t.Fatal("startup replay changed historical job", err)
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 1 || entries[0].Name() != "user-edited.strm" {
		t.Fatal("restore wrote outputs", err)
	}
	data, err := os.ReadFile(filepath.Join(output, "user-edited.strm"))
	if err != nil || string(data) != "user-edited-preserve\n" {
		t.Fatal("user output overwritten", err)
	}
	a, err := s.account("account")
	if err != nil || a.Secrets["authorization"] != "fixture-secret" {
		t.Fatal("credentials changed", err)
	}
	if s.signature("same-media") != os.Getenv("YOLINK_DRILL_SIGNATURE") {
		t.Fatal("signing key changed")
	}
}

func TestBackupIsolatedStartupDrillAndRepeatedRestart(t *testing.T) {
	f, _, dir := backupFixture(t)
	originalKey, err := os.ReadFile(filepath.Join(f.data, "credentials.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err = pauseRestoredWork(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	// Deliberately insert residual work AFTER quarantine. The durable startup
	// gate must suppress it independently of the rewritten row states.
	db, err := sql.Open("sqlite", filepath.Join(dir, "strmlink.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO jobs(id,created,status,payload) VALUES('residual',1,'queued','{"id":"residual","rule":"rule","status":"queued","resumable":true}');
UPDATE rules SET payload=json_set(payload,'$.enabled',json('true'),'$.interval',60,'$.last_run',0);
INSERT INTO ingest_events VALUES('residual','rule','/media','pending','','',1,1);
INSERT INTO deliveries(id,job,kind,status,next_at,payload) VALUES('residual','residual','library','pending',1,'{}');
UPDATE settings SET value='1' WHERE id='event_legacy_enabled';`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	if err = os.WriteFile(filepath.Join(output, "user-edited.strm"), []byte("user-edited-preserve\n"), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestBackupIsolatedStartupHelper$", "--", dir, output)
		cmd.Env = append(os.Environ(), "YOLINK_RESTORE_DRILL=1", "YOLINK_DRILL_SIGNATURE="+f.s.signature("same-media"))
		result, e := cmd.CombinedOutput()
		cancel()
		if e != nil {
			t.Fatalf("isolated restart %d failed: %v\n%s", i, e, result)
		}
	}
	key, err := os.ReadFile(filepath.Join(f.data, "credentials.key"))
	if err != nil || !bytes.Equal(key, originalKey) {
		t.Fatal("original key changed")
	}
	r, err := f.s.rule(f.r.ID)
	if err != nil || r.Enabled != f.r.Enabled {
		t.Fatal("original rules changed")
	}
}

func TestBackupRejectsInvalidRestoreGateAtStartup(t *testing.T) {
	_, _, dir := backupFixture(t)
	db, err := sql.Open("sqlite", filepath.Join(dir, "strmlink.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO settings(id,value) VALUES('restore_review_required','maybe')")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err := openStore(dir)
	if err == nil {
		s.db.Close()
		t.Fatal("invalid restore gate allowed startup")
	}
}
