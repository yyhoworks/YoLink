//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRestoreStatusAndNewUploadIgnoreConsumedPointers(t *testing.T) {
	f := restoreRawFixture(t, false)
	live := setup(t)
	s, handler, cookie := testToolsServer(t, live, live.c)
	s.s.dir = f.dir // Status needs only the fixture journal, not its synthetic DB.
	for _, state := range []string{"pending", "installed", "released_new"} {
		t.Run(state, func(t *testing.T) {
			p, err := f.m.loadJournal()
			if err != nil {
				t.Fatal(err)
			}
			if err = f.m.saveJournal(p, state); err != nil {
				t.Fatal(err)
			}
			if err = f.m.storeUpload(restoreCandidate{ID: f.id, Revision: restoreRevision(f.next)}); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, "/api/restore/status", nil)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			var result struct {
				Pending struct{ ID, State string }
				Upload  *restoreUpload
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Upload != nil || result.Pending.ID != f.id || result.Pending.State != state {
				t.Fatal("consumed pointer exposed as an unconfirmed candidate", w.Code, w.Body.String())
			}
			if state == "pending" {
				if err = f.m.uploadSlotAvailable(); err == nil {
					t.Fatal("pending restore allowed competing upload")
				}
				return
			}
			if err = f.m.uploadSlotAvailable(); err != nil {
				t.Fatal("consumed pointer blocked next upload", err)
			}
			if u, err := f.m.uploadedCandidate(); err != nil || u != nil {
				t.Fatal("consumed pointer not cleared", err)
			}
			if err = f.m.verifyInventory(context.Background(), path.Join(restoreControlName, f.id, "candidate"), f.next, false); err != nil {
				t.Fatal("consumed candidate evidence deleted", err)
			}
		})
	}
}

func TestRestoreHistoryAdmissionPreservesStartupAtLimit(t *testing.T) {
	for _, count := range []int{restoreHistoryLimit - 1, restoreHistoryLimit} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := restoreRawFixture(t, false)
			var prior *restoreJournal
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("%032x", i+1)
				next := &restoreJournal{Format: 1, Schema: currentSchemaVersion, Runtime: restoreRuntime{id, "released_new", id}, Created: time.Now().Unix(), New: f.next}
				if prior != nil {
					next.Previous = &prior.Runtime
					next.PreviousChecksum = restoreRevision(prior)
					if err := f.m.writeEnvelope(path.Join(restoreControlName, id, "prior.json"), prior, "prior"); err != nil {
						t.Fatal(err)
					}
				}
				prior = next
			}
			if err := f.m.saveJournal(prior, prior.Runtime.State); err != nil {
				t.Fatal(err)
			}
			before, err := f.m.root.ReadFile(path.Join(restoreControlName, "active.json"))
			if err != nil {
				t.Fatal(err)
			}
			err = f.m.armCandidate(context.Background(), f.id, restoreRevision(f.next), f.next)
			if count == restoreHistoryLimit {
				if err == nil || !strings.Contains(err.Error(), "上限") {
					t.Fatal("over-limit restore confirmation accepted", err)
				}
			} else {
				if err != nil {
					t.Fatal("last available history slot rejected", err)
				}
				if err = f.m.cancelRestore(context.Background(), f.id); err != nil {
					t.Fatal("last legal slot cannot be cancelled", err)
				}
			}
			after, err := f.m.root.ReadFile(path.Join(restoreControlName, "active.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("history boundary changed retained terminal state", err)
			}
			for i := 0; i < 2; i++ {
				if runtime, err := f.m.recoverStartup(context.Background()); err != nil || runtime != prior.Runtime {
					t.Fatal("boundary confirmation poisoned startup", runtime, err)
				}
			}
		})
	}
}

