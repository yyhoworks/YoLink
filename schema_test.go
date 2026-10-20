package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func schemaTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSchemaLegacyLayoutsUpgradeAtomicallyAndRepeatably(t *testing.T) {
	for _, layout := range []string{"empty", "base", "legacy-deliveries", "channels"} {
		t.Run(layout, func(t *testing.T) {
			db := schemaTestDB(t)
			if layout != "empty" {
				if _, err := db.Exec(baseSchema); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("INSERT INTO rules VALUES('keep','{\"id\":\"keep\"}'); INSERT INTO settings VALUES('metrics_since','original')"); err != nil {
					t.Fatal(err)
				}
			}
			if layout == "legacy-deliveries" || layout == "channels" {
				if _, err := db.Exec(featureSchema); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("INSERT INTO deliveries(id,job,kind,status,next_at,payload) VALUES('delivery','job','webhook','failed',123,'{}')"); err != nil {
					t.Fatal(err)
				}
				if layout == "channels" {
					tx, err := db.Begin()
					if err != nil {
						t.Fatal(err)
					}
					if err = migrateConnectionSchema(tx); err != nil {
						tx.Rollback()
						t.Fatal(err)
					}
					if err = tx.Commit(); err != nil {
						t.Fatal(err)
					}
				}
			}
			for i := 0; i < 2; i++ {
				if err := migrateSchema(db); err != nil {
					t.Fatal(err)
				}
				var version int
				if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != currentSchemaVersion {
					t.Fatal(version, err)
				}
			}
			if layout != "empty" {
				var value string
				if err := db.QueryRow("SELECT value FROM settings WHERE id='metrics_since'").Scan(&value); err != nil || value != "original" {
					t.Fatal("existing setting changed", value, err)
				}
				if err := db.QueryRow("SELECT payload FROM rules WHERE id='keep'").Scan(&value); err != nil || value != `{"id":"keep"}` {
					t.Fatal("rule lost", value, err)
				}
			}
			if layout == "legacy-deliveries" || layout == "channels" {
				var channel, status string
				if err := db.QueryRow("SELECT channel,status FROM deliveries WHERE id='delivery'").Scan(&channel, &status); err != nil || channel != legacyChannelID || status != "failed" {
					t.Fatal("delivery changed", channel, status, err)
				}
			}
		})
	}
}

func TestSchemaMigrationFailureRollsBackDataAndVersion(t *testing.T) {
	db := schemaTestDB(t)
	failure := errors.New("isolated migration failure")
	err := applySchemaMigrations(db, []func(*sql.Tx) error{func(tx *sql.Tx) error {
		if _, err := tx.Exec("CREATE TABLE should_rollback(value TEXT); INSERT INTO should_rollback VALUES('test')"); err != nil {
			return err
		}
		return failure
	}})
	if !errors.Is(err, failure) {
		t.Fatal("failure not propagated", err)
	}
	var count, version int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='should_rollback'").Scan(&count); err != nil || count != 0 {
		t.Fatal("DDL not rolled back", count, err)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 0 {
		t.Fatal("version advanced after failure", version, err)
	}
	if err := migrateSchema(db); err != nil {
		t.Fatal("retry after rollback failed", err)
	}
}

func TestStoreRejectsFutureSchemaBeforeCreatingKeysOrChangingDatabase(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "strmlink.db")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE future(value TEXT); INSERT INTO future VALUES('preserve'); PRAGMA user_version=9999"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := openStore(dir); err == nil {
		s.db.Close()
		t.Fatal("future schema accepted")
	}
	after, err := os.ReadFile(file)
	if err != nil || string(before) != string(after) {
		t.Fatal("future DB changed", err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatal("future database refusal created files", files, err)
	}
}
