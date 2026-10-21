package main

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestChangesBrowserEndToEnd(t *testing.T) {
	if os.Getenv("YOLINK_CHANGES_BROWSER_E2E") != "1" {
		t.Skip("opt-in isolated browser fixture")
	}
	f := strongScanFixture(t)
	job := generate(t, f, false, 0)
	if job.Status != "completed" {
		t.Fatal(job)
	}
	file := filepath.Join(f.output, "test/media/first.strm")
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	_, handler, _ := testToolsServer(t, f, f.c)
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "node_modules/@playwright/test/cli.js", "test", "--config=playwright.live.config.ts", "changes.spec.ts")
	cmd.Dir = "frontend"
	cmd.Env = append(os.Environ(), "YOLINK_TEST_BASE="+server.URL, "YOLINK_TEST_FIXTURE=changes")
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(file); err != nil || string(raw) != string(original) {
		t.Fatal("browser failed to restore exact output", err)
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM change_plans WHERE state='complete'"); n != 1 {
		t.Fatal("repair was not persisted", n)
	}
	rule, err := f.s.rule(f.r.ID)
	if err != nil || rule.Template != "music" || rule.TemplateVersion != 1 || rule.Filter.Preset != "audio" || rule.Enabled || !rule.Incremental {
		t.Fatal("template not saved as an ordinary editable rule", rule, err)
	}
	if n := scanHistoryCount(t, f, "SELECT COUNT(*) FROM jobs"); n != 1 {
		t.Fatal("template save started work", n)
	}
}