func restoreDeltaUnarmed(t *testing.T, m *restoreManager, id string) restoreCandidate {
	t.Helper()
	base, err := m.temporaryFiles().create("candidate", id)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.writeEnvelope(path.Join(base, "unarmed.json"), restoreUpload{ID: id}, "unarmed"); err != nil {
		t.Fatal(err)
	}
	if err = m.root.WriteFile(path.Join(base, "private"), []byte("preserve-candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	return restoreCandidate{ID: id, Revision: strings.Repeat("a", 64)}
}

func TestRestoreAmbiguousUploadPublicationProtectsCandidate(t *testing.T) {
	for _, phase := range []string{"journal.upload.after_rename", "dir.sync.before:" + restoreControlName} {
		t.Run(phase, func(t *testing.T) {
			m, err := openRestoreManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			candidate := restoreDeltaUnarmed(t, m, strings.Repeat("a", 32))
			injected := errors.New("upload publication sync interrupted")
			renamed := false
			m.fault = func(p string) error {
				if p == "journal.upload.after_rename" {
					renamed = true
				}
				if renamed && p == phase {
					return injected
				}
				return nil
			}
			if err = m.storeUpload(candidate); !errors.Is(err, injected) {
				t.Fatal("publication ambiguity not exercised", err)
			}
			m.fault = nil
			if err = m.cleanupCandidate(candidate.ID); err == nil {
				t.Fatal("ambiguous upload publication allowed deleting candidate")
			}
			for i := 0; i < 2; i++ {
				if _, err = m.recoverStartup(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if raw, err := m.root.ReadFile(path.Join(restoreControlName, candidate.ID, "private")); err != nil || string(raw) != "preserve-candidate" {
				t.Fatal("published upload deleted", err)
			}
			if err = m.discardUploaded(candidate.ID); err != nil {
				t.Fatal("explicit cancel failed", err)
			}
		})
	}
}

func TestRestoreUploadUnlinkSyncFailureDoesNotPublishCancellation(t *testing.T) {
	f := restoreRawFixture(t, false)
	if err := f.m.storeUpload(restoreCandidate{ID: f.id, Revision: restoreRevision(f.next)}); err != nil {
		t.Fatal(err)
	}
	before, err := f.m.root.ReadFile(path.Join(restoreControlName, "active.json"))
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("upload unlink parent fsync failed")
	f.m.fault = func(p string) error {
		if p == "dir.sync.before:"+restoreControlName {
			if _, err := f.m.root.Lstat(path.Join(restoreControlName, "upload.json")); os.IsNotExist(err) {
				return injected
			}
		}
		return nil
	}
	if err = f.m.cancelRestore(context.Background(), f.id); !errors.Is(err, injected) {
		t.Fatal("unlink sync failure not reached", err)
	}
	f.m.fault = nil
	if after, err := f.m.root.ReadFile(path.Join(restoreControlName, "active.json")); err != nil || !bytes.Equal(after, before) {
		t.Fatal("cancellation published after unlink sync failure", err)
	}
	if err = f.m.cleanupUnarmed(); err != nil {
		t.Fatal(err)
	}
	if err = f.m.verifyInventory(context.Background(), path.Join(restoreControlName, f.id, "candidate"), f.next, false); err != nil {
		t.Fatal("protected candidate deleted", err)
	}
	if err = f.m.cancelRestore(context.Background(), f.id); err != nil {
		t.Fatal("cancel retry", err)
	}
}

func TestRestoreExtraMultipartCleanupFailureRetriesOnStartup(t *testing.T) {
	f, _, snapshot := backupFixture(t)
	var encrypted bytes.Buffer
	if _, err := writeBackupArchive(context.Background(), &encrypted, snapshot, backupTestPassword, currentSchemaVersion); err != nil {
		t.Fatal(err)
	}
	s, h, cookie := testToolsServer(t, f, f.c)
	s.backupFault = func(p string) error {
		if strings.HasPrefix(p, "ownership.remove.before:") {
			return errors.New("unlink temporarily denied")
		}
		return nil
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	options, err := w.CreateFormField("options")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(options).Encode(backupRequest{AdminPassword: "fixture-admin-password", BackupPassword: backupTestPassword}); err != nil {
		t.Fatal(err)
	}
	file, err := w.CreateFormFile("archive", "synthetic.age")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(encrypted.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err = w.WriteField("unexpected", "extra part"); err != nil {
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
	if result.Code != 500 || !strings.Contains(result.Body.String(), "候选清理未完成") {
		t.Fatal("cleanup failure not observable", result.Code, result.Body.String())
	}
	s.backupFault = nil
	m, err := openRestoreManager(f.data)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	entries, err := m.temporaryFiles().entries(restoreControlName, temporaryControlLimit)
	if err != nil {
		t.Fatal(err)
	}
	claims := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "unarmed-") {
			claims++
		}
	}
	if claims != 1 {
		t.Fatal("ownership proof not retained", claims)
	}
	for i := 0; i < 2; i++ {
		if _, err = m.recoverStartup(context.Background()); err != nil {
			t.Fatal("rejection cleanup retry", err)
		}
	}
	entries, err = m.temporaryFiles().entries(restoreControlName, temporaryControlLimit)
	if err != nil || len(entries) != 0 {
		t.Fatal("rejected plaintext survived", len(entries), err)
	}
}

func TestRestoreDeepPreparedCandidateCancellationCleans(t *testing.T) {
	_, _, snapshot := backupFixture(t)
	name := backupDeepName(80)
	full := filepath.Join(snapshot, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("private deep baseline"), 0600); err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if _, err := writeBackupArchive(context.Background(), &encrypted, snapshot, backupTestPassword, currentSchemaVersion); err != nil {
		t.Fatal(err)
	}
	m, err := openRestoreManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	candidate, err := m.prepareCandidate(context.Background(), bytes.NewReader(encrypted.Bytes()), backupTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.storeUpload(candidate); err != nil {
		t.Fatal(err)
	}
	if err = m.cancelRestore(context.Background(), candidate.ID); err != nil {
		t.Fatal("valid deep candidate could not cancel", err)
	}
	for i := 0; i < 2; i++ {
		if _, err = m.recoverStartup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = m.root.Lstat(path.Join(restoreControlName, candidate.ID)); !os.IsNotExist(err) {
		t.Fatal("cancelled deep candidate remains", err)
	}
}

func TestRestoreLegacyDeepCleanupLowFDHelper(t *testing.T) {
	if os.Getenv("YOLINK_LEGACY_DEEP_LOW_FD") != "1" {
		t.Skip("subprocess only")
	}
	dir := os.Args[len(os.Args)-1]
	// Older staging created many ancestors before an archive error. Also cover
	// migration from the internal marker rather than requiring a new external one.
	m, err := openRestoreManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	id := strings.Repeat("a", 32)
	base := path.Join(restoreControlName, id)
	if err = m.mkdir(base); err != nil {
		t.Fatal(err)
	}
	if err = m.writeEnvelope(path.Join(base, "unarmed.json"), restoreUpload{ID: id}, "unarmed"); err != nil {
		t.Fatal(err)
	}
	deep := path.Join(base, ".restore-stage-interrupted", backupDeepName(180))
	if err = m.root.MkdirAll(path.Dir(deep), 0700); err != nil {
		t.Fatal(err)
	}
	if err = m.root.WriteFile(deep, []byte("legacy plaintext"), 0600); err != nil {
		t.Fatal(err)
	}
	var limit unix.Rlimit
	if err = unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	limit.Cur = 64
	if err = unix.Setrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = m.recoverStartup(context.Background()); err != nil {
			t.Fatal("legacy deep orphan cannot converge under fd limit", err)
		}
	}
	if _, err = m.root.Lstat(base); !os.IsNotExist(err) {
		t.Fatal("legacy plaintext remains", err)
	}
}

func TestRestoreLegacyDeepCleanupClosesAncestorHandles(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestRestoreLegacyDeepCleanupLowFDHelper$", "--", t.TempDir())
	cmd.Env = append(os.Environ(), "YOLINK_LEGACY_DEEP_LOW_FD=1")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(raw))
	}
}
