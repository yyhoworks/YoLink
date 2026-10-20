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

func TestOutputMountDiscoveryFiltersAndDecodes(t *testing.T) {
	raw := `1 0 0:1 / / ro - overlay overlay rw
2 1 8:1 /project/data /data rw - btrfs /dev/sda rw
3 1 8:1 /output /strm rw - btrfs /dev/sda rw
4 1 8:1 /extra /extra\040output rw - ext4 /dev/sda rw
5 1 8:1 /readonly /readonly ro - ext4 /dev/sda rw
6 1 0:2 / /tmp rw - tmpfs tmpfs rw
7 1 8:1 /system /etc/secrets rw - ext4 /dev/sda rw
8 1 8:1 /output /super-readonly rw - ext4 /dev/sda ro
9 1 8:1 /nested-data /data/private rw - ext4 /dev/sda rw
`
	got := mappedOutputPaths(raw, filepath.FromSlash("/data"))
	for i := range got {
		got[i] = filepath.ToSlash(got[i])
	}
	if strings.Join(got, ",") != "/strm,/extra output" {
		t.Fatal(got)
	}
}

func TestGenerateToMappedOutputAndLegacyCompatibility(t *testing.T) {
	f := setup(t)
	extra := t.TempDir()
	if err := f.e.addOutputMount(extra, false); err != nil {
		t.Fatal(err)
	}
	f.r.Output = filepath.ToSlash(filepath.Join(extra, "电影"))
	f.s.saveRule(f.r)
	j := generate(t, f, false, 0)
	if j.CreatedFiles != 3 || j.Errors != 0 {
		t.Fatal(j)
	}
	if _, err := os.Stat(filepath.Join(extra, "电影", "电影 & 测试.strm")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(f.output)
	if len(entries) != 0 {
		t.Fatal("extra output leaked into default", entries)
	}
	if j = generate(t, f, false, 0); j.CreatedFiles != 0 || j.Skipped != 3 {
		t.Fatal(j)
	}
	// Identical default paths represented as old relative or new absolute values share ownership.
	f.r.Output = "legacy"
	f.s.saveRule(f.r)
	generate(t, f, false, 0)
	f.r.Output = filepath.ToSlash(filepath.Join(f.output, "legacy"))
	f.s.saveRule(f.r)
	if j = generate(t, f, false, 0); j.Skipped != 3 || j.Errors != 0 {
		t.Fatal(j)
	}
	o, _ := f.s.generationOverview()
	if o.Outputs != 6 {
		t.Fatal(o)
	}
	for _, p := range []string{"../escape", filepath.ToSlash(filepath.Join(t.TempDir(), "escape")), filepath.ToSlash(filepath.Join(extra, "..", "outside")), filepath.ToSlash(extra) + "/../bad"} {
		if _, err := f.e.publish(p, "x", []byte("x")); err == nil {
			t.Fatal("unmapped path accepted", p)
		}
	}
}

func TestOutputDirectoryAPIAndSaveDoNotCreateFiles(t *testing.T) {
	f := setup(t)
	extra := t.TempDir()
	os.Mkdir(filepath.Join(extra, "Season 01"), 0755)
	os.WriteFile(filepath.Join(extra, "secret.txt"), []byte("not listed"), 0644)
	f.e.addOutputMount(extra, false)
	_, h, cookie := testToolsServer(t, f, f.c)
	read := func(path string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/output/directories"+path, nil)
		if auth {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if w := read("", false); w.Code != 401 {
		t.Fatal("unauthenticated directory read", w.Code)
	}
	w := read("", true)
	var roots struct {
		Mounts []outputMount `json:"mounts"`
	}
	json.Unmarshal(w.Body.Bytes(), &roots)
	if w.Code != 200 || len(roots.Mounts) < 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = read("?path="+url.QueryEscape(filepath.ToSlash(extra)), true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Season 01") || strings.Contains(w.Body.String(), "secret.txt") {
		t.Fatal(w.Body.String())
	}
	for _, p := range []string{filepath.ToSlash(f.data), filepath.ToSlash(extra) + "/../", "relative"} {
		if w = read("?path="+url.QueryEscape(p), true); w.Code != 400 {
			t.Fatal(p, w.Code)
		}
	}
	f.r.Output = filepath.ToSlash(filepath.Join(extra, "New", "Movies"))
	w = generationPOST(h, cookie, "/api/rules", f.r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(extra, "New")); !os.IsNotExist(err) {
		t.Fatal("saving path created directory", err)
	}
	if _, err := f.e.normalizeOutputDirectory(filepath.ToSlash(extra)); err != nil {
		t.Fatal("mount root cannot be selected", err)
	}
}

func TestMappedOutputMaintenanceBacksUpAndRestores(t *testing.T) {
	f := setup(t)
	extra := t.TempDir()
	f.e.addOutputMount(extra, false)
	f.r.Output = filepath.ToSlash(extra)
	f.s.saveRule(f.r)
	generate(t, f, false, 0)
	file := filepath.Join(extra, "电影 & 测试.strm")
	old, _ := os.ReadFile(file)
	s, h, cookie := testToolsServer(t, f, f.c)
	f.s.setSetting("public_base", "http://changed.example:8796")
	w := generationPOST(h, cookie, "/api/maintenance/links", map[string]any{})
	var p struct {
		ID      string `json:"id"`
		Ready   int    `json:"ready"`
		Applied int    `json:"applied"`
	}
	json.Unmarshal(w.Body.Bytes(), &p)
	if w.Code != 200 || p.Ready != 3 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = generationPOST(h, cookie, "/api/maintenance/"+p.ID+"/apply", map[string]any{})
	json.Unmarshal(w.Body.Bytes(), &p)
	if w.Code != 200 || p.Applied != 3 {
		t.Fatal(w.Body.String())
	}
	changed, _ := os.ReadFile(file)
	if string(changed) == string(old) {
		t.Fatal("extra mount not updated")
	}
	w = generationPOST(h, cookie, "/api/maintenance/"+p.ID+"/rollback", map[string]any{})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	restored, _ := os.ReadFile(file)
	if string(restored) != string(old) {
		t.Fatal("restore mismatch")
	}
	plan, _ := s.loadPlan(p.ID)
	if !filepath.IsAbs(filepath.FromSlash(plan.Items[0].Path)) {
		t.Fatal("extra mount identity lost")
	}
}

func TestMappedOutputRejectsSymlinkEscape(t *testing.T) {
	f := setup(t)
	outside := t.TempDir()
	link := filepath.Join(f.output, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlink unavailable on host", err)
	}
	if _, err := f.e.normalizeOutputDirectory(filepath.ToSlash(link) + "/Movies"); err == nil {
		t.Fatal("escaping directory accepted")
	}
	if _, err := f.e.publish("escape/movie.strm", "x", []byte("x")); err == nil {
		t.Fatal("escaping file written")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("outside files changed")
	}
}

func TestDockerMappedOutputDiscoverySmoke(t *testing.T) {
	if os.Getenv("STRMLINK_DOCKER_OUTPUT_TEST") != "1" {
		t.Skip("requires isolated Docker mounts")
	}
	f := setup(t)
	seen := map[string]bool{}
	for _, m := range f.e.outputMounts {
		seen[m.Path] = true
	}
	if !seen["/strm"] || !seen["/CustomOut"] || seen["/data"] {
		t.Fatal("Docker mounts were not detected correctly", seen)
	}
	f.r.Output = "/CustomOut/isolated-test"
	f.s.saveRule(f.r)
	if j := generate(t, f, false, 0); j.CreatedFiles != 3 || j.Errors != 0 {
		t.Fatal(j)
	}
	if _, err := os.Stat("/CustomOut/isolated-test/电影 & 测试.strm"); err != nil {
		t.Fatal(err)
	}
}

func TestServeOutputPickerReview(t *testing.T) {
	if os.Getenv("STRMLINK_OUTPUT_UI_REVIEW") != "1" {
		t.Skip("local UI fixture")
	}
	f := setup(t)
	f.a.Name = "移动媒体盘（测试账号）"
	f.s.saveAccount(f.a)
	extra := t.TempDir()
	os.Mkdir(filepath.Join(extra, "影视输出（测试目录）"), 0755)
	f.e.addOutputMount(extra, false)
	os.Mkdir(filepath.Join(f.output, "电影输出（测试目录）"), 0755)
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "StrmLink-Output-QA")
	s, err := newServer(f.s, f.e, reviewClouds{f.c}, f.data, f.output)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Output picker synthetic fixture http://127.0.0.1:18796")
	if err := http.ListenAndServe("127.0.0.1:18796", s.handler()); err != nil {
		t.Fatal(err)
	}
}
