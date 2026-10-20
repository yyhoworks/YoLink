package main

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type restoreCandidate struct {
	ID         string           `json:"id"`
	Revision   string           `json:"revision"`
	Created    int64            `json:"created"`
	Validation backupValidation `json:"validation"`
	Inventory  restoreInventory `json:"inventory"`
}

func (m *restoreManager) prepareCandidate(ctx context.Context, source io.Reader, password string) (out restoreCandidate, err error) {
	if !restorePlatformSupported() {
		return out, errors.New("当前平台支持隔离验证，恢复激活需在 Linux 部署中进行")
	}
	out.ID = randomID()
	out.Created = time.Now().Unix()
	base := path.Join(restoreControlName, out.ID)
	if err = m.mkdir(base); err != nil {
		return out, err
	}
	// This session ID was freshly allocated and has never been armed. Transfer
	// ownership of successful archive staging here, including validation failures.
	defer func() {
		if err == nil {
			return
		}
		if cleanupErr := m.root.RemoveAll(base); cleanupErr != nil {
			err = errors.Join(err, errors.New("未启用恢复候选清理失败，需管理员清理遗留资料"))
		} else if syncErr := m.syncDir(restoreControlName); syncErr != nil {
			err = errors.Join(err, errors.New("未启用恢复候选清理尚未完整提交"))
		}
	}()
	if err = m.writeEnvelope(path.Join(base, "unarmed.json"), restoreUpload{ID: out.ID}, "unarmed"); err != nil {
		return out, err
	}
	stage, manifest, err := stageBackupArchive(ctx, source, filepath.Join(m.root.Name(), filepath.FromSlash(base)), password)
	if err != nil {
		return out, err
	}
	if _, err = validateBackupSnapshot(ctx, stage, manifest); err != nil {
		return out, err
	}
	if err = pauseRestoredWork(ctx, stage); err != nil {
		return out, err
	}
	if err = materializeRestoredVerifier(stage); err != nil {
		return out, err
	}
	if err = closeRestoreWAL(ctx, stage); err != nil {
		return out, err
	}
	stageRelative, err := filepath.Rel(m.root.Name(), stage)
	if err != nil || !filepath.IsLocal(stageRelative) {
		return out, errors.New("恢复隔离路径无效")
	}
	candidate := path.Join(base, "candidate")
	if err = m.root.Rename(filepath.ToSlash(stageRelative), candidate); err != nil {
		return out, err
	}
	if err = m.syncTree(ctx, candidate); err != nil {
		return out, err
	}
	out.Inventory, err = m.inventoryAt(ctx, candidate, false)
	if err != nil {
		return out, err
	}
	out.Validation, err = m.validateCandidate(ctx, candidate, out.Inventory)
	if err != nil {
		return out, err
	}
	out.Revision = restoreRevision(out.Inventory)
	if err = m.writeEnvelope(path.Join(base, "candidate.json"), out, "candidate"); err != nil {
		return out, err
	}
	if err = m.syncTree(ctx, base); err != nil {
		return out, err
	}
	return out, nil
}

func materializeRestoredVerifier(dir string) error {
	if _, err := os.Lstat(filepath.Join(dir, "admin-credential.json")); os.IsNotExist(err) {
		raw, e := readBackupSmallFile(filepath.Join(dir, "initial-admin-password.txt"), 4096)
		if e != nil {
			return e
		}
		c := &adminCredential{Version: 1, Iterations: 600000, Salt: make([]byte, 16)}
		if _, e = rand.Read(c.Salt); e != nil {
			return e
		}
		c.Hash, e = pbkdf2.Key(sha256.New, strings.TrimSpace(string(raw)), c.Salt, c.Iterations, 32)
		if e != nil {
			return e
		}
		if e = saveAdminCredential(dir, c); e != nil {
			return e
		}
	} else if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, "initial-admin-password.txt")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func closeRestoreWAL(ctx context.Context, dir string) error {
	db, err := sql.Open("sqlite", filepath.Join(dir, "strmlink.db"))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	var busy, log, checkpointed int
	if err = db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil || busy != 0 || log != checkpointed {
		return errors.New("隔离数据库检查点未完整提交")
	}
	var mode string
	if err = db.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode); err != nil || mode != "delete" {
		return errors.New("隔离数据库日志模式未安全封存")
	}
	if err = db.Close(); err != nil {
		return err
	}
	for _, name := range []string{"strmlink.db-wal", "strmlink.db-shm"} {
		if _, err = os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			return errors.New("隔离数据库仍有未封存日志")
		}
	}
	return nil
}

func (m *restoreManager) validateCandidate(ctx context.Context, prefix string, in restoreInventory) (backupValidation, error) {
	manifest := backupManifest{Format: backupFormatVersion, Version: appVersion, Schema: currentSchemaVersion, Created: time.Now().Unix(), Files: in.Files}
	result, err := validateBackupSnapshot(ctx, filepath.Join(m.root.Name(), filepath.FromSlash(prefix)), manifest)
	if err != nil {
		return result, err
	}
	db, err := backupReadOnlyDB(filepath.Join(m.root.Name(), filepath.FromSlash(prefix), "strmlink.db"))
	if err != nil {
		return result, err
	}
	defer db.Close()
	if err = checkRestoreQuarantine(ctx, db); err != nil {
		return result, err
	}
	return result, nil
}

func checkRestoreQuarantine(ctx context.Context, db *sql.DB) error {
	var gate string
	if err := db.QueryRowContext(ctx, "SELECT value FROM settings WHERE id='restore_review_required'").Scan(&gate); err != nil || gate != "1" {
		return errors.New("恢复保护标记缺失")
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM jobs WHERE status IN ('running','queued') OR json_extract(payload,'$.resumable') IS NOT NULL AND json_extract(payload,'$.resumable')<>0`,
		`SELECT COUNT(*) FROM rules WHERE json_extract(payload,'$.enabled')<>0`,
		`SELECT COUNT(*) FROM ingest_events WHERE status='pending'`,
		`SELECT COUNT(*) FROM deliveries WHERE status IN ('pending','paused')`,
		`SELECT COUNT(*) FROM event_clients WHERE enabled<>0`,
		`SELECT COUNT(*) FROM settings WHERE id='event_legacy_enabled' AND value<>'0'`,
	} {
		var count int
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 0 {
			return errors.New("恢复后仍有未隔离的历史工作")
		}
	}
	return nil
}

func (m *restoreManager) armPreparedCandidate(ctx context.Context, id, revision string) error {
	if !restoreIDValid(id) {
		return errors.New("恢复候选身份无效")
	}
	var unarmed restoreUpload
	if _, err := m.readEnvelope(path.Join(restoreControlName, id, "unarmed.json"), &unarmed); err != nil || unarmed.ID != id {
		return errors.New("恢复候选已被使用，请重新上传")
	}
	var candidate restoreCandidate
	if _, err := m.readEnvelope(path.Join(restoreControlName, id, "candidate.json"), &candidate); err != nil {
		return err
	}
	if candidate.ID != id || candidate.Revision != revision || candidate.Revision != restoreRevision(candidate.Inventory) {
		return errors.New("恢复候选版本已变化")
	}
	if _, err := m.validateCandidate(ctx, path.Join(restoreControlName, id, "candidate"), candidate.Inventory); err != nil {
		return err
	}
	if err := m.armCandidate(ctx, id, revision, candidate.Inventory); err != nil {
		return err
	}
	if err := m.root.Remove(path.Join(restoreControlName, id, "unarmed.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return m.syncDir(path.Join(restoreControlName, id))
}
