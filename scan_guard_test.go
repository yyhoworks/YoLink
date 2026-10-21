package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChangeRepairUsesLatestSizeAndOriginalFilterPath(t *testing.T) {
	for _, mode := range []string{"latest-size", "raw-name", "raw-name-sanitized-parent"} {
		t.Run(mode, func(t *testing.T) {
			f := strongScanFixture(t)
			name, relative := "movie.mkv", "movie.strm"
			if mode != "latest-size" {
				name, relative = "movie:part.mkv", "movie_part.strm"
			}
			entry := Entry{ID: "movie", Name: name, Size: 4 << 20, Modified: "v1"}
			folder := "root"
			f.c.dirs = map[string][]Entry{"root": {entry}}
			if mode == "raw-name-sanitized-parent" {
				folder, relative = "sub", "dir_raw/"+relative
				f.c.dirs = map[string][]Entry{"root": {{ID: "sub", Name: "dir:raw", Dir: true}}, "sub": {entry}}
			}
			if j := generate(t, f, false, 0); j.Status != "completed" || j.CreatedFiles != 1 {
				t.Fatal(j)
			}
			file := filepath.Join(f.output, "test/media", relative)
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if mode == "latest-size" {
				f.c.dirs[folder][0].Size = 1 << 20
				f.r.Filter.MinBytes = 2 << 20
			} else {
				f.r.Filter.Pattern = "movie:.*"
				if mode == "raw-name-sanitized-parent" {
					f.r.Filter.Pattern = "dir_raw/movie:.*"
				}
			}
			if err := f.s.saveRule(f.r); err != nil {
				t.Fatal(err)
			}
			if j := generate(t, f, false, 0); j.Status != "completed" || j.Filtered != 1 {
				t.Fatal(j)
			}
			srv, _, _ := testToolsServer(t, f, f.c)
			_, _, key, _ := f.e.outputLocation("test/media/" + relative)
			p, err := srv.previewChangeRepair(t.Context(), []string{key})
			if err != nil || len(p.Items) != 1 || p.Items[0].Ready || !strings.Contains(p.Items[0].Note, "排除") {
				t.Fatal("filtered media became repairable", p, err)
			}
			if _, err := os.Stat(file); !os.IsNotExist(err) {
				t.Fatal("preview wrote output", err)
			}
		})
	}
}

func TestScanRuleLifecycleRevokesPointersAndCollectsOnlyUnreferencedHistory(t *testing.T) {
	for _, mode := range []string{"delete", "disjoint-root", "narrow-root", "output-mapping", "orphan-gc"} {
		t.Run(mode, func(t *testing.T) {
			f := strongScanFixture(t)
			initial := generate(t, f, false, 0)
			keepFile := filepath.Join(f.output, "test/media/second.strm")
			keepBytes, err := os.ReadFile(keepFile)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(filepath.Join(f.output, "test/media/first.strm")); err != nil {
				t.Fatal(err)
			}
			srv, _, _ := testToolsServer(t, f, f.c)
			_, _, key, _ := f.e.outputLocation("test/media/first.strm")
			preview, err := srv.previewChangeRepair(t.Context(), []string{key})
			if err != nil || !preview.Items[0].Ready {
				t.Fatal(preview, err)
			}
			// Scheduler timestamp updates preserve baseline authority.
			r := f.r
			r.LastRun = time.Now().Unix()
			if err = f.s.saveRule(r); err != nil {
				t.Fatal(err)
			}
			if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_baselines"); n != 1 {
				t.Fatal("timestamp update revoked baseline", n)
			}
			switch mode {
			case "delete":
				req := httptest.NewRequest("DELETE", "/api/rules/rule", nil)
				req.SetPathValue("id", f.r.ID)
				w := httptest.NewRecorder()
				srv.removeRule(w, req)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
			case "orphan-gc":
				_, err = f.s.db.Exec("DELETE FROM rules WHERE id=?", r.ID)
			default:
				if mode == "disjoint-root" {
					r.SourcePath = "/other"
				} else if mode == "narrow-root" {
					r.SourcePath = "/media/A"
				} else {
					r.Output = "other/library"
				}
				err = f.s.saveRule(r)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Explicit pressure injection tests policy, not a 128 MiB load.
			if _, err = f.s.db.Exec("UPDATE scan_storage SET rows=? WHERE name='scan_entries'", scanTableRows/2+1); err != nil {
				t.Fatal(err)
			}
			prune := func() {
				t.Helper()
				tx, er := f.s.db.Begin()
				if er != nil {
					t.Fatal(er)
				}
				defer tx.Rollback()
				if er = pruneScanHistoryTx(tx); er != nil {
					t.Fatal(er)
				}
				if er = tx.Commit(); er != nil {
					t.Fatal(er)
				}
			}
			prune()
			if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_baselines"); n != 0 {
				t.Fatal("obsolete pointer retained", n)
			}
			if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_runs WHERE id=?", initial.ID); n != 1 {
				t.Fatal("live preview reference collected", n)
			}
			if _, err = f.s.db.Exec("DELETE FROM change_plans WHERE id=?", preview.ID); err != nil {
				t.Fatal(err)
			}
			prune()
			if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_runs WHERE id=?", initial.ID); n != 0 {
				t.Fatal("obsolete run cannot be collected", n)
			}
			if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM media"); n != 2 {
				t.Fatal("media changed", n)
			}
			if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM outputs"); n != 2 {
				t.Fatal("registrations changed", n)
			}
			if raw, er := os.ReadFile(keepFile); er != nil || string(raw) != string(keepBytes) {
				t.Fatal("output changed", er)
			}
		})
	}
}

