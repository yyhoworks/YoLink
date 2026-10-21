package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func backupDeepName(depth int) string {
	return "strm-backups/" + strings.Repeat("a", 32) + "/" + strings.Repeat("d/", depth) + "deep.strm"
}

func TestBackupDeepHTTPExportCleansPlaintextAndPreservesOriginal(t *testing.T) {
	f := setup(t)
	_, h, cookie := testToolsServer(t, f, f.c)
	name := backupDeepName(70)
	if !backupNameAllowed(name) {
		t.Fatal("existing legal deep path rejected")
	}
	full := filepath.Join(f.data, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("private-original-link"), 0600); err != nil {
		t.Fatal(err)
	}
	w := generationPOST(h, cookie, "/api/backups/export", object{"admin_password": "fixture-admin-password", "backup_password": backupTestPassword})
	if w.Code != 200 {
		t.Fatal("deep export failed", w.Code, w.Body.String())
	}
	stage, manifest, err := stageBackupArchive(context.Background(), bytes.NewReader(w.Body.Bytes()), t.TempDir(), backupTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = validateBackupSnapshot(context.Background(), stage, manifest); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(stage, filepath.FromSlash(name))); err != nil || string(raw) != "private-original-link" {
		t.Fatal("deep archive lost content", err)
	}
	for i := 0; i < 2; i++ {
		m, err := openRestoreManager(f.data)
		if err != nil {
			t.Fatal(err)
		}
		_, err = m.recoverStartup(context.Background())
		m.Close()
		if err != nil {
			t.Fatal("deep export poisoned next startup", err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(f.data, backupWorkName))
	if err != nil || len(entries) != 0 {
		t.Fatal("deep export left owned plaintext", len(entries), err)
	}
	if raw, err := os.ReadFile(full); err != nil || string(raw) != "private-original-link" {
		t.Fatal("export changed original", err)
	}
}

func TestBackupParentBudgetRejectedBeforeCreatingStaging(t *testing.T) {
	m := backupManifest{Format: 1, Version: appVersion, Schema: currentSchemaVersion, Created: time.Now().Unix()}
	hash := strings.Repeat("a", 64)
	for _, name := range []string{"strmlink.db", "credentials.key", "playback.key", "events.key", "admin-credential.json"} {
		size := int64(32)
		if name == "admin-credential.json" {
			size = 1
		}
		m.Files = append(m.Files, backupFile{Name: name, Size: size, SHA256: hash})
	}
	for i := 0; i < 1500; i++ {
		name := fmt.Sprintf("strm-backups/%s/branch-%04d/%sx.strm", strings.Repeat("a", 32), i, strings.Repeat("d/", 70))
		if !backupNameAllowed(name) {
			t.Fatal("fixture path must otherwise be valid")
		}
		m.Files = append(m.Files, backupFile{Name: name, Size: 1, SHA256: hash})
	}
	err := validateBackupManifest(m)
	if err == nil || !strings.Contains(err.Error(), "父目录") {
		t.Fatal("distinct parent budget not enforced", err)
	}
	data := backupTestArchive(t, m, nil, nil, "")
	parent := t.TempDir()
	if name, _, err := stageBackupArchive(context.Background(), bytes.NewReader(data), parent, backupTestPassword); err == nil || name != "" {
		t.Fatal("over-budget archive created staging", err)
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
		t.Fatal("admission created partial plaintext", err)
	}
}

func TestBackupInterruptedDeepStagingLeavesNoPlaintext(t *testing.T) {
	// Authenticated malformed archive: a deep file is materialized first, then
	// the next header is missing. Partial staging must clean, not poison startup.
	name := backupDeepName(80)
	body := []byte("partial-deep-plaintext")
	sum := sha256.Sum256(body)
	m := backupManifest{Format: 1, Version: appVersion, Schema: currentSchemaVersion, Created: time.Now().Unix(), Files: []backupFile{{Name: name, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}}}
	for _, required := range []string{"strmlink.db", "credentials.key", "playback.key", "events.key", "admin-credential.json"} {
		size := int64(32)
		if required == "admin-credential.json" {
			size = 1
		}
		m.Files = append(m.Files, backupFile{Name: required, Size: size, SHA256: strings.Repeat("a", 64)})
	}
	data := backupTestArchive(t, m, []*tar.Header{{Name: name, Mode: 0600, Size: int64(len(body)), Typeflag: tar.TypeReg}}, [][]byte{body}, "")
	parent := t.TempDir()
	if stage, _, err := stageBackupArchive(context.Background(), bytes.NewReader(data), parent, backupTestPassword); err == nil || stage != "" {
		t.Fatal("interrupted stage accepted or left plaintext", err)
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
		t.Fatal("deep partial staging remains", err)
	}
}
