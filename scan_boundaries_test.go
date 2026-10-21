package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func changeHistoryResult(t *testing.T, f *fixture, query string) object {
	t.Helper()
	s, _, _ := testToolsServer(t, f, f.c)
	w := httptest.NewRecorder()
	s.changeHistory(w, httptest.NewRequest("GET", "/api/changes?"+query, nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result object
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestScopedHistoryUsesAncestorAndKeepsRootFullTime(t *testing.T) {
	f := strongScanFixture(t)
	f.c.dirs = map[string][]Entry{"root": {{ID: "sub", Name: "中文", Dir: true}, {ID: "other", Name: "其他", Dir: true}}, "sub": {{ID: "movie", Name: "movie.mkv", Size: 12, Modified: "v1"}}, "other": {{ID: "outside", Name: "outside.mkv", Size: 12, Modified: "v1"}}}
	root := generate(t, f, false, 0)
	if root.Status != "completed" {
		t.Fatal(root)
	}
	if _, err := f.s.db.Exec("UPDATE scan_baselines SET full_completed=123 WHERE scope='/media'"); err != nil {
		t.Fatal(err)
	}
	f.c.dirs["sub"] = nil
	j, err := f.e.enqueue(f.r.ID, "sub", "/media/中文", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	sub := finish(t, f, j)
	if sub.Status != "completed" {
		t.Fatal(sub)
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE run=? AND remote='movie' AND kind='source_absent'", sub.ID); n != 1 {
		t.Fatal("ancestor fragment not compared", n)
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE run=? AND remote='outside'", sub.ID); n != 0 {
		t.Fatal("subtree asserted outside change", n)
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_baselines WHERE scope='/media' AND run=?", root.ID); n != 1 {
		t.Fatal("subtree retired ancestor")
	}
	result := changeHistoryResult(t, f, "rule=rule&run="+sub.ID)
	if result["last_full"] != float64(123) {
		t.Fatal("subtree advanced root time", result)
	}
	f.c.dirs["sub"] = []Entry{{ID: "movie", Name: "movie.mkv", Size: 12, Modified: "v1"}}
	last := generate(t, f, false, 0)
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE run=? AND remote='movie' AND kind='added'", last.ID); n != 1 {
		t.Fatal("negative Chinese subtree observation lost", n)
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_baselines"); n != 1 {
		t.Fatal("obsolete subtree not retired")
	}
}

func TestPartialHistoryExposesLocalIssuesWithoutAbsenceAuthority(t *testing.T) {
	f := strongScanFixture(t)
	generate(t, f, false, 0)
	os.WriteFile(filepath.Join(f.output, "test/media/first.strm"), []byte("user edit"), 0600)
	os.Remove(filepath.Join(f.output, "test/media/second.strm"))
	j := generate(t, f, false, 0)
	if j.Status != "partial" {
		t.Fatal(j)
	}
	f.s.db.Exec("INSERT INTO scan_changes VALUES(?,'fabricated','source_absent','{}','')", j.ID)
	result := changeHistoryResult(t, f, "rule=rule&run="+j.ID)
	if result["total"] != float64(2) || result["run_status"] != "partial" {
		t.Fatal(result)
	}
	raw, _ := json.Marshal(result["items"])
	if strings.Contains(string(raw), "source_absent") {
		t.Fatal("partial run exposed absence", string(raw))
	}
}

func TestScanHistoryChangedOverlappingPointerRejectsPublication(t *testing.T) {
	f := scanHistoryFixture(t)
	first := scanHistoryStart(t, f, 1, "")
	first.recordListing(t.Context(), ScanDir{ID: "root", Source: "/media"}, nil)
	scanHistoryDone(t, first, "root")
	if err := first.publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	root := scanHistoryStart(t, f, 2, "")
	root.recordListing(t.Context(), ScanDir{ID: "root", Source: "/media"}, nil)
	scanHistoryDone(t, root, "root")
	sub := scanHistoryStart(t, f, 3, "/media/新增")
	sub.recordListing(t.Context(), ScanDir{ID: "root", Source: "/media/新增"}, nil)
	scanHistoryDone(t, sub, "root")
	if err := sub.publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := root.publish(t.Context()); err == nil {
		t.Fatal("root discarded a pointer published after admission")
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_baselines"); n != 2 {
		t.Fatal(n)
	}
}

func TestBackupScanFieldsRejectOversizeAndRequiredEmptyJSON(t *testing.T) {
	for _, field := range []string{"scope", "payload", "plan-payload", "change-payload"} {
		t.Run(field, func(t *testing.T) {
			f := scanHistoryFixture(t)
			session := scanHistoryStart(t, f, 1, "")
			switch field {
			case "scope":
				f.s.db.Exec("UPDATE scan_runs SET scope=?", strings.Repeat("x", 32769))
			case "payload":
				f.s.db.Exec("UPDATE scan_runs SET payload=''")
			case "plan-payload":
				f.s.db.Exec("INSERT INTO change_plans VALUES('bad',0,'preview','')")
			case "change-payload":
				f.s.db.Exec("INSERT INTO scan_changes VALUES(?,'bad','metadata',?,'')", session.Run.ID, strings.Repeat("x", (1<<20)+1))
			}
			if err := validateBackupRows(t.Context(), f.s.db); err == nil {
				t.Fatal("unsafe scan field accepted", field)
			}
		})
	}
}

func TestScanQuotaBudgetsRollbackAndReserveStateSlots(t *testing.T) {
	f := scanHistoryFixture(t)
	session := scanHistoryStart(t, f, 1, "")
	if _, err := f.s.db.Exec("INSERT INTO scan_items VALUES(?,'item','done','{}')", session.Run.ID); err != nil {
		t.Fatal(err)
	}
	var count, bytes int64
	f.s.db.QueryRow("SELECT rows,bytes FROM scan_storage WHERE name='scan_items'").Scan(&count, &bytes)
	// Inject a full budget rather than allocating 128 MiB merely to test the
	// trigger boundary. The post-quarantine counts are then restored and verified.
	if _, err := f.s.db.Exec("UPDATE scan_storage SET rows=?,bytes=? WHERE name='scan_items'", scanTableRows, scanTableBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec("INSERT INTO scan_items VALUES(?,'another','intent','{}')", session.Run.ID); err == nil {
		t.Fatal("full table admitted another row")
	}
	tx, err := f.s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = quarantineBackupPayloads(t.Context(), tx); err != nil {
		tx.Rollback()
		t.Fatal("quarantine consumed more status bytes", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var used int64
	f.s.db.QueryRow("SELECT bytes FROM scan_storage WHERE name='scan_items'").Scan(&used)
	if used != scanTableBytes {
		t.Fatal(used)
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_items"); n != 1 {
		t.Fatal("failed quota insert leaked row")
	}
	f.s.db.Exec("UPDATE scan_storage SET rows=?,bytes=? WHERE name='scan_items'", count, bytes)
	if err = validateBackupScanRows(t.Context(), f.s.db); err != nil {
		t.Fatal("valid counts drifted", err)
	}
}

func TestRepairReplayRejectsChangedOwnerEvenWithExactPostBytes(t *testing.T) {
	f := strongScanFixture(t)
	generate(t, f, false, 0)
	s, _, _ := testToolsServer(t, f, f.c)
	_, _, key, _ := f.e.outputLocation("test/media/first.strm")
	file := filepath.Join(f.output, "test/media/first.strm")
	original, _ := os.ReadFile(file)
	os.Remove(file)
	p, err := s.previewChangeRepair(t.Context(), []string{key})
	if err != nil {
		t.Fatal(err)
	}
	f.e.scanFault = func(point string) error {
		if point == "repair.after_file" {
			return os.ErrDeadlineExceeded
		}
		return nil
	}
	if _, err = s.applyChangeRepair(t.Context(), p.ID, restoreRevision(p)); err == nil {
		t.Fatal("interruption missing")
	}
	f.e.scanFault = nil
	f.s.db.Exec("UPDATE outputs SET media='new-owner' WHERE path=?", key)
	result, err := s.applyChangeRepair(t.Context(), p.ID, restoreRevision(p))
	if err != nil || result["conflicts"] != 1 || result["done"] != 0 {
		t.Fatal(result, err)
	}
	if raw, _ := os.ReadFile(file); string(raw) != string(original) {
		t.Fatal("post file altered")
	}
}

func TestRepairPreviewRespectsCurrentMediaFilter(t *testing.T) {
	f := strongScanFixture(t)
	generate(t, f, false, 0)
	f.r.Filter.Preset = "audio"
	if err := f.s.saveRule(f.r); err != nil {
		t.Fatal(err)
	}
	if j := generate(t, f, false, 0); j.Status != "completed" {
		t.Fatal(j)
	}
	s, _, _ := testToolsServer(t, f, f.c)
	_, _, key, _ := f.e.outputLocation("test/media/first.strm")
	os.Remove(filepath.Join(f.output, "test/media/first.strm"))
	p, err := s.previewChangeRepair(t.Context(), []string{key})
	if err != nil || p.Items[0].Ready || !strings.Contains(p.Items[0].Note, "排除") {
		t.Fatal(p, err)
	}
}