func TestScanWithoutBaselineCannotBypassControlledRepair(t *testing.T) {
	for _, mode := range []string{"limited", "retry", "legacy"} {
		for _, state := range []string{"missing", "modified", "outdated"} {
			t.Run(mode+"/"+state, func(t *testing.T) {
				f := strongScanFixture(t)
				generate(t, f, false, 0)
				file := filepath.Join(f.output, "test/media/first.strm")
				original, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				var mediaBefore, ownerBefore, hashBefore string
				_, _, key, err := f.e.outputLocation("test/media/first.strm")
				if err != nil {
					t.Fatal(err)
				}
				if err = f.s.db.QueryRow("SELECT m.payload,o.media,o.hash FROM outputs o JOIN media m ON m.id=o.media WHERE o.path=?", key).Scan(&mediaBefore, &ownerBefore, &hashBefore); err != nil {
					t.Fatal(err)
				}
				if state == "missing" {
					err = os.Remove(file)
				} else if state == "modified" {
					original = []byte("external user change")
					err = os.WriteFile(file, original, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = f.s.setSetting("public_base", "https://changed.invalid"); err != nil {
					t.Fatal(err)
				}
				j := Job{ID: randomID(), Rule: f.r.ID, Folder: f.r.Folder, SourcePath: f.r.SourcePath, Status: "running", Snapshot: &f.r}
				if mode == "limited" {
					j.Limit = 1
				} else if mode == "retry" {
					j.RetryDirs = []ScanDir{{ID: "root", Source: "/media"}}
				}
				if err = f.s.saveJob(j); err != nil {
					t.Fatal(err)
				}
				if mode == "legacy" {
					cp := ScanCheckpoint{Todo: []ScanDir{{ID: "root", Source: "/media"}}, Visited: map[string]bool{}, Claimed: map[string]string{}}
					raw, _ := json.Marshal(cp)
					if _, err = f.s.db.Exec("INSERT INTO job_checkpoints VALUES(?,?)", j.ID, string(raw)); err != nil {
						t.Fatal(err)
					}
				}
				if err = f.e.scan(t.Context(), f.a, f.r, &j); err != nil {
					t.Fatal(err)
				}
				if j.CreatedFiles != 0 || j.LocalIssues < 1 {
					t.Fatal("ordinary generation bypassed confirmation", j)
				}
				if state == "missing" {
					if _, err = os.Stat(file); !os.IsNotExist(err) {
						t.Fatal("registered missing output recreated", err)
					}
				} else if raw, er := os.ReadFile(file); er != nil || string(raw) != string(original) {
					t.Fatal("existing output modified", er)
				}
				var mediaAfter, ownerAfter, hashAfter string
				if err = f.s.db.QueryRow("SELECT m.payload,o.media,o.hash FROM outputs o JOIN media m ON m.id=o.media WHERE o.path=?", key).Scan(&mediaAfter, &ownerAfter, &hashAfter); err != nil || mediaAfter != mediaBefore || ownerAfter != ownerBefore || hashAfter != hashBefore {
					t.Fatal("preserved registration/index changed", err)
				}
				if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_runs WHERE id=?", j.ID); n != 0 {
					t.Fatal("ineligible scan gained baseline authority", n)
				}
				if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM activity_events WHERE json_extract(payload,'$.job')=? AND json_extract(payload,'$.note') LIKE '%未重建%'", j.ID); n < 1 {
					t.Fatal("preservation activity missing", n)
				}
			})
		}
	}
}

