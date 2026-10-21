package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func strongScanFixture(t *testing.T) *fixture {
	f := setup(t)
	f.r.Incremental = true
	f.c.dirs = map[string][]Entry{"root": {{ID: "first", Name: "first.mkv", Size: 12, Modified: "v1"}, {ID: "second", Name: "second.mkv", Size: 12, Modified: "v1"}}}
	if err := f.s.saveRule(f.r); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestIncrementalScanVerifiesOutputsAndReportsLocalChanges(t *testing.T) {
	f := strongScanFixture(t)
	first := generate(t, f, false, 0)
	if first.Status != "completed" || first.CreatedFiles != 2 {
		t.Fatal(first)
	}
	second := generate(t, f, false, 0)
	if second.Status != "completed" || second.IncrementalSkipped != 2 {
		t.Fatal(second)
	}
	firstPath := filepath.Join(f.output, "test/media/first.strm")
	secondPath := filepath.Join(f.output, "test/media/second.strm")
	if err := os.Remove(firstPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	third := generate(t, f, false, 0)
	if third.Status != "partial" || third.LocalIssues != 2 || third.CreatedFiles != 0 {
		t.Fatal(third)
	}
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatal("ordinary scan repaired without preview", err)
	}
	if raw, _ := os.ReadFile(secondPath); string(raw) != "user edit" {
		t.Fatal("user output changed")
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE run=? AND kind IN ('local_missing','local_modified')", third.ID); n != 2 {
		t.Fatal(n)
	}
	f.c.dirs["root"] = f.c.dirs["root"][:1]
	fourth := generate(t, f, false, 0)
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE run=? AND kind='source_absent'", fourth.ID); n != 1 {
		t.Fatal(n)
	}
	if raw, _ := os.ReadFile(secondPath); string(raw) != "user edit" {
		t.Fatal("source absence removed local output")
	}
}

func TestIncrementalWeakMetadataAndForcedFullDoNotUseFastPath(t *testing.T) {
	for _, mode := range []string{"weak", "full", "periodic"} {
		t.Run(mode, func(t *testing.T) {
			f := strongScanFixture(t)
			generate(t, f, false, 0)
			if mode == "weak" {
				for i := range f.c.dirs["root"] {
					f.c.dirs["root"][i].Modified = ""
				}
			}
			if mode == "periodic" {
				f.s.db.Exec("UPDATE scan_baselines SET full_completed=?", time.Now().Add(-25*time.Hour).Unix())
			}
			j, err := f.e.enqueueJob(f.r.ID, "", "", false, 0, "manual", "", mode == "full")
			if err != nil {
				t.Fatal(err)
			}
			done := finish(t, f, j)
			if done.Status != "completed" || done.IncrementalSkipped != 0 {
				t.Fatal(done)
			}
		})
	}
}

func TestIncrementalScanRejectsConfigEditBeforePublication(t *testing.T) {
	f := strongScanFixture(t)
	generate(t, f, false, 0)
	var prior string
	f.s.db.QueryRow("SELECT run FROM scan_baselines").Scan(&prior)
	j := Job{ID: randomID(), Rule: f.r.ID, Folder: f.r.Folder, SourcePath: f.r.SourcePath, Status: "running", Snapshot: &f.r}
	f.s.saveJob(j)
	f.e.scanFault = func(point string) error {
		if point == "scan.before_publish" {
			return f.s.setSetting("public_base", "https://changed.invalid")
		}
		return nil
	}
	err := f.e.scan(context.Background(), f.a, f.r, &j)
	if err == nil {
		t.Fatal("mixed generation config published")
	}
	var current string
	f.s.db.QueryRow("SELECT run FROM scan_baselines").Scan(&current)
	if current != prior {
		t.Fatal("prior baseline lost")
	}
}

