//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type restoreFixture struct {
	m         *restoreManager
	id        string
	old, next restoreInventory
	dir       string
}

func restoreWriteTestFile(t *testing.T, m *restoreManager, name, content string) {
	t.Helper()
	if err := m.root.MkdirAll(path.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.root.WriteFile(name, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func restoreRawFixture(t *testing.T, empty bool) *restoreFixture {
	t.Helper()
	dir := t.TempDir()
	m, err := openRestoreManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	id := strings.Repeat("c", 32)
	if !empty {
		for _, name := range restoreTopFiles {
			if name == "admin-credential.json" {
				continue
			}
			content := "old-" + name
			if strings.HasSuffix(name, ".key") {
				content = strings.Repeat("o", 32)
			}
			restoreWriteTestFile(t, m, name, content)
		}
		restoreWriteTestFile(t, m, "strmlink.db-wal", "old-wal")
		restoreWriteTestFile(t, m, "strmlink.db-shm", "old-shm")
		restoreWriteTestFile(t, m, "plans/"+strings.Repeat("a", 32)+".json", "old-plan")
		restoreWriteTestFile(t, m, "strm-backups/"+strings.Repeat("a", 32)+"/old.strm", "old-recovery")
	}
	restoreWriteTestFile(t, m, "operator-notes.txt", "preserve-unrelated")
	old, err := collectRestoreInventory(context.Background(), m.root, true)
	if err != nil {
		t.Fatal(err)
	}
	base := path.Join(restoreControlName, id, "candidate")
	for _, name := range restoreTopFiles {
		if name == "initial-admin-password.txt" {
			continue
		}
		content := "new-" + name
		if strings.HasSuffix(name, ".key") {
			content = strings.Repeat("n", 32)
		}
		restoreWriteTestFile(t, m, path.Join(base, name), content)
	}
	restoreWriteTestFile(t, m, path.Join(base, "plans/"+strings.Repeat("b", 32)+".json"), "new-plan")
	restoreWriteTestFile(t, m, path.Join(base, "strm-backups/"+strings.Repeat("b", 32)+"/new.strm"), "new-recovery")
	next, err := m.inventoryAt(context.Background(), base, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.armCandidate(context.Background(), id, restoreRevision(next), next); err != nil {
		t.Fatal(err)
	}
	return &restoreFixture{m, id, old, next, dir}
}

func restoreAssertComplete(t *testing.T, f *restoreFixture) restoreRuntime {
	t.Helper()
	runtime, err := f.m.recoverStartup(context.Background())
	if err != nil {
		t.Fatal("recovery did not converge", err)
	}
	got, err := collectRestoreInventory(context.Background(), f.m.root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !sameRestoreInventory(got, f.old) && !sameRestoreInventory(got, f.next) {
		t.Fatal("mixed old/new managed set")
	}
	if !runtime.held() {
		t.Fatal("recovery opened unrestricted runtime")
	}
	notes, err := f.m.root.ReadFile("operator-notes.txt")
	if err != nil || string(notes) != "preserve-unrelated" {
		t.Fatal("unrelated file changed", err)
	}
	again, err := f.m.recoverStartup(context.Background())
	if err != nil || again != runtime {
		t.Fatal("repeated restart changed operation", err)
	}
	return runtime
}

func TestRestoreJournalInstallRollbackAndOriginalAbsence(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "populated", true: "empty"}[empty], func(t *testing.T) {
			f := restoreRawFixture(t, empty)
			r := restoreAssertComplete(t, f)
			if r.State != "installed" {
				t.Fatal(r.State)
			}
			if err := f.m.verifyInventory(context.Background(), ".", f.next, true); err != nil {
				t.Fatal(err)
			}
			// Runtime is permitted to mutate DB/auth and add new managed files.
			restoreWriteTestFile(t, f.m, "strmlink.db", "mutated-runtime-db")
			restoreWriteTestFile(t, f.m, "admin-credential.json", "mutated-runtime-auth")
			newPlan := "plans/" + strings.Repeat("d", 32) + ".json"
			restoreWriteTestFile(t, f.m, newPlan, "post-restore-plan")
			post, err := collectRestoreInventory(context.Background(), f.m.root, true)
			if err != nil {
				t.Fatal(err)
			}
			if again, err := f.m.recoverStartup(context.Background()); err != nil || again != r {
				t.Fatal("mutable terminal state rejected", err)
			}
			if err = f.m.requestRollback(f.id); err != nil {
				t.Fatal(err)
			}
			r = restoreAssertComplete(t, f)
			if r.State != "rolled_back" {
				t.Fatal(r.State)
			}
			if err = f.m.verifyInventory(context.Background(), ".", f.old, true); err != nil {
				t.Fatal(err)
			}
			if err = f.m.verifyInventory(context.Background(), path.Join(restoreControlName, f.id, "post"), post, true); err != nil {
				t.Fatal("post-state not preserved", err)
			}
			if err = f.m.verifyInventory(context.Background(), path.Join(restoreControlName, f.id, "rollback"), f.old, true); err != nil {
				t.Fatal("immutable baseline consumed", err)
			}
		})
	}
}

func TestRestoreJournalEveryInstallDurabilityFaultConverges(t *testing.T) {
	reference := restoreRawFixture(t, false)
	points := []string{}
	reference.m.fault = func(point string) error { points = append(points, point); return nil }
	if _, err := reference.m.recoverStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	reference.m.fault = nil
	// Every real journal publication, fsync, rename and removal is exercised.
	// Repeated mkdir checks are covered at the first occurrence per directory.
	seenMkdir := map[string]bool{}
	for index, point := range points {
		if strings.HasPrefix(point, "dir.mkdir") {
			if seenMkdir[point] {
				continue
			}
			seenMkdir[point] = true
		}
		t.Run(strings.ReplaceAll(point, "/", "_")+"-"+strconv.Itoa(index), func(t *testing.T) {
			f := restoreRawFixture(t, false)
			count := 0
			injected := errors.New("injected durability failure")
			f.m.fault = func(string) error {
				current := count
				count++
				if current == index {
					return injected
				}
				return nil
			}
			if _, err := f.m.recoverStartup(context.Background()); !errors.Is(err, injected) {
				t.Fatal("fault was not reached", index, point, err)
			}
			f.m.fault = nil
			restoreAssertComplete(t, f)
		})
	}
}

func TestRestoreRollbackRestartDoesNotReallocateReserve(t *testing.T) {
	f := restoreRawFixture(t, false)
	f.m.fault = func(point string) error {
		if point == "rename.after:strmlink.db" {
			return errors.New("install interrupted")
		}
		return nil
	}
	if _, err := f.m.recoverStartup(context.Background()); err == nil {
		t.Fatal("install did not interrupt")
	}
	f.m.fault = func(point string) error {
		if point == "reserve.released" {
			return errors.New("rollback interrupted after reserve release")
		}
		return nil
	}
	if _, err := f.m.recoverStartup(context.Background()); err == nil {
		t.Fatal("rollback did not interrupt")
	}
	f.m.fault = func(point string) error {
		if point == "reserve.before_allocate" {
			t.Fatal("rolling rollback attempted fresh space allocation")
		}
		return nil
	}
	r := restoreAssertComplete(t, f)
	if r.State != "rolled_back" {
		t.Fatal(r.State)
	}
}

func TestRestoreJournalRefusesCorruptionAndChangedRollbackBaseline(t *testing.T) {
	for _, kind := range []string{"journal", "candidate", "baseline"} {
		t.Run(kind, func(t *testing.T) {
			f := restoreRawFixture(t, false)
			switch kind {
			case "journal":
				restoreWriteTestFile(t, f.m, path.Join(restoreControlName, "active.json"), `{"data":{},"checksum":"bad"}`)
			case "candidate":
				restoreWriteTestFile(t, f.m, path.Join(restoreControlName, f.id, "candidate/credentials.key"), strings.Repeat("x", 32))
			case "baseline":
				f.m.fault = func(point string) error {
					if point == "journal.prepared.durable" {
						return errors.New("stop before install")
					}
					return nil
				}
				if _, err := f.m.recoverStartup(context.Background()); err == nil {
					t.Fatal("did not interrupt")
				}
				f.m.fault = nil
				restoreWriteTestFile(t, f.m, path.Join(restoreControlName, f.id, "rollback/credentials.key"), strings.Repeat("x", 32))
			}
			if _, err := f.m.recoverStartup(context.Background()); err == nil {
				t.Fatal("corrupt state allowed startup")
			}
			if err := f.m.verifyInventory(context.Background(), ".", f.old, true); err != nil {
				t.Fatal("pre-install failure changed active data", err)
			}
		})
	}
}

func TestRestoreCancelPreservesPriorHoldAndGeneration(t *testing.T) {
	f := restoreRawFixture(t, false)
	r := restoreAssertComplete(t, f)
	for _, state := range []string{"installed", "released_new"} {
		p, err := f.m.loadJournal()
		if err != nil {
			t.Fatal(err)
		}
		if err = f.m.saveJournal(p, state); err != nil {
			t.Fatal(err)
		}
		id := strings.Repeat("e", 32)
		if state == "released_new" {
			id = strings.Repeat("f", 32)
		}
		if err = f.m.copyInventory(context.Background(), path.Join(restoreControlName, f.id, "candidate"), path.Join(restoreControlName, id, "candidate"), f.next); err != nil {
			t.Fatal(err)
		}
		if err = f.m.armCandidate(context.Background(), id, restoreRevision(f.next), f.next); err != nil {
			t.Fatal(err)
		}
		if err = f.m.cancelPending(id); err != nil {
			t.Fatal(err)
		}
		again, err := f.m.recoverStartup(context.Background())
		if err != nil || again.ID != r.ID || again.Generation != r.Generation || again.State != state {
			t.Fatal("prior runtime state lost", again, err)
		}
	}
}

func TestRestoreKilledActivationHelper(t *testing.T) {
	if os.Getenv("YOLINK_ACTIVATION_KILL_POINT") == "" {
		t.Skip("subprocess only")
	}
	lock, err := acquireDataLock(os.Args[len(os.Args)-1])
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	m, err := openRestoreManager(os.Args[len(os.Args)-1])
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.fault = func(point string) error {
		if point == os.Getenv("YOLINK_ACTIVATION_KILL_POINT") {
			os.Exit(77)
		}
		return nil
	}
	if _, err = m.recoverStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Fatal("kill point not reached")
}

func TestRestoreKilledProcessRecovery(t *testing.T) {
	for _, point := range []string{"journal.installing.durable", "file.sync.before:credentials.key", "rename.after:strmlink.db", "journal.installed.after_rename"} {
		t.Run(point, func(t *testing.T) {
			f := restoreRawFixture(t, false)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestRestoreKilledActivationHelper$", "--", f.dir)
			cmd.Env = append(os.Environ(), "YOLINK_ACTIVATION_KILL_POINT="+point)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 77 {
				t.Fatalf("unexpected child exit: %v %s", err, out)
			}
			restoreAssertComplete(t, f)
			entries, err := os.ReadDir(filepath.Join(f.dir, restoreControlName, "scratch"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatal("known interrupted scratch not cleaned")
			}
		})
	}
}

func TestRestoreRealCandidateMutableRuntimeRollbackAndMaintenanceGeneration(t *testing.T) {
	_, _, snapshot := backupFixture(t)
	var encrypted bytes.Buffer
	if _, err := writeBackupArchive(context.Background(), &encrypted, snapshot, backupTestPassword, currentSchemaVersion); err != nil {
		t.Fatal(err)
	}
	oldData := t.TempDir()
	old, err := openStore(oldData)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(oldData, "initial-admin-password.txt"), []byte("old-initial-admin-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = keyFile(oldData, "events.key", 32); err != nil {
		t.Fatal(err)
	}
	if err = old.saveJob(Job{ID: "old-resumable", Rule: "old-rule", Status: "queued", Resumable: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = old.db.Exec("INSERT INTO deliveries(id,job,kind,status,next_at,payload) VALUES('old-delivery','old-resumable','library','pending',1,'{}')"); err != nil {
		t.Fatal(err)
	}
	if err = old.db.Close(); err != nil {
		t.Fatal(err)
	}
	m, err := openRestoreManager(oldData)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	candidate, err := m.prepareCandidate(context.Background(), bytes.NewReader(encrypted.Bytes()), backupTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.armPreparedCandidate(context.Background(), candidate.ID, candidate.Revision); err != nil {
		t.Fatal(err)
	}
	runtime, err := m.recoverStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	current, err := openStoreForStartup(oldData, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if !current.restoreRestricted {
		t.Fatal("installed runtime not held")
	}
	if err = current.setSetting("public_base", "http://changed.example.test:8796"); err != nil {
		t.Fatal(err)
	}
	credential := &adminCredential{Version: 1, Iterations: 600000, Salt: make([]byte, 16)}
	if _, err = rand.Read(credential.Salt); err != nil {
		t.Fatal(err)
	}
	credential.Hash, err = pbkdf2.Key(sha256.New, "changed-restored-password", credential.Salt, credential.Iterations, 32)
	if err != nil {
		t.Fatal(err)
	}
	if err = saveAdminCredential(oldData, credential); err != nil {
		t.Fatal(err)
	}
	if err = current.db.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		runtime, err = m.recoverStartup(context.Background())
		if err != nil {
			t.Fatal("mutable state rejected", err)
		}
		current, err = openStoreForStartup(oldData, runtime)
		if err != nil {
			t.Fatal(err)
		}
		if !current.restoreRestricted {
			t.Fatal("hold lost")
		}
		if err = current.db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// Release after the caller's quarantine/DB-marker transaction. A later start
	// uses the released journal while imported automatic work stays quarantined.
	db, err := sql.Open("sqlite", filepath.Join(oldData, "strmlink.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE settings SET value='0' WHERE id='restore_review_required'")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.releaseReview(runtime.ID, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	runtime, err = m.recoverStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	current, err = openStoreForStartup(oldData, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if current.restoreRestricted {
		t.Fatal("review did not release")
	}
	server := &Server{s: current}
	imported := path.Join(oldData, "plans", strings.Repeat("a", 32)+".json")
	raw, err := os.ReadFile(imported)
	if err != nil {
		t.Fatal(err)
	}
	var importedPlan MaintenancePlan
	if err = json.Unmarshal(raw, &importedPlan); err != nil {
		t.Fatal(err)
	}
	for _, handler := range []func(*httptest.ResponseRecorder){
		func(w *httptest.ResponseRecorder) {
			r := httptest.NewRequest("POST", "/apply", nil)
			r.SetPathValue("id", importedPlan.ID)
			server.applyPlan(w, r)
		},
		func(w *httptest.ResponseRecorder) {
			r := httptest.NewRequest("POST", "/rollback", nil)
			r.SetPathValue("id", importedPlan.ID)
			server.rollbackPlan(w, r)
		},
	} {
		w := httptest.NewRecorder()
		handler(w)
		if w.Code != 409 {
			t.Fatal("historical plan executable after release", w.Code)
		}
	}
	fresh := MaintenancePlan{ID: strings.Repeat("d", 32), Mode: "links", Created: time.Now().Unix()}
	if err = server.savePlan(fresh); err != nil {
		t.Fatal(err)
	}
	loaded, err := server.loadPlan(fresh.ID)
	if err != nil || loaded.RestoreGeneration != runtime.Generation {
		t.Fatal("fresh plan generation missing", err)
	}
	if err = current.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = m.requestRollback(runtime.ID); err != nil {
		t.Fatal(err)
	}
	runtime, err = m.recoverStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := openStoreForStartup(oldData, runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer rolled.db.Close()
	j, err := rolled.job("old-resumable")
	if err != nil || j.Resumes != 0 || !rolled.restoreRestricted {
		t.Fatal("old historical work replayed", err)
	}
	cloud := &backupDenyCloud{}
	e, err := newEngine(context.Background(), rolled, cloud, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.processIngest()
	e.processDeliveries()
	if cloud.calls.Load() != 0 {
		t.Fatal("rollback triggered provider")
	}
	var delivery string
	if err = rolled.db.QueryRow("SELECT status FROM deliveries WHERE id='old-delivery'").Scan(&delivery); err != nil || delivery != "pending" {
		t.Fatal("rollback delivery executed", err)
	}
	t.Setenv("STRMLINK_ADMIN_PASSWORD", "") // fixture export used an env password
	rollbackServer, err := newServer(rolled, e, cloud, oldData, e.root.Name())
	if err != nil {
		t.Fatal(err)
	}
	if err = rollbackServer.finishRestoreReview(context.Background(), runtime.ID, runtime.Generation, "old-initial-admin-password"); err != nil {
		t.Fatal("initial-password-only rollback review failed", err)
	}
	savedCredential, err := loadAdminCredential(oldData)
	if err != nil || savedCredential == nil || !(&Server{adminCredential: savedCredential}).passwordMatches("old-initial-admin-password") {
		t.Fatal("rollback verifier not materialized", err)
	}
	j, err = rolled.job("old-resumable")
	if err != nil || j.Resumable || j.Status != "interrupted" {
		t.Fatal("review did not quarantine rollback work", err)
	}
	postCredential, err := loadAdminCredential(filepath.Join(oldData, restoreControlName, runtime.ID, "post"))
	if err != nil || postCredential == nil || !(&Server{adminCredential: postCredential}).passwordMatches("changed-restored-password") {
		t.Fatal("changed post-state authentication lost", err)
	}
}

func TestRestoreManualRollbackCaptureFaultPreservesActiveState(t *testing.T) {
	f := restoreRawFixture(t, false)
	restoreAssertComplete(t, f)
	restoreWriteTestFile(t, f.m, "strmlink.db", "changed-after-install")
	post, err := collectRestoreInventory(context.Background(), f.m.root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.requestRollback(f.id); err != nil {
		t.Fatal(err)
	}
	f.m.fault = func(point string) error {
		if strings.HasPrefix(point, "copy.before:") && strings.Contains(point, "/post/") {
			return unix.ENOSPC
		}
		return nil
	}
	if _, err = f.m.recoverStartup(context.Background()); !errors.Is(err, unix.ENOSPC) {
		t.Fatal("capture fault not reached", err)
	}
	if err = f.m.verifyInventory(context.Background(), ".", post, true); err != nil {
		t.Fatal("failed capture changed active runtime", err)
	}
	f.m.fault = nil
	restoreAssertComplete(t, f)
}

func TestRestoreDiskFullBeforeInstallKeepsOldSet(t *testing.T) {
	if os.Getenv("YOLINK_TEST_DISK_FULL") != "1" {
		t.Skip("dedicated bounded tmpfs only")
	}
	f := restoreRawFixture(t, false)
	// Arm is durable already. Fill only the dedicated test filesystem, never a
	// host workspace/data mount. Runner mounts a 64MiB private tmpfs for this test.
	fill, err := os.OpenFile(filepath.Join(f.dir, "disk-fill"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	block := make([]byte, 1<<20)
	for i := 0; i < 80; i++ {
		if _, err = fill.Write(block); err != nil {
			break
		}
	}
	fill.Close()
	if err == nil {
		t.Fatal("private disk limit did not produce ENOSPC")
	}
	if _, err = f.m.recoverStartup(context.Background()); err == nil {
		t.Fatal("full disk allowed activation")
	}
	if err = f.m.verifyInventory(context.Background(), ".", f.old, true); err != nil {
		t.Fatal("full disk changed old set", err)
	}
	if err = os.Remove(filepath.Join(f.dir, "disk-fill")); err != nil {
		t.Fatal(err)
	}
	restoreAssertComplete(t, f)
}

func TestRestoreDiskFullAfterInstallUsesReserveBeforeJournal(t *testing.T) {
	if os.Getenv("YOLINK_TEST_DISK_FULL") != "1" {
		t.Skip("dedicated bounded tmpfs only")
	}
	f := restoreRawFixture(t, false)
	filled := false
	f.m.fault = func(point string) error {
		if point != "journal.installed.before_write" || filled {
			return nil
		}
		filled = true
		fill, err := os.OpenFile(filepath.Join(f.dir, "disk-fill"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer fill.Close()
		block := make([]byte, 1<<20)
		for i := 0; i < 80; i++ {
			if _, err = fill.Write(block); err != nil {
				break
			}
		}
		if err == nil {
			t.Fatal("private disk did not fill")
		}
		return nil // Let the real next journal write hit the full filesystem.
	}
	if _, err := f.m.recoverStartup(context.Background()); err == nil || !filled {
		t.Fatal("full install journal unexpectedly succeeded", err)
	}
	f.m.fault = nil
	// Keep filler in place. Recovery must free the preallocated reserve before
	// trying to publish any journal, then restore the immutable old baseline.
	r := restoreAssertComplete(t, f)
	if r.State != "rolled_back" {
		t.Fatal("full installation did not roll back", r.State)
	}
}

func TestRestorePublishesTerminalOnlyAfterNestedDirectorySync(t *testing.T) {
	f := restoreRawFixture(t, true)
	// No old directories exist to incidentally sync new parent entries during
	// deletion. Include an empty nested directory without any file to sync it.
	empty := path.Join(restoreControlName, f.id, "candidate/strm-backups/"+strings.Repeat("b", 32)+"/empty/deeper")
	if err := f.m.root.MkdirAll(empty, 0700); err != nil {
		t.Fatal(err)
	}
	var err error
	f.next, err = f.m.inventoryAt(context.Background(), path.Join(restoreControlName, f.id, "candidate"), false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.m.loadJournal()
	if err != nil {
		t.Fatal(err)
	}
	p.New = f.next
	if err = f.m.syncTree(context.Background(), path.Join(restoreControlName, f.id, "candidate")); err != nil {
		t.Fatal(err)
	}
	if err = f.m.saveJournal(p, "pending"); err != nil {
		t.Fatal(err)
	}
	synced := map[string]bool{}
	f.m.fault = func(point string) error {
		if strings.HasPrefix(point, "dir.sync.after:") {
			synced[strings.TrimPrefix(point, "dir.sync.after:")] = true
		}
		if point == "journal.installed.before_write" {
			for _, dir := range f.next.Dirs {
				if !synced[dir] {
					t.Fatalf("terminal state before directory sync: %s", dir)
				}
			}
		}
		return nil
	}
	if _, err := f.m.recoverStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreInvalidAuthenticatedCandidateCleansFreshSession(t *testing.T) {
	_, _, snapshot := backupFixture(t)
	if err := os.WriteFile(filepath.Join(snapshot, "strmlink.db"), []byte("invalid sqlite snapshot"), 0600); err != nil {
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
	for i := 0; i < 2; i++ {
		if _, err = m.prepareCandidate(context.Background(), bytes.NewReader(encrypted.Bytes()), backupTestPassword); err == nil {
			t.Fatal("invalid SQLite accepted")
		}
		entries, err := os.ReadDir(filepath.Join(m.root.Name(), restoreControlName))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatal("failed authenticated import retained plaintext session")
		}
	}
}

func restoreManualFaultFixture(t *testing.T) *restoreFixture {
	t.Helper()
	f := restoreRawFixture(t, false)
	restoreAssertComplete(t, f)
	restoreWriteTestFile(t, f.m, "strmlink.db", "post-runtime-database")
	restoreWriteTestFile(t, f.m, "admin-credential.json", "post-runtime-auth")
	restoreWriteTestFile(t, f.m, "plans/"+strings.Repeat("d", 32)+".json", "post-runtime-plan")
	if err := f.m.requestRollback(f.id); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRestoreEveryManualRollbackDurabilityFaultConverges(t *testing.T) {
	reference := restoreManualFaultFixture(t)
	points := []string{}
	reference.m.fault = func(point string) error { points = append(points, point); return nil }
	if _, err := reference.m.recoverStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	reference.m.fault = nil
	seenMkdir := map[string]bool{}
	for index, point := range points {
		if strings.HasPrefix(point, "dir.mkdir") {
			if seenMkdir[point] {
				continue
			}
			seenMkdir[point] = true
		}
		t.Run(strings.ReplaceAll(point, "/", "_")+"-"+strconv.Itoa(index), func(t *testing.T) {
			f := restoreManualFaultFixture(t)
			post, err := collectRestoreInventory(context.Background(), f.m.root, true)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			fault := errors.New("manual rollback durability fault")
			f.m.fault = func(string) error {
				current := count
				count++
				if current == index {
					return fault
				}
				return nil
			}
			if _, err = f.m.recoverStartup(context.Background()); !errors.Is(err, fault) {
				t.Fatal("manual fault not reached", point, err)
			}
			f.m.fault = nil
			r := restoreAssertComplete(t, f)
			if r.State != "rolled_back" {
				t.Fatal(r.State)
			}
			if err = f.m.verifyInventory(context.Background(), path.Join(restoreControlName, f.id, "post"), post, true); err != nil {
				t.Fatal("post-state not complete after rollback recovery", err)
			}
			if err = f.m.verifyInventory(context.Background(), path.Join(restoreControlName, f.id, "rollback"), f.old, true); err != nil {
				t.Fatal("baseline consumed", err)
			}
		})
	}
}

func TestRestoreCrashWALWriterHelper(t *testing.T) {
	if os.Getenv("YOLINK_CRASH_WAL_WRITER") != "1" {
		t.Skip("subprocess only")
	}
	dir := os.Args[len(os.Args)-1]
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if err = s.setSetting("wal-only-commit", "committed-before-crash"); err != nil {
		t.Fatal(err)
	}
	if err = s.saveJob(Job{ID: "wal-job", Status: "queued", Resumable: true}); err != nil {
		t.Fatal(err)
	}
	os.Exit(78) // Do not Close SQLite: preserve valid committed WAL-only changes.
}

func TestRestoreValidCommittedWALSurvivesRestoreAndManualRollback(t *testing.T) {
	_, _, snapshot := backupFixture(t)
	var encrypted bytes.Buffer
	if _, err := writeBackupArchive(context.Background(), &encrypted, snapshot, backupTestPassword, currentSchemaVersion); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	old, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = old.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = keyFile(dir, "events.key", 32); err != nil {
		t.Fatal(err)
	}
	oldKey, err := os.ReadFile(filepath.Join(dir, "credentials.key"))
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestRestoreCrashWALWriterHelper$", "--", dir)
	cmd.Env = append(os.Environ(), "YOLINK_CRASH_WAL_WRITER=1")
	result, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 78 {
		t.Fatalf("WAL child: %v %s", err, result)
	}
	wal, err := os.Stat(filepath.Join(dir, "strmlink.db-wal"))
	if err != nil || wal.Size() == 0 {
		t.Fatal("committed WAL missing", err)
	}
	// Immutable SQLite deliberately ignores WAL, proving this row is absent
	// from the main DB and the rollback must preserve the actual WAL contents.
	db, err := backupReadOnlyDB(filepath.Join(dir, "strmlink.db"))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM settings WHERE id='wal-only-commit'").Scan(&count)
	db.Close()
	if err != nil || count != 0 {
		t.Fatal("fixture row not WAL-only", err)
	}
	m, err := openRestoreManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	candidate, err := m.prepareCandidate(context.Background(), bytes.NewReader(encrypted.Bytes()), backupTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.armPreparedCandidate(context.Background(), candidate.ID, candidate.Revision); err != nil {
		t.Fatal(err)
	}
	r, err := m.recoverStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.requestRollback(r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = m.recoverStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := openStoreForStartup(dir, r)
	if err != nil {
		t.Fatal(err)
	}
	defer rolled.db.Close()
	if rolled.setting("wal-only-commit", "") != "committed-before-crash" {
		t.Fatal("committed WAL row lost")
	}
	j, err := rolled.job("wal-job")
	if err != nil || j.Resumes != 0 || !rolled.restoreRestricted {
		t.Fatal("WAL job replayed", err)
	}
	key, err := os.ReadFile(filepath.Join(dir, "credentials.key"))
	if err != nil || !bytes.Equal(key, oldKey) {
		t.Fatal("WAL restored with wrong key set", err)
	}
}

func TestRestoreLargeJournalCanPublishRollbackFromPhysicalReserve(t *testing.T) {
	if os.Getenv("YOLINK_TEST_DISK_FULL") != "1" {
		t.Skip("dedicated bounded tmpfs only")
	}
	m, err := openRestoreManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	p := restoreJournal{Format: 1, Schema: currentSchemaVersion, Runtime: restoreRuntime{strings.Repeat("a", 32), "installing", strings.Repeat("b", 32)}, Created: 1}
	for i := 0; i < 70000; i++ {
		p.New.Files = append(p.New.Files, backupFile{fmt.Sprintf("plans/%032x.json", i), 0, strings.Repeat("f", 64)})
	}
	for _, name := range []string{"strmlink.db", "credentials.key", "playback.key", "events.key", "admin-credential.json"} {
		size := int64(1)
		if strings.HasSuffix(name, ".key") {
			size = 32
		}
		p.New.Files = append(p.New.Files, backupFile{name, size, strings.Repeat("f", 64)})
	}
	p.New.Dirs = []string{"plans"}
	if err = m.mkdir(path.Join(restoreControlName, p.Runtime.ID)); err != nil {
		t.Fatal(err)
	}
	if err = m.saveJournal(&p, "installing"); err != nil {
		t.Fatal(err)
	}
	if err = m.reserve(&p); err != nil {
		t.Fatal(err)
	}
	fill, err := os.OpenFile(filepath.Join(m.root.Name(), "disk-fill"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	block := make([]byte, 1<<20)
	for i := 0; i < 80; i++ {
		if _, err = fill.Write(block); err != nil {
			break
		}
	}
	fill.Close()
	if err == nil {
		t.Fatal("private disk did not fill")
	}
	if err = m.releaseReserve(&p); err != nil {
		t.Fatal(err)
	}
	if err = m.saveJournal(&p, "rolling_back"); err != nil {
		t.Fatal("large transition journal does not fit in physical reserve", err)
	}
}
