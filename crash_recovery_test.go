package main

import (
	"bufio"
	"context"
	"database/sql/driver"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// The blocking SQL function exists only in this test binary. Killing the child
// at an INSERT trigger exercises real WAL rollback without a production hook.
func TestCrashRecoveryChild(t *testing.T) {
	if os.Getenv("YOLINK_CRASH_CHILD") != "1" {
		t.Skip("isolated child process only")
	}
	if err := sqlite.RegisterScalarFunction("yolink_test_crash_gate", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		fmt.Println("PHASE1_KILL_READY")
		select {}
	}); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(os.Getenv("YOLINK_CRASH_DATA"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	table := os.Getenv("YOLINK_CRASH_TABLE")
	if table != "outputs" && table != "activity_events" && table != "output_details" {
		t.Fatal("invalid crash phase")
	}
	if _, err = s.db.Exec("CREATE TRIGGER phase1_crash_gate AFTER INSERT ON " + table + " BEGIN SELECT yolink_test_crash_gate(); END"); err != nil {
		t.Fatal(err)
	}
	c := crashRecoveryCloud()
	e, err := newEngine(context.Background(), s, c, os.Getenv("YOLINK_CRASH_OUTPUT"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.subtitleDownload = func(context.Context, PlaybackTarget) ([]byte, error) { return []byte("recovered subtitle"), nil }
	if _, err = e.enqueue("rule", "", "", false, 2); err != nil {
		t.Fatal(err)
	}
	select {}
}

func crashRecoveryCloud() *fixtureCloud {
	return &fixtureCloud{dirs: map[string][]Entry{"root": {
		{ID: "first", Name: "movie.mkv"}, {ID: "sub", Name: "movie.srt"}, {ID: "second", Name: "second.mkv"},
	}}, target: PlaybackTarget{URL: "https://fixture.invalid/subtitle", Mode: "302"}}
}

func TestAbruptProcessDeathBeforeCheckpointPreservesOutputsAndLimit(t *testing.T) {
	for _, table := range []string{"outputs", "activity_events", "output_details"} {
		for _, edited := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/edited=%t", table, edited), func(t *testing.T) {
				f := setup(t)
				f.e.Close()
				f.r.CopySubtitles = true
				if err := f.s.saveRule(f.r); err != nil {
					t.Fatal(err)
				}
				if err := f.s.db.Close(); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(os.Args[0], "-test.run=^TestCrashRecoveryChild$", "-test.timeout=1m")
				cmd.Env = append(os.Environ(), "YOLINK_CRASH_CHILD=1", "YOLINK_CRASH_DATA="+f.data, "YOLINK_CRASH_OUTPUT="+f.output, "YOLINK_CRASH_TABLE="+table)
				pipe, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				cmd.Stderr = cmd.Stdout
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = cmd.Process.Kill() })
				ready := make(chan struct{}, 1)
				output := make(chan string, 1)
				go func() {
					var log strings.Builder
					scan := bufio.NewScanner(pipe)
					for scan.Scan() {
						line := scan.Text()
						log.WriteString(line + "\n")
						if line == "PHASE1_KILL_READY" {
							ready <- struct{}{}
						}
					}
					output <- log.String()
				}()
				select {
				case <-ready:
				case log := <-output:
					_ = cmd.Wait()
					t.Fatal("child exited before crash boundary", log)
				case <-time.After(20 * time.Second):
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatal("child did not reach crash boundary")
				}
				if err = cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				<-output
				if err = cmd.Wait(); err == nil {
					t.Fatal("child was not abruptly terminated")
				}
				first := filepath.Join(f.output, "test/media/movie.strm")
				original, err := os.ReadFile(first)
				if err != nil {
					t.Fatal("publication did not precede crash", err)
				}
				if edited {
					original = []byte("https://user-edit.invalid/preserve\n")
					if err = os.WriteFile(first, original, 0600); err != nil {
						t.Fatal(err)
					}
				}
				s, err := openStore(f.data)
				if err != nil {
					t.Fatal(err)
				}
				defer s.db.Close()
				if _, err = s.db.Exec("DROP TRIGGER phase1_crash_gate"); err != nil {
					t.Fatal(err)
				}
				jobs, err := s.jobs(10)
				if err != nil || len(jobs) != 1 || jobs[0].Status != "queued" || jobs[0].Seen != 0 {
					t.Fatal("uncommitted progress survived crash", jobs, err)
				}
				// Install the downloader before permitting the real worker to resume.
				job := jobs[0]
				job.Status = "interrupted"
				if err = s.saveJob(job); err != nil {
					t.Fatal(err)
				}
				e, err := newEngine(context.Background(), s, crashRecoveryCloud(), f.output)
				if err != nil {
					t.Fatal(err)
				}
				defer e.Close()
				e.subtitleDownload = func(context.Context, PlaybackTarget) ([]byte, error) { return []byte("recovered subtitle"), nil }
				job.Status = "queued"
				if err = s.saveJob(job); err != nil {
					t.Fatal(err)
				}
				e.run(job.ID)
				done, err := s.job(job.ID)
				status := "completed"
				if edited {
					status = "partial"
				}
				if err != nil || done.Status != status || done.Seen != 2 {
					t.Fatal("resume status/limit mismatch", done, err)
				}
				if b, err := os.ReadFile(first); err != nil || string(b) != string(original) {
					t.Fatal("published or user-edited bytes changed", err)
				}
				if _, err = os.Stat(filepath.Join(f.output, "test/media/second.strm")); err != nil {
					t.Fatal("crash replay prematurely exhausted limit", err)
				}
				if !edited {
					b, err := os.ReadFile(filepath.Join(f.output, "test/media/movie.srt"))
					if err != nil || string(b) != "recovered subtitle" {
						t.Fatal("interrupted companion not recovered", err)
					}
				}
				rows, err := s.db.Query("SELECT path,hash FROM outputs")
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				for rows.Next() {
					var name, hash string
					if err = rows.Scan(&name, &hash); err != nil {
						t.Fatal(err)
					}
					if edited && filepath.ToSlash(name) == "test/media/movie.strm" {
						continue // User edits intentionally differ from registered hash.
					}
					b, err := os.ReadFile(filepath.Join(f.output, filepath.FromSlash(name)))
					if err != nil || contentHash(b) != hash {
						t.Fatal("inconsistent persisted output ownership", name, err)
					}
				}
				if err = rows.Err(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