func TestScanHistorySiblingMovesUseLatestCoveredObservation(t *testing.T) {
	for _, latest := range []string{"A", "Z"} {
		for _, present := range []bool{true, false} {
			t.Run(latest+"/present="+map[bool]string{true: "yes", false: "no"}[present], func(t *testing.T) {
				f := strongScanFixture(t)
				older := "A"
				if latest == "A" {
					older = "Z"
				}
				f.c.dirs = map[string][]Entry{"root": {{ID: "A", Name: "A", Dir: true}, {ID: "Z", Name: "Z", Dir: true}}, "A": {}, "Z": {}}
				if j := generate(t, f, false, 0); j.Status != "completed" {
					t.Fatal(j)
				}
				entry := Entry{ID: "movie", Name: "movie.mkv", Size: 12, Modified: "v1"}
				for step, scope := range []string{older, latest} {
					f.c.dirs[older], f.c.dirs[latest] = nil, nil
					f.c.dirs[scope] = []Entry{entry}
					j, err := f.e.enqueue(f.r.ID, scope, "/media/"+scope, false, 0)
					if err != nil {
						t.Fatal(err)
					}
					if j = finish(t, f, j); j.Status != "completed" {
						t.Fatal(j)
					}
					// Simulate NAS/NTP wall-clock rollback without changing the
					// host clock. Publication order must remain authoritative.
					if _, err = f.s.db.Exec("UPDATE scan_runs SET completed=? WHERE id=?", 3000000000-step*1000000000, j.ID); err != nil {
						t.Fatal(err)
					}
				}
				if !present {
					f.c.dirs[latest] = nil
				}
				root := generate(t, f, false, 0)
				if root.Status != "completed" {
					t.Fatal(root)
				}
				if present {
					if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE run=? AND remote='movie'", root.ID); n != 0 {
						t.Fatal("older sibling caused a false move", n)
					}
				} else {
					var source string
					if err := f.s.db.QueryRow("SELECT json_extract(before_payload,'$.source') FROM scan_changes WHERE run=? AND remote='movie' AND kind='source_absent'", root.ID).Scan(&source); err != nil || source != "/media/"+latest+"/movie.mkv" {
						t.Fatal("absence used an older sibling observation", source, err)
					}
				}
			})
		}
	}
}

func TestChangeRepairRejectsOlderPathAfterSiblingMoveAndMissingOutput(t *testing.T) {
	f := strongScanFixture(t)
	entry := Entry{ID: "movie", Name: "movie.mkv", Size: 12, Modified: "v1"}
	f.c.dirs = map[string][]Entry{"root": {{ID: "A", Name: "A", Dir: true}, {ID: "Z", Name: "Z", Dir: true}}, "A": {}, "Z": {entry}}
	if j := generate(t, f, false, 0); j.Status != "completed" {
		t.Fatal(j)
	}
	scan := func(scope string) Job {
		t.Helper()
		j, err := f.e.enqueue(f.r.ID, scope, "/media/"+scope, false, 0)
		if err != nil {
			t.Fatal(err)
		}
		j = finish(t, f, j)
		if j.Status != "completed" {
			t.Fatal(j)
		}
		return j
	}
	f.c.dirs["A"], f.c.dirs["Z"] = []Entry{entry}, nil
	scan("A")
	if err := os.Remove(filepath.Join(f.output, "test/media/Z/movie.strm")); err != nil {
		t.Fatal(err)
	}
	f.c.dirs["A"], f.c.dirs["Z"] = nil, []Entry{entry}
	if j := scan("Z"); j.LocalIssues != 1 || j.CreatedFiles != 0 {
		t.Fatal("missing output was not preserved", j)
	}
	file := filepath.Join(f.output, "test/media/A/movie.strm")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	srv, _, _ := testToolsServer(t, f, f.c)
	_, _, key, _ := f.e.outputLocation("test/media/A/movie.strm")
	p, err := srv.previewChangeRepair(t.Context(), []string{key})
	if err != nil || len(p.Items) != 1 || p.Items[0].Ready || !strings.Contains(p.Items[0].Note, "最新完整观测") {
		t.Fatal("older sibling path became repairable", p, err)
	}
	if _, err = os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("preview recreated moved source", err)
	}
	// Replacing the newest positive scope with repeated empty snapshots must
	// not revive the older sibling's positive observation.
	f.c.dirs["Z"] = nil
	for i := 0; i < 2; i++ {
		empty := scan("Z")
		if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE run=? AND kind='source_tombstone'", empty.ID); n != 1 {
			t.Fatal("retired identity was not carried", n)
		}
		result := changeHistoryResult(t, f, "rule=rule&run="+empty.ID)
		if raw, er := json.Marshal(result); er != nil || strings.Contains(string(raw), "source_tombstone") || i == 1 && result["total"] != float64(0) {
			t.Fatal("internal absence entered history/pagination", result, er)
		}
		p, err = srv.previewChangeRepair(t.Context(), []string{key})
		if err != nil || p.Items[0].Ready {
			t.Fatal("empty scope resurrected the older sibling", i, p, err)
		}
	}
	root := generate(t, f, false, 0)
	if root.Status != "completed" {
		t.Fatal(root)
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE run=? AND remote='movie' AND kind='source_absent'", root.ID); n != 0 {
		t.Fatal("root repeated absence using a superseded sibling", n)
	}
}

