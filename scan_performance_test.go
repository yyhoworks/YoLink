package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

type measuredScanCloud struct {
	Clouds
	lists atomic.Int64
}

func (c *measuredScanCloud) List(ctx context.Context, account, folder string) ([]Entry, error) {
	c.lists.Add(1)
	return c.Clouds.List(ctx, account, folder)
}

// TestIncrementalRescanListsRootOnceAndSkipsUnchanged is the always-on,
// small-scale counterpart of the opt-in evidence run below: an incremental
// rescan of unchanged files must list the root exactly once and skip every
// file, so a fast-path regression fails an ordinary test run instead of
// going unnoticed between manual measurements.
func TestIncrementalRescanListsRootOnceAndSkipsUnchanged(t *testing.T) {
	f := strongScanFixture(t)
	c := &measuredScanCloud{Clouds: f.c}
	f.e.clouds = c
	initial, err := f.e.enqueue(f.r.ID, "", "", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if j := finishWithin(t, f, initial, time.Minute); j.Status != "completed" || j.CreatedFiles != 2 {
		t.Fatal(j)
	}
	c.lists.Store(0)
	j, err := f.e.enqueueJob(f.r.ID, "", "", false, 0, "manual", "", false)
	if err != nil {
		t.Fatal(err)
	}
	j = finishWithin(t, f, j, time.Minute)
	if j.Status != "completed" || j.CreatedFiles != 0 || j.IncrementalSkipped != 2 {
		t.Fatal("incremental rescan did not take the fast path", j)
	}
	if c.lists.Load() != 1 {
		t.Fatal("incremental rescan listed directories", c.lists.Load(), "times, want 1")
	}
}

func TestIncrementalPerformanceEvidence(t *testing.T) {
	if os.Getenv("YOLINK_INCREMENTAL_MEASURE") != "1" {
		t.Skip("opt-in repeatable synthetic comparison")
	}
	for _, full := range []bool{true, false} {
		t.Run(fmt.Sprintf("full=%v", full), func(t *testing.T) {
			f := strongScanFixture(t)
			files := make([]Entry, 1000)
			for i := range files {
				files[i] = Entry{ID: fmt.Sprintf("file-%04d", i), Name: fmt.Sprintf("movie-%04d.mkv", i), Size: 1024, Modified: "fixture-v1"}
			}
			f.c.dirs["root"] = files
			c := &measuredScanCloud{Clouds: f.c}
			f.e.clouds = c
			initial, err := f.e.enqueue(f.r.ID, "", "", false, 0)
			if err != nil {
				t.Fatal(err)
			}
			// This fixture uses a persistent NAS volume with 1,000 per-item
			// durable transactions, not the five-second small-fixture budget.
			if j := finishWithin(t, f, initial, 3*time.Minute); j.Status != "completed" {
				t.Fatal(j)
			}
			for sample := 0; sample < 2; sample++ {
				var before, after int64
				var memBefore, memAfter runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&memBefore)
				c.lists.Store(0)
				f.s.db.QueryRow("SELECT total_changes()").Scan(&before)
				started := time.Now()
				j, err := f.e.enqueueJob(f.r.ID, "", "", false, 0, "manual", "", full)
				if err != nil {
					t.Fatal(err)
				}
				j = finishWithin(t, f, j, 3*time.Minute)
				elapsed := time.Since(started)
				f.s.db.QueryRow("SELECT total_changes()").Scan(&after)
				runtime.ReadMemStats(&memAfter)
				if j.Status != "completed" || j.CreatedFiles != 0 || j.Skipped != 1000 || c.lists.Load() != 1 {
					t.Fatal(j, c.lists.Load())
				}
				if !full && j.IncrementalSkipped != 1000 {
					t.Fatal("fast path not measured", j)
				}
				t.Logf("full=%v sample=%d files=1000 lists=%d sqlite_changes_including_quota_triggers=%d elapsed_ms=%.3f allocated_bytes=%d incremental_skipped=%d generated=%d", full, sample, c.lists.Load(), after-before, float64(elapsed.Microseconds())/1000, memAfter.TotalAlloc-memBefore.TotalAlloc, j.IncrementalSkipped, j.CreatedFiles)
			}
		})
	}
}
