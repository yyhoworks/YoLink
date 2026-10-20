package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func oldMigrationURL(storage, source string) []byte {
	return []byte("http://old-muvyo:7811/redirect?" + url.Values{"storage": {storage}, "path": {source}}.Encode() + "\n")
}
func migrationFile(t *testing.T, dir, name string, raw []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, raw, 0644); err != nil {
		t.Fatal(err)
	}
	return p
}
func migrationPlanResponse(t *testing.T, s *Server, w *httptest.ResponseRecorder) MaintenancePlan {
	t.Helper()
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	p, err := s.loadPlan(result.ID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func makeInplacePlan(t *testing.T, f *fixture, s *Server, h http.Handler, cookie *http.Cookie, dir string, extras map[string]any) MaintenancePlan {
	t.Helper()
	scan := migrationPlanResponse(t, s, generationPOST(h, cookie, "/api/maintenance/migration/scan", map[string]any{"directory": filepath.ToSlash(dir)}))
	in := map[string]any{"plan_id": scan.ID, "mapping": map[string]string{"yun139-1": f.a.ID}}
	for k, v := range extras {
		in[k] = v
	}
	return migrationPlanResponse(t, s, generationPOST(h, cookie, "/api/maintenance/migration/inplace", in))
}
func TestInplaceMigrationPreservesPathsAndSidecarsAndRestores(t *testing.T) {
	f := setup(t)
	s, h, c := testToolsServer(t, f, f.c)
	dir := filepath.Join(f.output, "MVStrm")
	original := oldMigrationURL("yun139-1", "/movie/电影 & 测试.mkv")
	filename := migrationFile(t, dir, "电影/别名.strm", original)
	nfo := migrationFile(t, dir, "电影/别名.nfo", []byte("metadata"))
	poster := migrationFile(t, dir, "电影/poster.jpg", []byte("poster"))
	p := makeInplacePlan(t, f, s, h, c, dir, map[string]any{"prefixes": map[string]any{"yun139-1": map[string]string{"from": "/movie", "to": "/"}}})
	var count int
	f.s.db.QueryRow("SELECT COUNT(*) FROM media").Scan(&count)
	if count != 0 {
		t.Fatal("preview wrote media")
	}
	raw, _ := os.ReadFile(filename)
	if string(raw) != string(original) || len(p.Items) != 1 {
		t.Fatal("preview changed original")
	}
	p = migrationPlanResponse(t, s, generationPOST(h, c, "/api/maintenance/"+p.ID+"/apply", map[string]any{"confirm_directory": p.Directory, "limit": 1}))
	if !p.Items[0].Applied || p.Items[0].Media.RemoteID != "movie" {
		t.Fatal(planSummary(p))
	}
	raw, _ = os.ReadFile(filename)
	if !strings.Contains(string(raw), "nas.example:8796/s/") {
		t.Fatal("file not converted")
	}
	backup, err := os.ReadFile(s.inplaceBackup(p, p.Items[0]))
	if err != nil || string(backup) != string(original) {
		t.Fatal("backup missing", err)
	}
	for name, want := range map[string]string{nfo: "metadata", poster: "poster"} {
		got, _ := os.ReadFile(name)
		if string(got) != want {
			t.Fatal("sidecar changed")
		}
	}
	f.s.db.QueryRow("SELECT COUNT(*) FROM outputs").Scan(&count)
	if count != 1 {
		t.Fatal("output not registered")
	}
	// Repeated apply and reload use the durable checkpoint without duplicating work.
	again := migrationPlanResponse(t, s, generationPOST(h, c, "/api/maintenance/"+p.ID+"/apply", map[string]any{"confirm_directory": p.Directory, "limit": 1}))
	if !again.Items[0].Applied {
		t.Fatal("lost durable progress")
	}
	p = migrationPlanResponse(t, s, generationPOST(h, c, "/api/maintenance/"+p.ID+"/rollback", map[string]any{}))
	raw, _ = os.ReadFile(filename)
	if p.Items[0].Applied || string(raw) != string(original) {
		t.Fatal("rollback failed")
	}
	f.s.db.QueryRow("SELECT COUNT(*) FROM outputs").Scan(&count)
	if count != 0 {
		t.Fatal("rollback retained output ownership")
	}
}
func TestInplaceMigrationGuardsAndUnsupportedFiles(t *testing.T) {
	f := setup(t)
	s, h, c := testToolsServer(t, f, f.c)
	dir := filepath.Join(f.output, "MVStrm")
	raw := oldMigrationURL("yun139-1", "/电影 & 测试.mkv")
	file := migrationFile(t, dir, "one.strm", raw)
	migrationFile(t, dir, "unsupported.strm", oldMigrationURL("guangyapan-1", "/movie.mkv"))
	migrationFile(t, dir, "new.strm", []byte("http://nas.example:8796/s/already?k=sign\n"))
	p := makeInplacePlan(t, f, s, h, c, dir, nil)
	if len(p.Items) != 3 {
		t.Fatal(len(p.Items))
	}
	endpoint := "/api/maintenance/" + p.ID + "/apply"
	if w := generationPOST(h, c, endpoint, map[string]any{}); w.Code != 400 {
		t.Fatal("missing directory confirmation accepted")
	}
	f.s.setSetting("public_base", "http://changed:8796")
	if w := generationPOST(h, c, endpoint, map[string]any{"confirm_directory": p.Directory}); w.Code != 409 {
		t.Fatal("changed public base accepted")
	}
	f.s.setSetting("public_base", "http://nas.example:8796")
	f.s.saveJob(Job{ID: "guard-busy", Status: "queued"})
	if w := generationPOST(h, c, endpoint, map[string]any{"confirm_directory": p.Directory}); w.Code != 409 {
		t.Fatal("busy job accepted")
	}
	f.s.saveJob(Job{ID: "guard-busy", Status: "cancelled"})
	os.WriteFile(file, []byte("manually changed"), 0644)
	p = migrationPlanResponse(t, s, generationPOST(h, c, endpoint, map[string]any{"confirm_directory": p.Directory}))
	for _, item := range p.Items {
		if item.Applied || item.Error == "" {
			t.Fatal("unexpected overwrite", item.Path)
		}
	}
	got, _ := os.ReadFile(file)
	if string(got) != "manually changed" {
		t.Fatal("external edit overwritten")
	}
	outside := t.TempDir()
	if w := generationPOST(h, c, "/api/maintenance/migration/scan", map[string]any{"directory": filepath.ToSlash(outside)}); w.Code != 400 {
		t.Fatal("unmapped directory accepted")
	}
}
func TestInplaceMigrationRetryAndBatchLimit(t *testing.T) {
	f := setup(t)
	s, h, c := testToolsServer(t, f, f.c)
	dir := filepath.Join(f.output, "MVStrm")
	for _, name := range []string{"one.strm", "two.strm"} {
		migrationFile(t, dir, name, oldMigrationURL("yun139-1", "/missing.mkv"))
	}
	p := makeInplacePlan(t, f, s, h, c, dir, nil)
	endpoint := "/api/maintenance/" + p.ID + "/apply"
	p = migrationPlanResponse(t, s, generationPOST(h, c, endpoint, map[string]any{"confirm_directory": p.Directory, "limit": 1}))
	summary := planSummary(p)
	if summary["pending"] != 1 || summary["skipped"] != 1 {
		t.Fatal(summary)
	}
	// File becomes available; retry the failed item and retain the untouched item.
	f.c.dirs["root"] = append(f.c.dirs["root"], Entry{ID: "found", Name: "missing.mkv"})
	p = migrationPlanResponse(t, s, generationPOST(h, c, endpoint, map[string]any{"confirm_directory": p.Directory, "limit": 1, "retry": true}))
	summary = planSummary(p)
	if summary["applied"] != 1 || summary["pending"] != 1 {
		t.Fatal(summary)
	}
}
func TestInplaceRollbackKeepsExternalEditsAndRecoversInterruptedWrite(t *testing.T) {
	f := setup(t)
	s, h, c := testToolsServer(t, f, f.c)
	dir := filepath.Join(f.output, "MVStrm")
	raw := oldMigrationURL("yun139-1", "/电影 & 测试.mkv")
	file := migrationFile(t, dir, "one.strm", raw)
	p := makeInplacePlan(t, f, s, h, c, dir, nil)
	// Simulate a crash between atomic publication and output/progress registration.
	m := *p.Items[0].Media
	m.RemoteID = "movie"
	f.s.saveMedia(m)
	backup := s.inplaceBackup(p, p.Items[0])
	os.MkdirAll(filepath.Dir(backup), 0700)
	os.WriteFile(backup, raw, 0600)
	os.WriteFile(file, []byte(p.Items[0].Content), 0644)
	p = migrationPlanResponse(t, s, generationPOST(h, c, "/api/maintenance/"+p.ID+"/apply", map[string]any{"confirm_directory": p.Directory, "limit": 1}))
	if !p.Items[0].Applied {
		t.Fatal(planSummary(p))
	}
	os.WriteFile(file, []byte("external"), 0644)
	p = migrationPlanResponse(t, s, generationPOST(h, c, "/api/maintenance/"+p.ID+"/rollback", map[string]any{}))
	got, _ := os.ReadFile(file)
	if string(got) != "external" || !p.Items[0].Applied || p.Items[0].Error == "" {
		t.Fatal("rollback overwrote external content")
	}
}
