//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupHTTPImportConfirmCancelRestartReviewAndRollback(t *testing.T) {
	f, _, snapshot := backupFixture(t)
	var encrypted bytes.Buffer
	if _, err := writeBackupArchive(context.Background(), &encrypted, snapshot, backupTestPassword, currentSchemaVersion); err != nil {
		t.Fatal(err)
	}
	s, h, cookie := testToolsServer(t, f, f.c)
	in := backupRequest{AdminPassword: "fixture-admin-password", BackupPassword: backupTestPassword}
	result := backupMultipartRequest(t, h, cookie, in, encrypted.Bytes())
	if result.Code != 200 {
		t.Fatal(result.Code, result.Body.String())
	}
	var upload struct{ ID, Revision string }
	if err := json.Unmarshal(result.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	blocked := backupMultipartRequest(t, h, cookie, in, encrypted.Bytes())
	if blocked.Code != 409 {
		t.Fatal("multiple plaintext candidates allowed", blocked.Code)
	}
	confirm := backupRequest{AdminPassword: in.AdminPassword, ID: upload.ID, Revision: strings.Repeat("0", 64), Confirm: "RESTORE"}
	if w := generationPOST(h, cookie, "/api/restore/confirm", confirm); w.Code != 409 {
		t.Fatal("stale revision accepted", w.Code)
	}
	confirm.Revision = upload.Revision
	if w := generationPOST(h, cookie, "/api/restore/confirm", confirm); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := generationPOST(h, cookie, "/api/restore/cancel", backupRequest{AdminPassword: in.AdminPassword, ID: upload.ID}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := os.Lstat(filepath.Join(f.data, restoreControlName, upload.ID)); !os.IsNotExist(err) {
		t.Fatal("cancelled candidate session retained", err)
	}
	// Clear auth attempt window after its independent checks, then run activation.
	s.mu.Lock()
	clear(s.attempts)
	s.mu.Unlock()
	result = backupMultipartRequest(t, h, cookie, in, encrypted.Bytes())
	if result.Code != 200 {
		t.Fatal(result.Code, result.Body.String())
	}
	if err := json.Unmarshal(result.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	confirm.ID, confirm.Revision = upload.ID, upload.Revision
	if w := generationPOST(h, cookie, "/api/restore/confirm", confirm); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Simulate main's controlled shutdown then pre-Store startup recovery.
	f.e.Close()
	f.s.db.Close()
	m, err := openRestoreManager(f.data)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	runtime, err := m.recoverStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	current, err := openStoreForStartup(f.data, runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer current.db.Close()
	e, err := newEngine(context.Background(), current, f.c, f.output)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	restored, err := newServer(current, e, f.c, f.data, f.output)
	if err != nil {
		t.Fatal(err)
	}
	h = restored.handler()
	login := authLogin(h, in.AdminPassword)
	if login.Code != 200 {
		t.Fatal(login.Code)
	}
	cookie = login.Result().Cookies()[0]
	w := generationPOST(h, cookie, "/api/restore/review", backupRequest{AdminPassword: in.AdminPassword, ID: runtime.ID, Generation: runtime.Generation, Confirm: "REVIEWED"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !current.restoreRestricted {
		t.Fatal("review started workers in existing process")
	}
	p, err := m.loadJournal()
	if err != nil || p.Runtime.State != "released_new" {
		t.Fatal("review release not durable", err)
	}
	current.db.Close()
	e.Close()
	if err = runRestoreRollbackCLI(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	runtime, err = m.recoverStartup(context.Background())
	if err != nil || runtime.State != "rolled_back" || !runtime.held() {
		t.Fatal("CLI rollback failed", err)
	}
}

func TestRestoreUploadCrashResidueCleanupKeepsArmedHistory(t *testing.T) {
	f := restoreRawFixture(t, false)
	orphan := strings.Repeat("d", 32)
	if err := f.m.mkdir(path.Join(restoreControlName, orphan)); err != nil {
		t.Fatal(err)
	}
	if err := f.m.writeEnvelope(path.Join(restoreControlName, orphan, "unarmed.json"), restoreUpload{ID: orphan}, "unarmed"); err != nil {
		t.Fatal(err)
	}
	restoreWriteTestFile(t, f.m, path.Join(restoreControlName, orphan, "orphan-private-data"), "decrypted orphan")
	if err := f.m.uploadSlotAvailable(); err == nil {
		t.Fatal("pending operation did not block upload")
	}
	if _, err := f.m.root.Lstat(path.Join(restoreControlName, orphan)); !os.IsNotExist(err) {
		t.Fatal("orphan not cleaned", err)
	}
	if _, err := f.m.root.Lstat(path.Join(restoreControlName, f.id, "candidate")); err != nil {
		t.Fatal("armed candidate removed", err)
	}
	if err := runRestoreRollbackCLI(context.Background(), f.m); err != nil {
		t.Fatal("pending CLI rollback should cancel", err)
	}
	p, err := f.m.loadJournal()
	if err != nil || p != nil {
		t.Fatal("pending CLI rollback activated restore", err)
	}
	if err = f.m.verifyInventory(context.Background(), ".", f.old, true); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreReadOnlyGuardBlocksCrossOriginImport(t *testing.T) {
	f := setup(t)
	_, h, cookie := testToolsServer(t, f, f.c)
	r := httptest.NewRequest("POST", "/api/restore/import", strings.NewReader("invalid"))
	r.Header.Set("Origin", "https://foreign.example")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
