package main

import (
	"context"
	"fmt"
	"testing"
)

func TestIngestBatchSelectsIdleRulesFairly(t *testing.T) {
	for _, status := range []string{"running", "queued", "completed"} {
		t.Run(status, func(t *testing.T) {
			f := setup(t)
			// Keep the real queue/store, but step ingestion without background races.
			f.cancel()
			f.e.wg.Wait()
			f.e.ctx = context.Background()
			if err := f.s.saveJob(Job{ID: "first-rule-job", Rule: f.r.ID, Status: status}); err != nil {
				t.Fatal(err)
			}
			second := f.r
			second.ID = "second-rule"
			if err := f.s.saveRule(second); err != nil {
				t.Fatal(err)
			}
			f.c.dirs["root"] = nil
			for i := 0; i < 96; i++ {
				folder := fmt.Sprintf("folder-%03d", i)
				f.c.dirs["root"] = append(f.c.dirs["root"], Entry{ID: folder, Name: folder, Dir: true})
				if _, err := f.s.db.Exec("INSERT INTO ingest_events VALUES(?,?,?,'pending','','',?,1)", fmt.Sprintf("event-%03d", i), f.r.ID, "/media/"+folder, i); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.db.Exec("INSERT INTO ingest_events VALUES('second-event',?,?,'pending','','',100,1)", second.ID, second.SourcePath); err != nil {
				t.Fatal(err)
			}
			f.e.processIngest()
			var job, eventStatus string
			if err := f.s.db.QueryRow("SELECT job,status FROM ingest_events WHERE id='second-event'").Scan(&job, &eventStatus); err != nil {
				t.Fatal(err)
			}
			if job == "" || eventStatus != "submitted" {
				t.Fatalf("idle rule blocked by earlier events: job=%q status=%q", job, eventStatus)
			}
			var submitted int
			if err := f.s.db.QueryRow("SELECT COUNT(*) FROM ingest_events WHERE rule=? AND status='submitted'", f.r.ID).Scan(&submitted); err != nil {
				t.Fatal(err)
			}
			want := 0
			if status == "completed" {
				want = 1
			}
			if submitted != want {
				t.Fatalf("first rule dispatched %d events, want %d", submitted, want)
			}
			// A blocked event is retained and resumes once that rule becomes idle.
			if status != "completed" {
				if err := f.s.saveJob(Job{ID: "first-rule-job", Rule: f.r.ID, Status: "completed"}); err != nil {
					t.Fatal(err)
				}
				f.e.processIngest()
				var firstStatus string
				if err := f.s.db.QueryRow("SELECT status FROM ingest_events WHERE id='event-000'").Scan(&firstStatus); err != nil {
					t.Fatal(err)
				}
				if firstStatus != "submitted" {
					t.Fatal("oldest deferred event did not resume", firstStatus)
				}
			}
		})
	}
}
