package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompanionFailurePreservesTypedCause(t *testing.T) {
	for _, kind := range []string{"subtitle", "nonmedia"} {
		for _, status := range []int{401, 429} {
			f := setup(t)
			f.r.CopySubtitles, f.r.CopyNonMedia = true, true
			f.c.dirs = map[string][]Entry{"root": {{ID: "m", Name: "movie.mkv"}, {ID: "s", Name: "movie.srt"}, {ID: "n", Name: "movie.nfo"}}}
			if err := f.s.saveRule(f.r); err != nil {
				t.Fatal(err)
			}
			failure := cloudHTTPError(status, "", time.Now())
			good := func(context.Context, PlaybackTarget) ([]byte, error) { return []byte("fixture"), nil }
			bad := func(context.Context, PlaybackTarget) ([]byte, error) { return nil, failure }
			f.e.subtitleDownload, f.e.companionDownload = good, good
			if kind == "subtitle" {
				f.e.subtitleDownload = bad
			} else {
				f.e.companionDownload = bad
			}
			j := generate(t, f, false, 0)
			if j.Status != "partial" || j.ErrorKind != failure.Kind {
				t.Fatalf("%s/%d lost cause: %+v", kind, status, j)
			}
		}
	}
}

func TestRestartRetriesInterruptedCompanionWithoutAdvancingCursor(t *testing.T) {
	for _, kind := range []string{"subtitle", "nonmedia"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			f.r.CopySubtitles, f.r.CopyNonMedia = true, true
			f.c.dirs = map[string][]Entry{"root": {{ID: "n", Name: "movie.nfo"}}}
			if kind == "subtitle" {
				f.c.dirs["root"] = []Entry{{ID: "m", Name: "movie.mkv"}, {ID: "s", Name: "movie.srt"}, {ID: "m2", Name: "second.mkv"}}
			}
			if err := f.s.saveRule(f.r); err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			block := func(ctx context.Context, _ PlaybackTarget) ([]byte, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			f.e.subtitleDownload, f.e.companionDownload = block, block
			j, err := f.e.enqueue(f.r.ID, "", "", false, 2)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("copy did not start")
			}
			f.e.Close()
			j, err = f.s.job(j.ID)
			if err != nil || j.Status != "interrupted" {
				t.Fatal("shutdown not resumable", j, err)
			}
			if j.Seen != 0 {
				t.Fatal("tentative item counters survived without cursor", j.Seen)
			}
			var cp ScanCheckpoint
			if err = f.s.loadCheckpoint(j.ID, &cp); err != nil || cp.Index != 0 {
				t.Fatal("unfinished companion cursor advanced", cp.Index, err)
			}
			s, err := openStore(f.data)
			if err != nil {
				t.Fatal(err)
			}
			defer s.db.Close()
			// No worker until the resumed downloader has been injected.
			queued, err := s.job(j.ID)
			if err != nil {
				t.Fatal(err)
			}
			queued.Status = "interrupted"
			if err = s.saveJob(queued); err != nil {
				t.Fatal(err)
			}
			en, err := newEngine(context.Background(), s, f.c, f.output)
			if err != nil {
				t.Fatal(err)
			}
			defer en.Close()
			good := func(context.Context, PlaybackTarget) ([]byte, error) { return []byte("recovered companion"), nil }
			en.subtitleDownload, en.companionDownload = good, good
			queued.Status = "queued"
			if err = s.saveJob(queued); err != nil {
				t.Fatal(err)
			}
			en.run(queued.ID)
			done, err := s.job(queued.ID)
			if err != nil || done.Status != "completed" || done.Errors != 0 {
				t.Fatal("companion not recovered", done, err)
			}
			if kind == "subtitle" {
				if done.Seen != 2 {
					t.Fatal("replayed media counted twice", done.Seen)
				}
				if _, err = os.Stat(filepath.Join(f.output, "test/media/second.strm")); err != nil {
					t.Fatal("replay prematurely reached generation limit", err)
				}
			}
			name := "movie.nfo"
			if kind == "subtitle" {
				name = "movie.srt"
			}
			bytes, err := os.ReadFile(filepath.Join(f.output, "test/media", name))
			if err != nil || string(bytes) != "recovered companion" {
				t.Fatal("missing recovered companion", err)
			}
		})
	}
}
