package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
)

func checkpointFixture(t *testing.T) (*fixture, Job, *ScanCheckpoint) {
	t.Helper()
	f := setup(t)
	f.e.Close()
	j := Job{ID: "checkpoint-test", Rule: f.r.ID, Folder: f.r.Folder, SourcePath: f.r.SourcePath, Status: "running", Snapshot: &f.r}
	if err := f.s.saveJob(j); err != nil {
		t.Fatal(err)
	}
	cp := &ScanCheckpoint{Current: &ScanDir{ID: "root", Source: "/media"}, Files: []Entry{{ID: "a", Name: "a.mkv"}, {ID: "b", Name: "b.mkv"}}, Claimed: map[string]string{}, Visited: map[string]bool{}}
	return f, j, cp
}

func TestCompactCheckpointUpdatesCursorWithoutRewritingSnapshotOrStructure(t *testing.T) {
	f, j, cp := checkpointFixture(t)
	if err := f.e.saveScan(j, cp); err != nil {
		t.Fatal(err)
	}
	// These triggers prove the actual write path, not merely encoded sizes.
	if _, err := f.s.db.Exec(`CREATE TRIGGER snapshot_immutable BEFORE UPDATE ON checkpoint_snapshots BEGIN SELECT RAISE(ABORT,'unexpected snapshot rewrite'); END;
CREATE TRIGGER structure_immutable BEFORE UPDATE ON job_checkpoints BEGIN SELECT RAISE(ABORT,'unexpected structure rewrite'); END;`); err != nil {
		t.Fatal(err)
	}
	cp.Index = 1
	cp.Claimed["a.strm"] = "a"
	cp.claimPath, cp.claimMedia = "a.strm", "a"
	j.CreatedFiles = 1
	if err := f.e.saveScan(j, cp); err != nil {
		t.Fatal(err)
	}
	var loaded ScanCheckpoint
	if err := f.s.loadCheckpoint(j.ID, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Index != 1 || len(loaded.Files) != 2 || loaded.Claimed["a.strm"] != "a" {
		t.Fatal("checkpoint data lost", loaded)
	}
	saved, err := f.s.job(j.ID)
	if err != nil || saved.CreatedFiles != 1 {
		t.Fatal("progress not committed with cursor", err)
	}
	if _, err = f.s.db.Exec("DELETE FROM job_checkpoints WHERE job=?", j.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"checkpoint_cursors", "checkpoint_snapshots", "checkpoint_claims"} {
		var n int
		if err = f.s.db.QueryRow("SELECT count(*) FROM "+table+" WHERE job=?", j.ID).Scan(&n); err != nil || n != 0 {
			t.Fatal("checkpoint cleanup left rows", table, n, err)
		}
	}
}

func TestCompactCheckpointFailedCommitPreservesPreviousProgress(t *testing.T) {
	f, j, cp := checkpointFixture(t)
	if err := f.e.saveScan(j, cp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`CREATE TRIGGER fail_cursor BEFORE UPDATE ON checkpoint_cursors BEGIN SELECT RAISE(ABORT,'injected disk failure'); END;`); err != nil {
		t.Fatal(err)
	}
	cp.Index, j.CreatedFiles = 1, 1
	cp.Claimed["a.strm"] = "a"
	cp.claimPath, cp.claimMedia = "a.strm", "a"
	if err := f.e.saveScan(j, cp); err == nil {
		t.Fatal("failure injection missed")
	}
	var loaded ScanCheckpoint
	if err := f.s.loadCheckpoint(j.ID, &loaded); err != nil {
		t.Fatal(err)
	}
	saved, err := f.s.job(j.ID)
	if err != nil || saved.CreatedFiles != 0 || loaded.Index != 0 || len(loaded.Claimed) != 0 {
		t.Fatal("partial checkpoint commit", saved, loaded, err)
	}
	if _, err = f.s.db.Exec("DROP TRIGGER fail_cursor"); err != nil {
		t.Fatal(err)
	}
	if err = f.e.saveScan(j, cp); err != nil {
		t.Fatal("retry failed", err)
	}
	if err = f.s.loadCheckpoint(j.ID, &loaded); err != nil || loaded.Index != 1 || loaded.Claimed["a.strm"] != "a" {
		t.Fatal("retry lost claim", err)
	}
}

func TestLegacyCheckpointConvertsWithoutRescanningOrLosingClaims(t *testing.T) {
	f, j, cp := checkpointFixture(t)
	cp.Index, j.Skipped = 1, 1
	cp.Claimed["test/media/a.strm"] = "a"
	if _, err := f.s.db.Exec("INSERT INTO job_checkpoints VALUES(?,?)", j.ID, scanJSON(cp)); err != nil {
		t.Fatal(err)
	}
	var loaded ScanCheckpoint
	if err := f.s.loadCheckpoint(j.ID, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.persisted {
		t.Fatal("legacy format mistaken for compact")
	}
	if err := f.e.saveScan(j, &loaded); err != nil {
		t.Fatal(err)
	}
	if err := f.s.loadCheckpoint(j.ID, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Format != 1 || !loaded.persisted || loaded.Index != 1 || loaded.Claimed["test/media/a.strm"] != "a" {
		t.Fatal("legacy state not preserved", loaded)
	}
}

func TestCheckpointCorruptionFailsClosed(t *testing.T) {
	for _, corruption := range []string{"negative", "past_end", "missing_cursor", "missing_snapshot", "wrong_folder", "future_format", "invalid_json", "null", "empty_object", "missing_todo_id", "missing_file_id", "duplicate_file_id"} {
		t.Run(corruption, func(t *testing.T) {
			f, j, cp := checkpointFixture(t)
			if err := f.e.saveScan(j, cp); err != nil {
				t.Fatal(err)
			}
			var query string
			switch corruption {
			case "negative":
				query = `UPDATE job_checkpoints SET payload='{"Index":-1}'`
			case "past_end":
				query = "UPDATE checkpoint_cursors SET position=999"
			case "missing_cursor":
				query = "DELETE FROM checkpoint_cursors"
			case "missing_snapshot":
				query = "DELETE FROM checkpoint_snapshots"
			case "wrong_folder":
				query = "UPDATE checkpoint_snapshots SET folder='wrong'"
			case "future_format":
				query = `UPDATE job_checkpoints SET payload='{"Format":99}'`
			case "invalid_json":
				query = "UPDATE job_checkpoints SET payload='broken'"
			case "null":
				query = "UPDATE job_checkpoints SET payload='null'"
			case "empty_object":
				query = "UPDATE job_checkpoints SET payload='{}'"
			case "missing_todo_id":
				query = `UPDATE job_checkpoints SET payload=json_set(payload,'$.Todo',json('[{"ID":"","Source":"/media"}]'))`
			case "missing_file_id":
				query = `UPDATE checkpoint_snapshots SET payload='[{"id":"","name":"a.mkv"}]'`
			case "duplicate_file_id":
				query = `UPDATE checkpoint_snapshots SET payload='[{"id":"a","name":"a.mkv"},{"id":"a","name":"b.mkv"}]'`
			}
			if _, err := f.s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			var loaded ScanCheckpoint
			if err := f.s.loadCheckpoint(j.ID, &loaded); err == nil {
				t.Fatal("corrupt checkpoint accepted")
			}
			if err := f.e.scan(context.Background(), f.a, f.r, &j); err == nil {
				t.Fatal("scan accepted corrupt checkpoint")
			}
		})
	}
}

// Real SQLite writes, comparing the prior full-JSON checkpoint representation
// against the compact transaction. Fixture creation is outside measured time.
func BenchmarkCheckpointProgress(b *testing.B) {
	for _, count := range []int{100, 10000} {
		for _, compact := range []bool{false, true} {
			b.Run(fmt.Sprintf("files=%d/compact=%v", count, compact), func(b *testing.B) {
				s, err := openStore(b.TempDir())
				if err != nil {
					b.Fatal(err)
				}
				defer s.db.Close()
				cp := &ScanCheckpoint{Current: &ScanDir{ID: "root", Source: "/media"}, Files: make([]Entry, count), Claimed: map[string]string{}, Visited: map[string]bool{}}
				for i := range cp.Files {
					cp.Files[i] = Entry{ID: fmt.Sprint(i), Name: fmt.Sprintf("episode-%d.mkv", i)}
					cp.Claimed[fmt.Sprintf("episode-%d.strm", i)] = fmt.Sprint(i)
				}
				j := Job{ID: "benchmark", Status: "running"}
				tx, err := s.db.Begin()
				if err != nil {
					b.Fatal(err)
				}
				if err = saveCheckpointTx(tx, j.ID, cp); err != nil {
					tx.Rollback()
					b.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					b.Fatal(err)
				}
				cp.persisted = true
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					cp.Index = i % count
					j.Seen = i
					tx, err := s.db.Begin()
					if err != nil {
						b.Fatal(err)
					}
					if err = saveJobTx(tx, j); err == nil {
						if compact {
							err = saveCheckpointTx(tx, j.ID, cp)
						} else {
							err = saveLegacyCheckpointBenchmark(tx, j.ID, cp)
						}
					}
					if err != nil {
						tx.Rollback()
						b.Fatal(err)
					}
					if err = tx.Commit(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func saveLegacyCheckpointBenchmark(tx *sql.Tx, job string, cp *ScanCheckpoint) error {
	raw, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE job_checkpoints SET payload=? WHERE job=?", string(raw), job)
	return err
}
