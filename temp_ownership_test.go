package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func temporaryFixture(t *testing.T) temporaryFiles {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return temporaryFiles{root: root}
}

func TestBackupOwnershipNeverAdoptsPreexistingTargetOrClaim(t *testing.T) {
	for _, existing := range []string{"target", "claim"} {
		t.Run(existing, func(t *testing.T) {
			o := temporaryFixture(t)
			id := strings.Repeat("a", 32)
			target, claim, _ := temporaryNames("export", id)
			if err := o.ensureParent(backupWorkName); err != nil {
				t.Fatal(err)
			}
			if existing == "target" {
				if err := o.root.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := o.root.WriteFile(target+"/unrelated", []byte("operator file"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := o.root.WriteFile(claim, []byte("unrelated"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := o.create("export", id); !errors.Is(err, os.ErrExist) {
				t.Fatal("pre-existing identity adopted", err)
			}
			if existing == "target" {
				if _, err := o.root.Lstat(claim); !os.IsNotExist(err) {
					t.Fatal("claim retrospectively granted", err)
				}
				if raw, err := o.root.ReadFile(target + "/unrelated"); err != nil || string(raw) != "operator file" {
					t.Fatal("unrelated file changed", err)
				}
			}
		})
	}
}

func TestBackupOwnershipBudgetIncludesLiveAncestorLists(t *testing.T) {
	o := temporaryFixture(t)
	target, err := o.create("export", strings.Repeat("f", 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"first", "second"} {
		if err = o.root.Mkdir(target+"/"+dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b"} {
			if err = o.root.WriteFile(target+"/"+dir+"/"+name, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Model other live ancestor allocations without creating 400k files. This
	// frame's two names + its first child's two names exceed the three free slots.
	budget := temporaryWalkBudget{remaining: 100, pending: temporaryPendingLimit - 3}
	if err = o.walk(target, 0, &budget, false); err == nil {
		t.Fatal("freed budget for still-live ancestor lists")
	}
	if budget.pending != temporaryPendingLimit-3 {
		t.Fatal("failed traversal leaked its allocation budget", budget.pending)
	}
	if err = o.cleanup("export", strings.Repeat("f", 32)); err != nil {
		t.Fatal("ordinary safe cleanup failed", err)
	}
}

func TestBackupOwnershipPublicationFailureCreatesNoPlaintext(t *testing.T) {
	for _, point := range []string{"ownership.claim.before_write", "ownership.claim.before_sync", "ownership.claim.before_publish", "ownership.claim.after_publish", "ownership.claim.durable", "ownership.target.before_mkdir"} {
		t.Run(point, func(t *testing.T) {
			o := temporaryFixture(t)
			id := strings.Repeat("a", 32)
			interrupted := errors.New("injected disk-full/publication failure")
			o.fault = func(p string) error {
				if p == point {
					return interrupted
				}
				return nil
			}
			if _, err := o.create("export", id); !errors.Is(err, interrupted) {
				t.Fatal("fault not reached", err)
			}
			target, _, _ := temporaryNames("export", id)
			if _, err := o.root.Lstat(target); !os.IsNotExist(err) {
				t.Fatal("plaintext target created before successful claim publication", err)
			}
			o.fault = nil
			if err := o.cleanupExports(); err != nil {
				t.Fatal(err)
			}
			if err := o.cleanupExports(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBackupOwnershipPartialDeletionAndFinalUnlinkConverge(t *testing.T) {
	for _, phase := range []string{"child-deleted", "before-parent-sync", "before-claim-unlink", "after-claim-unlink", "after-final-sync"} {
		t.Run(phase, func(t *testing.T) {
			o := temporaryFixture(t)
			id := strings.Repeat("b", 32)
			target, err := o.create("export", id)
			if err != nil {
				t.Fatal(err)
			}
			if err = o.root.WriteFile(target+"/private", []byte("private"), 0600); err != nil {
				t.Fatal(err)
			}
			interrupted := errors.New("interrupted cleanup")
			parentSyncs := 0
			hit := false
			o.fault = func(point string) error {
				if point == "ownership.dir.sync.before:"+backupWorkName {
					parentSyncs++
				}
				wanted := phase == "child-deleted" && point == "ownership.remove.after:"+target+"/private" ||
					phase == "before-parent-sync" && point == "ownership.dir.sync.before:"+backupWorkName && parentSyncs == 1 ||
					phase == "before-claim-unlink" && point == "ownership.claim.before_remove" ||
					phase == "after-claim-unlink" && point == "ownership.claim.after_remove" ||
					phase == "after-final-sync" && point == "ownership.dir.sync.after:"+backupWorkName && parentSyncs == 2
				if wanted {
					hit = true
					return interrupted
				}
				return nil
			}
			if err = o.cleanup("export", id); !errors.Is(err, interrupted) || !hit {
				t.Fatal("cleanup fault not observable", err)
			}
			_, claim, _ := temporaryNames("export", id)
			if phase == "child-deleted" || phase == "before-parent-sync" || phase == "before-claim-unlink" {
				if err = (temporaryFiles{root: o.root}).readClaim("export", id); err != nil {
					t.Fatal("claim lost before deletion durability", err)
				}
			} else if _, err = o.root.Lstat(claim); !os.IsNotExist(err) {
				t.Fatal("post-unlink contract incorrectly recreated claim", err)
			}
			fresh := temporaryFiles{root: o.root}
			for i := 0; i < 2; i++ {
				if err = fresh.cleanupExports(); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{target, claim} {
				if _, err = o.root.Lstat(name); !os.IsNotExist(err) {
					t.Fatal("cleanup did not converge", name, err)
				}
			}
		})
	}
}

func TestBackupOwnershipCorruptOversizedAndUnknownRecordsFailClosed(t *testing.T) {
	for _, malformed := range []string{"bad-json", "oversized", "wrong-identity"} {
		t.Run(malformed, func(t *testing.T) {
			o := temporaryFixture(t)
			id := strings.Repeat("a", 32)
			target, err := o.create("export", id)
			if err != nil {
				t.Fatal(err)
			}
			_, claim, _ := temporaryNames("export", id)
			raw := []byte("invalid")
			if malformed == "oversized" {
				raw = []byte(strings.Repeat(" ", temporaryClaimMax+1))
			}
			if malformed == "wrong-identity" {
				other := strings.Repeat("b", 32)
				if _, err = o.create("export", other); err != nil {
					t.Fatal(err)
				}
				_, otherClaim, _ := temporaryNames("export", other)
				raw, err = o.root.ReadFile(otherClaim)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = o.root.WriteFile(claim, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err = o.cleanupExports(); err == nil {
				t.Fatal("bad claim accepted")
			}
			if _, err = o.root.Lstat(target); err != nil {
				t.Fatal("invalid ownership allowed deletion", err)
			}
		})
	}
	o := temporaryFixture(t)
	if err := o.ensureParent(backupWorkName); err != nil {
		t.Fatal(err)
	}
	unknown := path.Join(backupWorkName, strings.Repeat("c", 32))
	if err := o.root.Mkdir(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	if err := o.cleanupExports(); err != nil {
		t.Fatal(err)
	}
	if _, err := o.root.Lstat(unknown); err != nil {
		t.Fatal("unclaimed operator directory deleted", err)
	}
}

func TestBackupHTTPPlaintextCleanupFailsBeforeSuccessHeaders(t *testing.T) {
	f := setup(t)
	s, h, cookie := testToolsServer(t, f, f.c)
	s.backupFault = func(point string) error {
		if strings.HasPrefix(point, "ownership.remove.before:") && strings.Contains(point, ".backup-snapshot-") {
			return errors.New("disk remove denied")
		}
		return nil
	}
	w := generationPOST(h, cookie, "/api/backups/export", object{"admin_password": "fixture-admin-password", "backup_password": backupTestPassword})
	if w.Code != 500 || w.Header().Get("Content-Disposition") != "" || strings.Contains(w.Header().Get("Content-Type"), "octet-stream") {
		t.Fatal("success published before plaintext cleanup", w.Code)
	}
	s.backupFault = nil
	m, err := openRestoreManager(f.data)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err = m.recoverStartup(context.Background()); err != nil {
		t.Fatal("startup cleanup without journal failed", err)
	}
	if _, err = m.recoverStartup(context.Background()); err != nil {
		t.Fatal("second startup cleanup failed", err)
	}
	entries, err := os.ReadDir(filepath.Join(f.data, backupWorkName))
	if err != nil || len(entries) != 0 {
		t.Fatal("plaintext cleanup left owned work", len(entries), err)
	}
}

func TestBackupHTTPCanceledExportStillCleansWorkspace(t *testing.T) {
	for _, point := range []string{"export.snapshot.created", "export.encrypted"} {
		t.Run(point, func(t *testing.T) {
			f := setup(t)
			s, h, cookie := testToolsServer(t, f, f.c)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.backupFault = func(p string) error {
				if p == point {
					cancel()
				}
				return nil
			}
			r := httptest.NewRequest("POST", "/api/backups/export", strings.NewReader(`{"admin_password":"fixture-admin-password","backup_password":"`+backupTestPassword+`"}`)).WithContext(ctx)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code == 200 {
				t.Fatal("canceled export returned success")
			}
			entries, err := os.ReadDir(filepath.Join(f.data, backupWorkName))
			if err != nil || len(entries) != 0 {
				t.Fatal("canceled request canceled its cleanup", len(entries), err)
			}
		})
	}
}

func TestBackupKilledExportHelper(t *testing.T) {
	if os.Getenv("YOLINK_KILLED_EXPORT") != "1" {
		t.Skip("subprocess only")
	}
	dir := os.Args[len(os.Args)-1]
	lock, err := acquireDataLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	store, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()
	if _, err = keyFile(dir, "playback.key", 32); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "fixture-admin-password")
	s, err := newServer(store, nil, nil, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	s.backupFault = func(point string) error {
		if point == "export.snapshot.created" {
			fmt.Println("snapshot-ready")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		}
		return nil
	}
	r := httptest.NewRequest("POST", "/api/backups/export", strings.NewReader(`{"admin_password":"fixture-admin-password","backup_password":"`+backupTestPassword+`"}`))
	s.exportBackup(httptest.NewRecorder(), r)
	t.Fatal("helper unexpectedly completed")
}

func TestBackupRealKilledExportFreshStartupTwicePreservesData(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "operator-file"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, ".backup-snapshot-operator")
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "keep"), []byte("unclaimed"), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestBackupKilledExportHelper$", "--", dir)
	cmd.Env = append(os.Environ(), "YOLINK_KILLED_EXPORT=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != "snapshot-ready" {
		t.Fatal("real HTTP export did not reach plaintext snapshot boundary")
	}
	contender := exec.CommandContext(ctx, exe, "-test.run=^TestDataLockRejectsSecondProcessHelper$", "--", dir)
	contender.Env = append(os.Environ(), "YOLINK_LOCK_PROBE=1")
	if result, err := contender.CombinedOutput(); err != nil {
		t.Fatal("competing startup could collect a live export", err, string(result))
	}
	before := map[string][]byte{}
	for _, name := range []string{"strmlink.db", "credentials.key", "playback.key", "events.key", "operator-file"} {
		before[name], err = os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("killed child exited successfully")
	}
	for i := 0; i < 2; i++ {
		lock, err := acquireDataLock(dir)
		if err != nil {
			t.Fatal(err)
		}
		m, err := openRestoreManager(dir)
		if err != nil {
			lock.Close()
			t.Fatal(err)
		}
		_, err = m.recoverStartup(context.Background())
		m.Close()
		lock.Close()
		if err != nil {
			t.Fatal("fresh pre-Store cleanup failed", err)
		}
	}
	for name, expected := range before {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(raw) != string(expected) {
			t.Fatal("cleanup changed original data", name, err)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(legacy, "keep")); err != nil || string(raw) != "unclaimed" {
		t.Fatal("unclaimed legacy directory touched", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, backupWorkName))
	if err != nil || len(entries) != 0 {
		t.Fatal("killed-export plaintext survived startup", len(entries), err)
	}
}

type canceledBackupWriter struct {
	header http.Header
	cancel context.CancelFunc
}

func (w *canceledBackupWriter) Header() http.Header       { return w.header }
func (w *canceledBackupWriter) WriteHeader(int)           {}
func (w *canceledBackupWriter) Write([]byte) (int, error) { w.cancel(); return 0, context.Canceled }

func TestBackupHTTPStreamingCancellationAndConcurrentOperations(t *testing.T) {
	f := setup(t)
	s, h, cookie := testToolsServer(t, f, f.c)
	lock, err := acquireDataLock(f.data)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ready, release := make(chan struct{}), make(chan struct{})
	s.backupFault = func(point string) error {
		if point == "export.snapshot.created" {
			close(ready)
			<-release
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("POST", "/api/backups/export", strings.NewReader(`{"admin_password":"fixture-admin-password","backup_password":"`+backupTestPassword+`"}`)).WithContext(ctx)
	r.AddCookie(cookie)
	w := &canceledBackupWriter{header: make(http.Header), cancel: cancel}
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(w, r) }()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("live export not ready")
	}
	blocked := generationPOST(h, cookie, "/api/backups/export", object{"admin_password": "fixture-admin-password", "backup_password": backupTestPassword})
	if blocked.Code != 409 {
		close(release)
		<-done
		t.Fatal("concurrent collector accepted while export is live", blocked.Code)
	}
	entries, err := os.ReadDir(filepath.Join(f.data, backupWorkName))
	if err != nil || len(entries) != 2 {
		close(release)
		<-done
		t.Fatal("live workspace collected", len(entries), err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stream cancellation did not finish")
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("cancellation during stream not reached")
	}
	entries, err = os.ReadDir(filepath.Join(f.data, backupWorkName))
	if err != nil || len(entries) != 0 {
		t.Fatal("stream cancellation left temporary files", len(entries), err)
	}
}

func TestBackupOwnershipBoundsAndSymlinksPreserveWholeTree(t *testing.T) {
	t.Run("bounded-control", func(t *testing.T) {
		o := temporaryFixture(t)
		id := strings.Repeat("d", 32)
		target, err := o.create("export", id)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < temporaryControlLimit; i++ {
			if err = o.root.WriteFile(fmt.Sprintf("%s/operator-%d", backupWorkName, i), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err = o.cleanupExports(); err == nil {
			t.Fatal("unbounded control directory accepted")
		}
		if _, err = o.root.Lstat(target); err != nil {
			t.Fatal("removed target before bounded validation", err)
		}
	})
	t.Run("nested-symlink", func(t *testing.T) {
		o := temporaryFixture(t)
		id := strings.Repeat("e", 32)
		target, err := o.create("export", id)
		if err != nil {
			t.Fatal(err)
		}
		if err = o.root.WriteFile(target+"/private", []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
		if err = o.root.Symlink("private", target+"/link"); err != nil {
			t.Skip("symlinks unavailable", err)
		}
		if err = o.cleanupExports(); err == nil {
			t.Fatal("nested symlink allowed")
		}
		if raw, err := o.root.ReadFile(target + "/private"); err != nil || string(raw) != "preserve" {
			t.Fatal("unsafe tree partially removed", err)
		}
	})
}
