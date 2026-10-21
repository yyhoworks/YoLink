//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path"
	"strings"
	"testing"
)

func TestRestoreCancelledPartialCleanupSurvivesLostInternalMarker(t *testing.T) {
	f := restoreRawFixture(t, false)
	interrupted := errors.New("synthetic partial cleanup")
	f.m.fault = func(point string) error {
		if point != "cancel.state_published" {
			return nil
		}
		// A partial RemoveAll may delete this child before another unlink
		// fails. The next process must not need that deleted child as proof.
		if err := f.m.root.Remove(path.Join(restoreControlName, f.id, "unarmed.json")); err != nil {
			t.Fatal(err)
		}
		return interrupted
	}
	if err := f.m.cancelRestore(context.Background(), f.id); !errors.Is(err, interrupted) {
		t.Fatal("cancellation fault not reached", err)
	}
	f.m.fault = nil
	fresh, err := openRestoreManager(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err = fresh.uploadSlotAvailable(); err != nil {
		t.Fatal("cleanup could not resume", err)
	}
	if _, err = fresh.root.Lstat(path.Join(restoreControlName, f.id)); !os.IsNotExist(err) {
		t.Fatal("cancelled plaintext orphan survived after ownership marker was partially removed", err)
	}
	if err = fresh.verifyInventory(context.Background(), ".", f.old, true); err != nil {
		t.Fatal("cleanup changed active data", err)
	}
}

func TestRestoreHistoricalCandidateReplayCannotRewritePriorEvidence(t *testing.T) {
	_, _, snapshot := backupFixture(t)
	var encrypted bytes.Buffer
	ctx := context.Background()
	if _, err := writeBackupArchive(ctx, &encrypted, snapshot, backupTestPassword, currentSchemaVersion); err != nil {
		t.Fatal(err)
	}
	m, err := openRestoreManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	install := func() restoreCandidate {
		t.Helper()
		candidate, err := m.prepareCandidate(ctx, bytes.NewReader(encrypted.Bytes()), backupTestPassword)
		if err != nil {
			t.Fatal(err)
		}
		if err = m.armPreparedCandidate(ctx, candidate.ID, candidate.Revision); err != nil {
			t.Fatal(err)
		}
		if runtime, err := m.recoverStartup(ctx); err != nil || runtime.State != "installed" {
			t.Fatal("candidate not installed in isolated data", runtime, err)
		}
		return candidate
	}
	a, b := install(), install()
	// Simulate stale internal and external markers left by interruption. History
	// must still win, including when a stale upload pointer also names A.
	if err = m.writeEnvelope(path.Join(restoreControlName, a.ID, "unarmed.json"), restoreUpload{ID: a.ID}, "stale-unarmed"); err != nil {
		t.Fatal(err)
	}
	if err = m.storeUpload(a); err != nil {
		t.Fatal(err)
	}
	p, err := m.loadJournal()
	if err != nil {
		t.Fatal(err)
	}
	rollbackA, err := m.inventoryAt(ctx, path.Join(restoreControlName, a.ID, "rollback"), true)
	if err != nil {
		t.Fatal(err)
	}
	rollbackB := p.Old
	names := []string{path.Join(restoreControlName, "active.json"), path.Join(restoreControlName, b.ID, "prior.json"), path.Join(restoreControlName, a.ID, "candidate.json"), path.Join(restoreControlName, "upload.json")}
	before := map[string][]byte{}
	for _, name := range names {
		before[name], err = m.root.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Even a lower-level call cannot reuse historical A after terminal A→B.
	if err = m.armCandidate(ctx, a.ID, a.Revision, a.Inventory); err == nil {
		t.Fatal("historical candidate A replayed after B")
	}
	if err = m.armPreparedCandidate(ctx, a.ID, a.Revision); err == nil {
		t.Fatal("used candidate accepted by preparation gate")
	}
	if err = m.discardUploaded(a.ID); err == nil {
		t.Fatal("stale upload pointer allowed deleting historical A")
	}
	if err = m.cancelRestore(ctx, a.ID); err == nil {
		t.Fatal("cancel allowed deleting historical A")
	}
	if err = m.cleanupUnarmed(); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		after, err := m.root.ReadFile(name)
		if err != nil || !bytes.Equal(before[name], after) {
			t.Fatal("replay changed retained journal/prior/candidate evidence", name, err)
		}
	}
	if err = m.verifyInventory(ctx, path.Join(restoreControlName, a.ID, "candidate"), a.Inventory, false); err != nil {
		t.Fatal("replay changed retained candidate", err)
	}
	if err = m.verifyInventory(ctx, ".", b.Inventory, true); err != nil {
		t.Fatal("replay changed active data", err)
	}
	if err = m.verifyInventory(ctx, path.Join(restoreControlName, a.ID, "rollback"), rollbackA, true); err != nil {
		t.Fatal("A rollback evidence changed", err)
	}
	if err = m.verifyInventory(ctx, path.Join(restoreControlName, b.ID, "rollback"), rollbackB, true); err != nil {
		t.Fatal("B rollback evidence changed", err)
	}
}

func TestRestoreCancellationCrashBoundariesPreserveProtection(t *testing.T) {
	for _, point := range []string{"cancel.upload_removed", "cancel.state_published"} {
		t.Run(point, func(t *testing.T) {
			f := restoreRawFixture(t, false)
			if err := f.m.storeUpload(restoreCandidate{ID: f.id, Revision: restoreRevision(f.next)}); err != nil {
				t.Fatal(err)
			}
			interrupted := errors.New("cancellation interrupted")
			f.m.fault = func(p string) error {
				if p == point {
					return interrupted
				}
				return nil
			}
			if err := f.m.cancelRestore(context.Background(), f.id); !errors.Is(err, interrupted) {
				t.Fatal("fault not reached", err)
			}
			f.m.fault = nil
			if upload, err := f.m.uploadedCandidate(); err != nil || upload != nil {
				t.Fatal("matching upload not durably removed first", err)
			}
			p, err := f.m.loadJournal()
			if err != nil {
				t.Fatal(err)
			}
			if point == "cancel.upload_removed" {
				if p == nil || p.Runtime.ID != f.id {
					t.Fatal("active protection removed before cancellation")
				}
				if err = f.m.cleanupUnarmed(); err != nil {
					t.Fatal(err)
				}
				if _, err = f.m.root.Lstat(path.Join(restoreControlName, f.id)); err != nil {
					t.Fatal("still-active candidate collected", err)
				}
				if err = f.m.cancelRestore(context.Background(), f.id); err != nil {
					t.Fatal(err)
				}
			} else if p != nil {
				t.Fatal("cancellation not published")
			}
			for i := 0; i < 2; i++ {
				if _, err = f.m.recoverStartup(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = f.m.root.Lstat(path.Join(restoreControlName, f.id)); !os.IsNotExist(err) {
				t.Fatal("cancelled plaintext survived", err)
			}
			if err = f.m.verifyInventory(context.Background(), ".", f.old, true); err != nil {
				t.Fatal("cancellation changed live data", err)
			}
		})
	}
}

func TestRestoreCorruptProtectionBlocksEveryCandidateDeletion(t *testing.T) {
	for _, source := range []string{"active", "upload", "prior"} {
		t.Run(source, func(t *testing.T) {
			f := restoreRawFixture(t, false)
			orphan := strings.Repeat("d", 32)
			base, err := f.m.temporaryFiles().create("candidate", orphan)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.m.writeEnvelope(path.Join(base, "unarmed.json"), restoreUpload{ID: orphan}, "unarmed"); err != nil {
				t.Fatal(err)
			}
			if err = f.m.root.WriteFile(path.Join(base, "private"), []byte("do not delete"), 0600); err != nil {
				t.Fatal(err)
			}
			switch source {
			case "active":
				err = f.m.root.WriteFile(path.Join(restoreControlName, "active.json"), []byte("corrupt"), 0600)
			case "upload":
				err = f.m.root.WriteFile(path.Join(restoreControlName, "upload.json"), []byte("corrupt"), 0600)
			case "prior":
				p, e := f.m.loadJournal()
				if e != nil {
					t.Fatal(e)
				}
				p.Previous = &restoreRuntime{ID: strings.Repeat("a", 32), State: "installed", Generation: strings.Repeat("b", 32)}
				p.PreviousChecksum = strings.Repeat("0", 64)
				if err = f.m.saveJournal(p, "pending"); err == nil {
					err = f.m.root.WriteFile(path.Join(restoreControlName, f.id, "prior.json"), []byte("corrupt"), 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			// Ordered slice, not a map: Go map iteration order is random,
			// and if one action ever succeeded it would change the state
			// the remaining actions are asserted against.
			for _, action := range []struct {
				name string
				run  func() error
			}{
				{"cleanup", f.m.cleanupUnarmed},
				{"direct", func() error { return f.m.cleanupCandidate(orphan) }},
				{"cancel-orphan", func() error { return f.m.cancelRestore(context.Background(), orphan) }},
				{"cancel-active", func() error { return f.m.cancelRestore(context.Background(), f.id) }},
				{"discard", func() error { return f.m.discardUploaded(orphan) }},
			} {
				if raw, err := f.m.root.ReadFile(path.Join(base, "private")); err != nil || string(raw) != "do not delete" {
					t.Fatal("protected state changed before action", action.name, err)
				}
				if err = action.run(); err == nil {
					t.Fatal("corruption allowed deletion path", action.name)
				}
			}
			if raw, err := f.m.root.ReadFile(path.Join(base, "private")); err != nil || string(raw) != "do not delete" {
				t.Fatal("corrupt protection allowed candidate deletion", err)
			}
		})
	}
}
