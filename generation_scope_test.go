package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTargetedGenerationUsesFullScanOutputPaths(t *testing.T) {
	f := setup(t)
	f.c.dirs = map[string][]Entry{
		"root":   {{ID: "season", Name: "Season:1", Dir: true}},
		"season": {{ID: "disc", Name: "Disc. ", Dir: true}},
		"disc":   {{ID: "episode", Name: "episode.mkv"}},
	}
	full := generate(t, f, false, 0)
	if full.Status != "completed" || full.CreatedFiles != 1 {
		t.Fatal(full)
	}
	file := filepath.Join(f.output, "test/media/Season_1/Disc/episode.strm")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		j, err := f.e.enqueue(f.r.ID, "disc", "/media/Season:1/Disc. ", dry, 0)
		if err != nil {
			t.Fatal(err)
		}
		j = finish(t, f, j)
		if j.Status != "completed" || j.CreatedFiles != 0 || (dry && j.PreviewSame != 1) || (!dry && j.Skipped != 1) {
			t.Fatalf("targeted scan disagrees with full scan (dry=%v): %+v", dry, j)
		}
	}
	after, err := os.ReadFile(file)
	if err != nil || string(before) != string(after) {
		t.Fatal("targeted generation changed existing file", err)
	}
}

func TestRetryUsesSanitizedPathsFromCheckpointAndFailures(t *testing.T) {
	for _, mode := range []string{"checkpoint", "failure_only", "legacy_checkpoint", "resume_legacy_checkpoint"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			f.c.dirs = map[string][]Entry{"root": {{ID: "season", Name: "Season:1", Dir: true}}, "season": {{ID: "episode", Name: "episode.mkv"}}}
			f.c.fail = "season"
			old := generate(t, f, false, 0)
			if old.Status != "failed" {
				t.Fatal(old)
			}
			if mode == "failure_only" {
				if _, err := f.s.db.Exec("DELETE FROM job_checkpoints WHERE job=?", old.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "legacy_checkpoint" || mode == "resume_legacy_checkpoint" {
				if _, err := f.s.db.Exec(`UPDATE job_checkpoints SET payload=json_set(payload,'$.Current.relative','Season:1') WHERE job=?`, old.ID); err != nil {
					t.Fatal(err)
				}
			}
			f.c.fail = ""
			var j Job
			if mode == "resume_legacy_checkpoint" {
				// Restart recovery queues the same job, retaining its checkpoint.
				j = old
				j.Status = "queued"
				j.Errors = 0
				if err := f.s.saveJob(j); err != nil {
					t.Fatal(err)
				}
				f.e.run(j.ID)
			} else {
				var err error
				j, err = f.e.retry(old.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			j = finish(t, f, j)
			if j.Status != "completed" || j.CreatedFiles != 1 {
				t.Fatal(j)
			}
			if _, err := os.Stat(filepath.Join(f.output, "test/media/Season_1/episode.strm")); err != nil {
				t.Fatal(err)
			}
			full := generate(t, f, false, 0)
			if full.Status != "completed" || full.CreatedFiles != 0 || full.Skipped != 1 {
				t.Fatal(full)
			}
		})
	}
}

func TestTargetedGenerationRejectsUnrepresentableDirectory(t *testing.T) {
	f := setup(t)
	f.c.dirs = map[string][]Entry{"root": {{ID: "bad", Name: "...", Dir: true}}, "bad": {{ID: "movie", Name: "movie.mkv"}}}
	j, err := f.e.enqueue(f.r.ID, "bad", "/media/...", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	j = finish(t, f, j)
	if j.Status != "failed" || j.CreatedFiles != 0 {
		t.Fatal(j)
	}
	// Invalid names must not collapse into the parent output directory.
	if _, err := os.Stat(filepath.Join(f.output, "test/media/movie.strm")); !os.IsNotExist(err) {
		t.Fatal("invalid directory escaped its output scope", err)
	}
}

func TestRetryRejectsChangedRuleMapping(t *testing.T) {
	for _, field := range []string{"account", "folder", "source", "output", "missing_snapshot"} {
		t.Run(field, func(t *testing.T) {
			f := setup(t)
			f.c.fail = "sub"
			old := generate(t, f, false, 0)
			if old.Status != "failed" {
				t.Fatal(old)
			}
			h, cookie := generationHandler(t, f)
			rule := f.r
			switch field {
			case "account":
				account := f.a
				account.ID = "other-account"
				if err := f.s.saveAccount(account); err != nil {
					t.Fatal(err)
				}
				rule.Account = account.ID
			case "folder":
				rule.Folder = "other-root"
			case "source":
				// A broader root still passes the old request's path-prefix check.
				rule.SourcePath = "/"
			case "output":
				rule.Output = "other-library"
			case "missing_snapshot":
				old.Snapshot = nil
				if err := f.s.saveJob(old); err != nil {
					t.Fatal(err)
				}
			}
			if w := generationPOST(h, cookie, "/api/rules", rule); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			before, err := f.s.generationOverview()
			if err != nil {
				t.Fatal(err)
			}
			w := generationPOST(h, cookie, "/api/jobs/"+old.ID+"/retry", object{})
			if w.Code != 400 || !strings.Contains(w.Body.String(), "重新生成") {
				t.Fatalf("obsolete retry must be rejected with recovery guidance: %d %s", w.Code, w.Body.String())
			}
			previous, err := f.s.job(old.ID)
			if err != nil || previous.RetriedBy != "" {
				t.Fatal("rejected retry consumed the original failure", previous, err)
			}
			after, err := f.s.generationOverview()
			if err != nil || after != before {
				t.Fatal("rejected retry changed generation state", before, after, err)
			}
		})
	}
}

func TestRetryAllowsNonMappingEdits(t *testing.T) {
	f := setup(t)
	f.c.fail = "sub"
	old := generate(t, f, false, 0)
	if old.Status != "failed" {
		t.Fatal(old)
	}
	h, cookie := generationHandler(t, f)
	rule := f.r
	rule.Name = "renamed"
	rule.Interval = 3600
	if w := generationPOST(h, cookie, "/api/rules", rule); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	f.c.fail = ""
	j, err := f.e.retry(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	j = finish(t, f, j)
	if j.Status != "completed" || j.CreatedFiles != 1 || j.Skipped != 0 {
		t.Fatal("unchanged mapping should retry only the failed directory", j)
	}
}