func TestScanTombstonesPreserveNewerSiblingAndDirectoryRetirement(t *testing.T) {
	for _, mode := range []string{"newer-sibling", "directory-retired"} {
		t.Run(mode, func(t *testing.T) {
			f := strongScanFixture(t)
			entry := Entry{ID: "movie", Name: "movie.mkv", Size: 12, Modified: "v1"}
			f.c.dirs = map[string][]Entry{"root": {{ID: "A", Name: "A", Dir: true}, {ID: "Z", Name: "Z", Dir: true}}, "A": {}, "Z": {entry}}
			if mode == "directory-retired" {
				f.c.dirs["A"], f.c.dirs["Z"] = []Entry{entry}, nil
			}
			generate(t, f, false, 0)
			scan := func(scope string) Job {
				t.Helper()
				j, err := f.e.enqueue(f.r.ID, scope, "/media/"+scope, false, 0)
				if err != nil {
					t.Fatal(err)
				}
				j = finish(t, f, j)
				if j.Status != "completed" {
					t.Fatal(j)
				}
				return j
			}
			if mode == "newer-sibling" {
				f.c.dirs["A"], f.c.dirs["Z"] = []Entry{entry}, nil
				scan("A")
			} else {
				directory := entry
				directory.Dir = true
				f.c.dirs["A"], f.c.dirs["Z"] = nil, []Entry{directory}
				scan("Z")
				f.c.dirs["Z"] = nil
			}
			scan("Z")
			scan("Z")
			if err := os.Remove(filepath.Join(f.output, "test/media/A/movie.strm")); err != nil {
				t.Fatal(err)
			}
			srv, _, _ := testToolsServer(t, f, f.c)
			_, _, key, _ := f.e.outputLocation("test/media/A/movie.strm")
			preview, err := srv.previewChangeRepair(t.Context(), []string{key, key})
			wantReady := mode == "newer-sibling"
			if err != nil || len(preview.Items) < 1 || preview.Items[0].Ready != wantReady {
				t.Fatal("tombstone authority used the empty run instead of retired positive", preview, err)
			}
			// A fresh positive observation supersedes a prior absence normally.
			if mode == "directory-retired" {
				f.c.dirs["A"] = []Entry{entry}
				scan("A")
				preview, err = srv.previewChangeRepair(t.Context(), []string{key})
				if err != nil || !preview.Items[0].Ready {
					t.Fatal("fresh positive remained suppressed", preview, err)
				}
			}
			root := generate(t, f, false, 0)
			if root.Status != "completed" {
				t.Fatal(root)
			}
			if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE run=? AND kind='source_tombstone'", root.ID); n != 0 {
				t.Fatal("root retained unnecessary tombstones", n)
			}
		})
	}
}

func TestScanTombstonePublicationQuotaFailureRollsBack(t *testing.T) {
	f := scanHistoryFixture(t)
	positive := scanHistoryStart(t, f, 1, "/media/Z")
	d := ScanDir{ID: "root", Source: "/media/Z"}
	if err := positive.recordListing(t.Context(), d, []Entry{{ID: "movie", Name: "movie.mkv"}}); err != nil {
		t.Fatal(err)
	}
	scanHistoryDone(t, positive, "root", "movie")
	if err := positive.publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	empty := scanHistoryStart(t, f, 2, "/media/Z")
	if err := empty.recordListing(t.Context(), d, nil); err != nil {
		t.Fatal(err)
	}
	scanHistoryDone(t, empty, "root")
	// Leave room for source_absent, then reject the internal tombstone via
	// the actual run-byte CHECK. Counter injection is not a load benchmark.
	var oldObservation string
	if err := f.s.db.QueryRow("SELECT payload FROM scan_entries WHERE run=? AND remote='movie'", positive.Run.ID).Scan(&oldObservation); err != nil {
		t.Fatal(err)
	}
	absenceBytes := len(empty.Run.ID) + len("movie") + len("source_absent") + len(oldObservation)
	if _, err := f.s.db.Exec("UPDATE scan_runs SET bytes=? WHERE id=?", scanRunBytes-absenceBytes-1, empty.Run.ID); err != nil {
		t.Fatal(err)
	}
	if err := empty.publish(t.Context()); err == nil {
		t.Fatal("over-quota publication succeeded")
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_baselines WHERE run=?", positive.Run.ID); n != 1 {
		t.Fatal("quota failure lost old pointer", n)
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE run=?", empty.Run.ID); n != 0 {
		t.Fatal("failed publication leaked partial changes", n)
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM sqlite_temp_master WHERE name IN ('scan_compare','scan_tombstones')"); n != 0 {
		t.Fatal("rolled back temporary comparison survived", n)
	}
}
