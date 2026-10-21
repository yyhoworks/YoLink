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
				for _, scope := range []string{older, latest} {
					f.c.dirs[older], f.c.dirs[latest] = nil, nil
					f.c.dirs[scope] = []Entry{entry}
					j, err := f.e.enqueue(f.r.ID, scope, "/media/"+scope, false, 0)
					if err != nil {
						t.Fatal(err)
					}
					if j = finish(t, f, j); j.Status != "completed" {
						t.Fatal(j)
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