func TestIncrementalCrashChild(t *testing.T) {
	if os.Getenv("YOLINK_INCREMENTAL_CHILD") != "1" {
		t.Skip("isolated child only")
	}
	s, err := openStore(os.Getenv("YOLINK_INCREMENTAL_DATA"))
	if err != nil {
		t.Fatal(err)
	}
	folder := os.Getenv("YOLINK_INCREMENTAL_OUTPUT")
	c := &fixtureCloud{dirs: map[string][]Entry{"root": {{ID: "first", Name: "first.mkv", Size: 12, Modified: "v1"}, {ID: "second", Name: "second.mkv", Size: 12, Modified: "v1"}}}}
	e, err := newEngine(context.Background(), s, c, folder)
	if err != nil {
		t.Fatal(err)
	}
	e.scanFault = func(point string) error {
		if point == os.Getenv("YOLINK_INCREMENTAL_POINT") {
			os.Exit(87)
		}
		return nil
	}
	if _, err = e.enqueue("rule", "", "", false, 0); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestIncrementalAbruptDeathConvergesItemAndBaseline(t *testing.T) {
	for _, point := range []string{"item.after_intent", "item.after_file", "item.after_registration", "item.before_commit", "scan.before_publish", "scan.after_publish"} {
		t.Run(point, func(t *testing.T) {
			f := strongScanFixture(t)
			f.e.Close()
			f.s.db.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestIncrementalCrashChild$", "-test.timeout=20s")
			cmd.Env = append(os.Environ(), "YOLINK_INCREMENTAL_CHILD=1", "YOLINK_INCREMENTAL_DATA="+f.data, "YOLINK_INCREMENTAL_OUTPUT="+f.output, "YOLINK_INCREMENTAL_POINT="+point)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 87 {
				t.Fatalf("did not reach kill phase: %v %s", err, output)
			}
			s, err := openStore(f.data)
			if err != nil {
				t.Fatal(err)
			}
			defer s.db.Close()
			e, err := newEngine(context.Background(), s, &fixtureCloud{dirs: map[string][]Entry{"root": {{ID: "first", Name: "first.mkv", Size: 12, Modified: "v1"}, {ID: "second", Name: "second.mkv", Size: 12, Modified: "v1"}}}}, f.output)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			jobs, err := s.jobs(10)
			if err != nil || len(jobs) != 1 {
				t.Fatal(jobs, err)
			}
			f2 := &fixture{s: s, e: e}
			done := finish(t, f2, jobs[0])
			if done.Status != "completed" || done.Seen != 2 || done.Errors != 0 {
				t.Fatal(done)
			}
			var registrations, activities, baselines int
			s.db.QueryRow("SELECT COUNT(*) FROM outputs").Scan(&registrations)
			s.db.QueryRow("SELECT COUNT(*) FROM activity_events WHERE json_extract(payload,'$.event')='strm'").Scan(&activities)
			s.db.QueryRow("SELECT COUNT(*) FROM scan_baselines").Scan(&baselines)
			if registrations != 2 || activities != 2 || baselines != 1 {
				t.Fatal("replayed or missing durable effects", registrations, activities, baselines)
			}
			if done.CreatedFiles+done.RecoveredFiles != 2 {
				t.Fatal("dishonest replay counters", done)
			}
		})
	}
}

func TestChangeRepairRevalidatesEveryBindingAndPreservesNewFiles(t *testing.T) {
	for _, mutation := range []string{"none", "file", "owner", "hash", "media", "base", "account", "rule", "baseline", "generation", "after-file", "registration"} {
		t.Run(mutation, func(t *testing.T) {
			f := strongScanFixture(t)
			generate(t, f, false, 0)
			srv, _, _ := testToolsServer(t, f, f.c)
			_, _, key, _ := f.e.outputLocation("test/media/first.strm")
			file := filepath.Join(f.output, "test/media/first.strm")
			original, _ := os.ReadFile(file)
			os.Remove(file)
			p, err := srv.previewChangeRepair(t.Context(), []string{key})
			if err != nil || !p.Items[0].Ready {
				t.Fatal(p, err)
			}
			switch mutation {
			case "file":
				os.WriteFile(file, []byte("external replacement"), 0600)
			case "owner":
				f.s.db.Exec("UPDATE outputs SET media='other-owner' WHERE path=?", key)
			case "hash":
				f.s.db.Exec("UPDATE outputs SET hash='other-hash' WHERE path=?", key)
			case "media":
				f.s.db.Exec("UPDATE media SET payload=json_set(payload,'$.name','changed.mkv') WHERE id=?", p.Items[0].Media.ID)
			case "base":
				f.s.setSetting("public_base", "https://changed.invalid")
			case "account":
				a := f.a
				a.Revision++
				f.s.saveAccount(a)
			case "rule":
				r := f.r
				r.CopyNonMedia = true
				f.s.saveRule(r)
			case "baseline":
				generate(t, f, false, 0)
			case "generation":
				f.s.startupRestore.Generation = strings.Repeat("a", 32)
			case "after-file", "registration":
				point := "repair.after_file"
				if mutation == "registration" {
					point = "repair.after_registration"
				}
				f.e.scanFault = func(v string) error {
					if v == point {
						return errors.New("simulated abrupt boundary")
					}
					return nil
				}
			}
			result, err := srv.applyChangeRepair(t.Context(), p.ID, restoreRevision(p))
			if mutation == "after-file" || mutation == "registration" {
				if err == nil {
					t.Fatal("fault did not interrupt repair")
				}
				f.e.scanFault = nil
				result, err = srv.applyChangeRepair(t.Context(), p.ID, restoreRevision(p))
			}
			if err != nil {
				t.Fatal(err)
			}
			if mutation == "none" || mutation == "after-file" || mutation == "registration" {
				if result["done"] != 1 {
					t.Fatal(result)
				}
				if raw, _ := os.ReadFile(file); string(raw) != string(original) {
					t.Fatal("exact bytes not restored")
				}
				second, err := srv.applyChangeRepair(t.Context(), p.ID, restoreRevision(p))
				if err != nil || second["done"] != 1 {
					t.Fatal("replay", second, err)
				}
			} else {
				if result["conflicts"] != 1 {
					t.Fatal("stale repair accepted", result)
				}
				if mutation == "file" {
					if raw, _ := os.ReadFile(file); string(raw) != "external replacement" {
						t.Fatal("external file overwritten")
					}
				} else if _, err = os.Stat(file); !os.IsNotExist(err) {
					t.Fatal("stale repair published", err)
				}
			}
		})
	}
}

func TestRestoredScanAndRepairCannotReplay(t *testing.T) {
	f := strongScanFixture(t)
	generate(t, f, false, 0)
	srv, _, _ := testToolsServer(t, f, f.c)
	_, _, key, _ := f.e.outputLocation("test/media/first.strm")
	os.Remove(filepath.Join(f.output, "test/media/first.strm"))
	p, err := srv.previewChangeRepair(t.Context(), []string{key})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = quarantineBackupPayloads(t.Context(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = srv.applyChangeRepair(t.Context(), p.ID, restoreRevision(p)); err == nil {
		t.Fatal("restored preview executed")
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_baselines"); n != 0 {
		t.Fatal("restored baseline retained authority")
	}
	if _, err = os.Stat(filepath.Join(f.output, "test/media/first.strm")); !os.IsNotExist(err) {
		t.Fatal("restore replay wrote output")
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "fixture-secret") {
		t.Fatal("preview contains credentials")
	}
}
