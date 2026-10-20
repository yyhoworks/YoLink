package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func backupMultipartRequest(t *testing.T, h http.Handler, cookie *http.Cookie, in backupRequest, archive []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	field, err := w.CreateFormField("options")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(field).Encode(in); err != nil {
		t.Fatal(err)
	}
	file, err := w.CreateFormFile("archive", "synthetic.age")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/restore/import", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.AddCookie(cookie)
	result := httptest.NewRecorder()
	h.ServeHTTP(result, r)
	return result
}

func TestBackupHTTPExportAuthenticationExclusionAndEncryptedResponse(t *testing.T) {
	f := setup(t)
	s, h, cookie := testToolsServer(t, f, f.c)
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest("POST", "/api/backups/export", strings.NewReader(`{}`)))
	if unauth.Code != 401 {
		t.Fatal(unauth.Code)
	}
	wrong := generationPOST(h, cookie, "/api/backups/export", object{"admin_password": "wrong", "backup_password": backupTestPassword})
	if wrong.Code != 403 {
		t.Fatal(wrong.Code)
	}
	r := httptest.NewRequest("POST", "/api/backups/export", strings.NewReader(`{}`))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://foreign.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin export accepted")
	}
	s.backupMu.Lock()
	blocked := generationPOST(h, cookie, "/api/backups/export", object{"admin_password": "fixture-admin-password", "backup_password": backupTestPassword})
	s.backupMu.Unlock()
	if blocked.Code != 409 {
		t.Fatal("concurrent expensive operation accepted", blocked.Code)
	}
	result := generationPOST(h, cookie, "/api/backups/export", object{"admin_password": "fixture-admin-password", "backup_password": backupTestPassword})
	if result.Code != 200 || result.Header().Get("Cache-Control") != "no-store" || !strings.Contains(result.Header().Get("Content-Disposition"), ".age") {
		t.Fatal(result.Code, result.Body.String())
	}
	if bytes.Contains(result.Body.Bytes(), []byte("fixture-secret")) {
		t.Fatal("HTTP backup leaked plaintext")
	}
	stage, manifest, err := stageBackupArchive(t.Context(), bytes.NewReader(result.Body.Bytes()), t.TempDir(), backupTestPassword)
	if err != nil {
		t.Fatal("HTTP backup not decryptable", err)
	}
	if _, err = validateBackupSnapshot(t.Context(), stage, manifest); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreReviewGuardDefaultsToDenyMutationsAndPlayback(t *testing.T) {
	f := setup(t)
	s, h, cookie := testToolsServer(t, f, f.c)
	// Fixture worker remains idle; production restricted startup creates none.
	s.s.restoreRestricted = true
	for _, endpoint := range []string{"/api/jobs", "/api/outputs/repair", "/api/maintenance/" + strings.Repeat("a", 32) + "/apply", "/api/deliveries/old/retry", "/api/event-clients/legacy"} {
		w := generationPOST(h, cookie, endpoint, object{})
		if w.Code != 409 {
			t.Fatal("restricted mutation allowed", endpoint, w.Code)
		}
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/s/test/movie.mkv?k=bad", nil)
	h.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatal("restricted playback allowed", w.Code)
	}
	for _, endpoint := range []string{"/api/restore/status", "/api/state"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", endpoint, nil)
		req.AddCookie(cookie)
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal("safe inspection rejected", endpoint, w.Code)
		}
	}
}

func TestRestoreRestartRequiresReauthAndExplicitConfirmation(t *testing.T) {
	f := setup(t)
	s, h, cookie := testToolsServer(t, f, f.c)
	called := false
	s.restart = func() { called = true }
	for _, in := range []backupRequest{{AdminPassword: "wrong", Confirm: "RESTART"}, {AdminPassword: "fixture-admin-password"}} {
		w := generationPOST(h, cookie, "/api/restore/restart", in)
		if w.Code == 200 || called {
			t.Fatal("restart without valid confirmation")
		}
	}
	w := generationPOST(h, cookie, "/api/restore/restart", backupRequest{AdminPassword: "fixture-admin-password", Confirm: "RESTART"})
	if w.Code != 200 || !called {
		t.Fatal("confirmed restart not dispatched", w.Code)
	}
}
