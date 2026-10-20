package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingOutputRepairRestoresExactBytesAndPreservesExisting(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	s, _, _ := testToolsServer(t, f, f.c)
	_, _, key, _ := f.e.outputLocation("test/media/电影 & 测试.strm")
	file := filepath.Join(f.output, filepath.FromSlash(key))
	original, _ := os.ReadFile(file)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	created, err := s.restoreMissingOutput(key)
	if err != nil || !created {
		t.Fatal(created, err)
	}
	raw, _ := os.ReadFile(file)
	if string(raw) != string(original) {
		t.Fatal("restoration changed the signed content")
	}
	if status, _ := s.inspectOutput(context.Background(), key, false); status != "healthy" {
		t.Fatal(status)
	}
	if created, err = s.restoreMissingOutput(key); created || err != nil {
		t.Fatal("repair is not idempotent", created, err)
	}
	if err := os.WriteFile(file, []byte("owner's replacement"), 0644); err != nil {
		t.Fatal(err)
	}
	if created, err = s.restoreMissingOutput(key); created || err != nil {
		t.Fatal(created, err)
	}
	raw, _ = os.ReadFile(file)
	if string(raw) != "owner's replacement" {
		t.Fatal("an existing file was replaced")
	}
}

func TestMissingOutputRepairRejectsUnownedAndChangedSignature(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	s, _, _ := testToolsServer(t, f, f.c)
	if _, err := s.restoreMissingOutput("../outside.strm"); err == nil {
		t.Fatal("unowned path accepted")
	}
	_, _, key, _ := f.e.outputLocation("test/media/音乐.strm")
	file := filepath.Join(f.output, filepath.FromSlash(key))
	os.Remove(file)
	f.s.setSetting("public_base", "https://changed.example")
	if _, err := s.restoreMissingOutput(key); err == nil {
		t.Fatal("different content was restored")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("rejected repair wrote a file")
	}
}

func TestMissingOutputRepairAPIIsBoundedAndBlocksActiveGeneration(t *testing.T) {
	f := setup(t)
	generate(t, f, false, 0)
	_, h, cookie := testToolsServer(t, f, f.c)
	_, _, key, _ := f.e.outputLocation("test/media/音乐.strm")
	body, _ := json.Marshal(object{"paths": []string{key}})
	request := func(body string, authenticated bool) int {
		r := httptest.NewRequest("POST", "/api/outputs/repair", strings.NewReader(body))
		if authenticated {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if status := request(string(body), false); status != http.StatusUnauthorized {
		t.Fatal(status)
	}
	if status := request(`{"paths":[]}`, true); status != 400 {
		t.Fatal(status)
	}
	if err := os.Remove(filepath.Join(f.output, filepath.FromSlash(key))); err != nil {
		t.Fatal(err)
	}
	if status := request(string(body), true); status != 200 {
		t.Fatal(status)
	}
	if _, err := os.Stat(filepath.Join(f.output, filepath.FromSlash(key))); err != nil {
		t.Fatal("API did not restore the selected file", err)
	}
	if err := f.s.saveJob(Job{ID: "pending-repair-test", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if status := request(string(body), true); status != 409 {
		t.Fatal(status)
	}
}
