package main

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type schemaRunner interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
}

const currentSchemaVersion = 2

const baseSchema = `CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY, payload TEXT NOT NULL, secret BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS rules(id TEXT PRIMARY KEY, payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS media(id TEXT PRIMARY KEY, account TEXT NOT NULL, remote_id TEXT NOT NULL, source_path TEXT NOT NULL, payload TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS media_path ON media(account, source_path);
CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY, created INTEGER NOT NULL, status TEXT NOT NULL, payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS outputs(path TEXT PRIMARY KEY, media TEXT NOT NULL, hash TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS settings(id TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS account_reports(id TEXT PRIMARY KEY, payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ingest_events(id TEXT PRIMARY KEY,rule TEXT NOT NULL,source_path TEXT NOT NULL,status TEXT NOT NULL,job TEXT NOT NULL,error TEXT NOT NULL,created INTEGER NOT NULL,revision INTEGER NOT NULL);`

// Version zero includes all pre-versioned releases. Bring their schema up to
// the known baseline in ONE transaction; only then advance user_version.
func migrateLegacySchema(tx *sql.Tx) error {
	for _, schema := range []string{baseSchema, featureSchema, activitySchema} {
		if _, err := tx.Exec(schema); err != nil {
			return err
		}
	}
	if err := migrateConnectionSchema(tx); err != nil {
		return err
	}
	_, err := tx.Exec("INSERT OR IGNORE INTO settings(id,value) VALUES('metrics_since',?)", time.Now().UTC().Format(time.RFC3339))
	return err
}

func migrateSchema(db *sql.DB) error {
	return applySchemaMigrations(db, []func(*sql.Tx) error{migrateLegacySchema, migrateCompactCheckpoints})
}

func migrateCompactCheckpoints(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE checkpoint_snapshots(job TEXT PRIMARY KEY,folder TEXT NOT NULL,payload TEXT NOT NULL);
CREATE TABLE checkpoint_cursors(job TEXT PRIMARY KEY,position INTEGER NOT NULL CHECK(position>=0));
CREATE TABLE checkpoint_claims(job TEXT NOT NULL,path TEXT NOT NULL,media TEXT NOT NULL,PRIMARY KEY(job,path));
CREATE TRIGGER checkpoint_cleanup AFTER DELETE ON job_checkpoints BEGIN
 DELETE FROM checkpoint_snapshots WHERE job=OLD.job;
 DELETE FROM checkpoint_cursors WHERE job=OLD.job;
 DELETE FROM checkpoint_claims WHERE job=OLD.job;
END;`)
	return err
}

func applySchemaMigrations(db *sql.DB, migrations []func(*sql.Tx) error) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > len(migrations) {
		return errors.New("数据库版本高于当前程序或版本无效，请使用匹配版本；未修改数据库")
	}
	// Do not alter journal settings or create tables before the future-version
	// guard. Restore validation uses the same gate as ordinary startup.
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;"); err != nil {
		return err
	}
	for version < len(migrations) {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		// Recheck under the transaction rather than trusting the initial read.
		var current int
		err = tx.QueryRow("PRAGMA user_version").Scan(&current)
		if err == nil && current != version {
			err = errors.New("数据库版本在迁移期间发生变化，请重试")
		}
		if err == nil {
			err = migrations[version](tx)
		}
		if err == nil {
			_, err = tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", version+1))
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("数据库升级到版本 %d 失败：%w", version+1, err)
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		version++
	}
	return nil
}
