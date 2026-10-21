package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func scanHistoryStart(t *testing.T, f *fixture, n int, scope string) *scanSession {
	t.Helper()
	if scope == "" {
		scope = f.r.SourcePath
	}
	session, err := f.s.startScanHistory(context.Background(), Job{ID: fmt.Sprintf("%032x", n), Rule: f.r.ID, Folder: f.r.Folder, SourcePath: scope}, f.r, "fixture-output")
	if err != nil || session == nil {
		t.Fatal("start scan history", err)
	}
	return session
}
func scanHistoryDone(t *testing.T, session *scanSession, ids ...string) {
	t.Helper()
	tx, err := session.s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		if err = markScanObservationTx(tx, session.Run.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
func scanHistoryCount(t *testing.T, f *fixture, query string, args ...any) int {
	t.Helper()
	var count int
	if err := f.s.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestScanHistoryRejectsRemoteIdentityAliasingBeforeCompletion(t *testing.T) {
	f := scanHistoryFixture(t)
	session := scanHistoryStart(t, f, 1, "")
	if err := session.recordListing(t.Context(), ScanDir{ID: "root", Source: "/media"}, []Entry{{ID: "a", Name: "A", Dir: true}, {ID: "b", Name: "B", Dir: true}}); err != nil {
		t.Fatal(err)
	}
	if err := session.recordListing(t.Context(), ScanDir{ID: "a", Source: "/media/A"}, []Entry{{ID: "same", Name: "movie.mkv"}}); err != nil {
		t.Fatal(err)
	}
	if err := session.recordListing(t.Context(), ScanDir{ID: "b", Source: "/media/B"}, []Entry{{ID: "same", Name: "movie.mkv"}}); err == nil {
		t.Fatal("same identity accepted at two paths")
	}
	if err := session.publish(t.Context()); err == nil {
		t.Fatal("partial scan became baseline")
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_baselines"); n != 0 {
		t.Fatal("aliasing scan published", n)
	}
}

func TestScanHistoryOnlyCompleteCoveredRunsCanAssertAbsence(t *testing.T) {
	f := scanHistoryFixture(t)
	first := scanHistoryStart(t, f, 1, "")
	file := Entry{ID: "movie", Name: "movie.mkv", Size: 10, Modified: "v1"}
	d := ScanDir{ID: "root", Source: "/media"}
	if err := first.recordListing(t.Context(), d, []Entry{file}); err != nil {
		t.Fatal(err)
	}
	scanHistoryDone(t, first, "movie", "root")
	if err := first.publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	partial := scanHistoryStart(t, f, 2, "")
	if err := partial.publish(t.Context()); err == nil {
		t.Fatal("unlisted root became a complete scan")
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE kind='source_absent'"); n != 0 {
		t.Fatal("partial run asserted source absence")
	}
	scoped := scanHistoryStart(t, f, 3, "/media/subtree")
	if err := scoped.recordListing(t.Context(), ScanDir{ID: "root", Source: "/media/subtree"}, nil); err != nil {
		t.Fatal(err)
	}
	scanHistoryDone(t, scoped, "root")
	if err := scoped.publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE kind='source_absent'"); n != 0 {
		t.Fatal("different scope asserted root absence")
	}
	final := scanHistoryStart(t, f, 4, "")
	if err := final.recordListing(t.Context(), d, []Entry{}); err != nil {
		t.Fatal(err)
	}
	scanHistoryDone(t, final, "root")
	if err := final.publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE run=? AND kind='source_absent'", final.Run.ID); n != 1 {
		t.Fatal("complete empty listing did not report missing source", n)
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_baselines"); n != 1 {
		t.Fatal("full-root completion left obsolete subtree pointers", n)
	}
}

func TestScanHistoryReplayAndChangedInputsKeepLastBaseline(t *testing.T) {
	for _, change := range []string{"base", "account", "rule", "generation"} {
		t.Run(change, func(t *testing.T) {
			f := scanHistoryFixture(t)
			first := scanHistoryStart(t, f, 1, "")
			file := Entry{ID: "movie", Name: "movie.mkv", Size: 10, Modified: "v1"}
			d := ScanDir{ID: "root", Source: "/media"}
			if err := first.recordListing(t.Context(), d, []Entry{file}); err != nil {
				t.Fatal(err)
			}
			scanHistoryDone(t, first, "movie", "root")
			if err := first.publish(t.Context()); err != nil {
				t.Fatal(err)
			}
			second := scanHistoryStart(t, f, 2, "")
			for i := 0; i < 2; i++ {
				if err := second.recordListing(t.Context(), d, []Entry{file}); err != nil {
					t.Fatal("identical replay", err)
				}
			}
			scanHistoryDone(t, second, "movie", "root")
			reopened := scanHistoryStart(t, f, 2, "")
			if reopened.Complete {
				t.Fatal("unfinished run marked complete")
			}
			unchanged, err := reopened.unchanged(t.Context(), file, "/media/movie.mkv")
			if err != nil || !unchanged {
				t.Fatal("completed observation not reusable", err)
			}
			switch change {
			case "base":
				if err = f.s.setSetting("public_base", "http://changed.example"); err != nil {
					t.Fatal(err)
				}
			case "account":
				a := f.a
				a.Revision++
				if err = f.s.saveAccount(a); err != nil {
					t.Fatal(err)
				}
			case "rule":
				r := f.r
				r.CopySubtitles = true
				if err = f.s.saveRule(r); err != nil {
					t.Fatal(err)
				}
			case "generation":
				f.s.startupRestore.Generation = strings.Repeat("a", 32)
			}
			if err = reopened.publish(t.Context()); err == nil {
				t.Fatal("stale identity published")
			}
			var baseline string
			if err = f.s.db.QueryRow("SELECT run FROM scan_baselines WHERE rule=? AND scope=?", f.r.ID, f.r.SourcePath).Scan(&baseline); err != nil || baseline != first.Run.ID {
				t.Fatal("changed scan replaced valid prior baseline", baseline, err)
			}
		})
	}
}

func TestScanHistoryExcludesLimitedPreviewAndRetryCoverage(t *testing.T) {
	f := scanHistoryFixture(t)
	for _, job := range []Job{{DryRun: true}, {Limit: 1}, {RetryDirs: []ScanDir{{ID: "only-failed", Source: "/media/B"}}}} {
		job.ID = randomID()
		job.Rule = f.r.ID
		job.Folder = f.r.Folder
		job.SourcePath = f.r.SourcePath
		if session, err := f.s.startScanHistory(t.Context(), job, f.r, "fixture-output"); err != nil || session != nil {
			t.Fatal("incomplete scope acquired baseline authority", err)
		}
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_runs"); n != 0 {
		t.Fatal("ineligible job persisted run", n)
	}
	f.s.restoreRestricted = true
	if _, err := f.s.startScanHistory(t.Context(), Job{}, f.r, "fixture-output"); err == nil {
		t.Fatal("restore hold allowed scan mutation")
	}
}

func TestScanHistoryPeriodicFullReconciliationAndCompletedReplay(t *testing.T) {
	f := scanHistoryFixture(t)
	first := scanHistoryStart(t, f, 1, "")
	if err := first.recordListing(t.Context(), ScanDir{ID: "root", Source: "/media"}, nil); err != nil {
		t.Fatal(err)
	}
	scanHistoryDone(t, first, "root")
	if !first.Run.Full {
		t.Fatal("first scan tried incremental")
	}
	if err := first.publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened := scanHistoryStart(t, f, 1, "")
	if !reopened.Complete {
		t.Fatal("completed run replay lost terminal identity")
	}
	second := scanHistoryStart(t, f, 2, "")
	if second.Run.Full {
		t.Fatal("fresh baseline forced full reconciliation")
	}
	if _, err := f.s.db.Exec("UPDATE scan_baselines SET full_completed=?", time.Now().Add(-25*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	third := scanHistoryStart(t, f, 3, "")
	if !third.Run.Full {
		t.Fatal("expired full reconciliation was skipped")
	}
}

func TestScanHistoryRootReconcilesNewerSubtreeObservations(t *testing.T) {
	for _, initial := range []bool{false, true} {
		t.Run(fmt.Sprintf("initially-present=%v", initial), func(t *testing.T) {
			f := scanHistoryFixture(t)
			file := Entry{ID: "movie", Name: "movie.mkv", Size: 10, Modified: "v1"}
			dir := Entry{ID: "sub", Name: "A", Dir: true}
			complete := func(n int, subtree, present bool) *scanSession {
				scope, folder := "/media", "root"
				if subtree {
					scope, folder = "/media/A", "sub"
				}
				session, err := f.s.startScanHistory(t.Context(), Job{ID: fmt.Sprintf("%032x", n), Rule: f.r.ID, Folder: folder, SourcePath: scope}, f.r, "fixture-output")
				if err != nil {
					t.Fatal(err)
				}
				var files []Entry
				if present {
					files = []Entry{file}
				}
				if !subtree {
					if err = session.recordListing(t.Context(), ScanDir{ID: "root", Source: "/media"}, []Entry{dir}); err != nil {
						t.Fatal(err)
					}
				}
				if err = session.recordListing(t.Context(), ScanDir{ID: "sub", Source: "/media/A"}, files); err != nil {
					t.Fatal(err)
				}
				ids := []string{"sub"}
				if !subtree {
					ids = append(ids, "root")
				}
				if present {
					ids = append(ids, "movie")
				}
				scanHistoryDone(t, session, ids...)
				if err = session.publish(t.Context()); err != nil {
					t.Fatal(err)
				}
				return session
			}
			complete(1, false, initial)
			complete(2, true, !initial)
			last := complete(3, false, initial)
			kind := "source_absent"
			if initial {
				kind = "added"
			}
			if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_changes WHERE run=? AND remote='movie' AND kind=?", last.Run.ID, kind); n != 1 {
				t.Fatalf("lost subtree observation: %s count=%d", kind, n)
			}
			if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM scan_baselines"); n != 1 {
				t.Fatal("obsolete subtree pointer remains", n)
			}
		})
	}
}
