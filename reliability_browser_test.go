package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in browser contract test: real HTTP handlers, auth, SQLite and output
// files, with only the cloud/media transport replaced. Never uses NAS data.
func TestReliabilityBrowserEndToEnd(t *testing.T) {
	if os.Getenv("YOLINK_BROWSER_E2E") != "1" {
		t.Skip("set YOLINK_BROWSER_E2E=1 with frontend dependencies and Edge installed")
	}
	f := setup(t)
	m := diagnosticMedia(t, f)
	// The normal worker runs against the in-memory cloud fixture only.
	j := healthFailedJob(t, f, "synthetic-recovery")
	output := filepath.Join(f.output, "test/media/电影 & 测试.strm")
	const edited = "https://user-edit.example.test/preserved\n"
	if err := os.WriteFile(output, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	s, handler, _ := testToolsServer(t, f, f.c)
	s.probeClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 206, Body: io.NopCloser(strings.NewReader(strings.Repeat("v", 5000))), Header: http.Header{"Content-Type": {"video/mp4"}, "Content-Range": {"bytes 0-1023/5000"}}}, nil
	})}
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "node_modules/@playwright/test/cli.js", "test", "--config=playwright.live.config.ts")
	cmd.Dir = "frontend"
	cmd.Env = append(os.Environ(), "YOLINK_TEST_BASE="+server.URL, "YOLINK_TEST_MEDIA="+m.ID)
	result, err := cmd.CombinedOutput()
	t.Log(string(result))
	if err != nil {
		t.Fatal("real-handler browser test failed:", err)
	}
	stored, err := f.s.job(j.ID)
	if err != nil || stored.RetriedBy == "" {
		t.Fatal("browser did not queue recovery", err)
	}
	if b, err := os.ReadFile(output); err != nil || string(b) != edited {
		t.Fatal("diagnosis changed the user's output", err)
	}
	var reports int
	if err := f.s.db.QueryRow("SELECT COUNT(*) FROM diagnostic_reports").Scan(&reports); err != nil || reports < 1 {
		t.Fatal("browser diagnosis not persisted", err)
	}
}
