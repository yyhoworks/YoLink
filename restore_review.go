package main

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"net/http"
	"os"
	"strings"
)

// Restrict effects while permitting local inspection, password recovery and
// explicit read-only provider diagnostics. Automatic work and normal playback
// remain disabled until review plus restart. Every new mutation defaults denied.
func restoreReviewRequestAllowed(r *http.Request) bool {
	if r.Method == "GET" || r.Method == "HEAD" {
		// These GETs resolve provider/media URLs or export usable playback links.
		if strings.HasPrefix(r.URL.Path, "/api/resolve/") || strings.HasSuffix(r.URL.Path, "/playlist") || r.URL.Path == "/api/automation" {
			return false
		}
		return true
	}
	switch r.URL.Path {
	case "/api/logout", "/api/password", "/api/backups/export", "/api/restore/import", "/api/restore/confirm", "/api/restore/cancel", "/api/restore/review", "/api/restore/rollback", "/api/restore/restart", "/api/playback/cache/clear":
		return true
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 4 && parts[0] == "api" {
		if parts[1] == "accounts" && (parts[3] == "check" || parts[3] == "quota") {
			return true
		}
		if parts[1] == "media" && parts[3] == "diagnose" {
			return true
		}
	}
	return false
}

// Caller is holding the immutable startup hold and operation mutex. Reapply
// quarantine in one DB transaction before releasing the external journal hold.
// A crash between these steps stays restricted on restart.
func (s *Server) finishRestoreReview(ctx context.Context, id, generation, adminPassword string) error {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if !s.passwordMatches(adminPassword) {
		return errors.New("当前管理员密码不正确")
	}
	if !s.s.restoreRestricted || s.s.startupRestore.ID != id || s.s.startupRestore.Generation != generation {
		return errors.New("恢复核对会话已变化")
	}
	m, err := openRestoreManager(s.s.dir)
	if err != nil {
		return err
	}
	defer m.Close()
	p, err := m.loadJournal()
	if err != nil {
		return err
	}
	if p == nil || p.Runtime.ID != id || p.Runtime.Generation != generation || !restoreTerminal(p.Runtime.State) {
		return errors.New("恢复会话尚未完成")
	}
	if p.Runtime.State == "released_new" || p.Runtime.State == "released_old" {
		return nil
	}
	tx, err := s.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = quarantineBackupPayloads(ctx, tx); err != nil {
		return err
	}
	for _, query := range []string{
		`UPDATE ingest_events SET status='failed',error='恢复核对完成，历史事件未自动重放',revision=revision+1 WHERE status='pending'`,
		`UPDATE deliveries SET status='cancelled',next_at=0,error='恢复核对完成，历史投递未自动重放' WHERE status IN ('pending','paused')`,
		`UPDATE event_clients SET enabled=0`,
		`INSERT INTO settings(id,value) VALUES('event_legacy_enabled','0') ON CONFLICT(id) DO UPDATE SET value='0'`,
		`INSERT INTO settings(id,value) VALUES('restore_review_required','0') ON CONFLICT(id) DO UPDATE SET value='0'`,
	} {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Old deployments may have used only an initial-password file or env. The
	// current reauthenticated password must survive a later host/env change.
	if s.adminCredential == nil {
		credential := &adminCredential{Version: 1, Iterations: 600000, Salt: make([]byte, 16)}
		if _, err = rand.Read(credential.Salt); err != nil {
			return err
		}
		credential.Hash, err = pbkdf2.Key(sha256.New, adminPassword, credential.Salt, credential.Iterations, 32)
		if err != nil {
			return err
		}
		if err = saveAdminCredential(s.s.dir, credential); err != nil {
			return err
		}
		s.adminCredential = credential
	}
	if err = syncRestoredRuntime(ctx, s.s); err != nil {
		return err
	}
	return m.releaseReview(id, generation)
}

func syncRestoredRuntime(ctx context.Context, s *Store) error {
	var busy, log, checkpointed int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(FULL)").Scan(&busy, &log, &checkpointed); err != nil || busy != 0 || log != checkpointed {
		return errors.New("恢复核对数据库尚未完整提交")
	}
	m, err := openRestoreManager(s.dir)
	if err != nil {
		return err
	}
	defer m.Close()
	for _, name := range []string{"strmlink.db", "strmlink.db-wal", "admin-credential.json"} {
		f, e := m.root.OpenFile(name, os.O_RDWR, 0)
		if e != nil {
			if strings.HasSuffix(name, "-wal") && os.IsNotExist(e) {
				continue
			}
			return e
		}
		e = f.Sync()
		closed := f.Close()
		if e != nil {
			return e
		}
		if closed != nil {
			return closed
		}
	}
	return m.syncDir(".")
}
