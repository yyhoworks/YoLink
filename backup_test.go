package main

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

const backupTestPassword = "synthetic-only-backup-passphrase"

func backupFixture(t *testing.T) (*fixture, *Server, string) {
	t.Helper()
	f := setup(t)
	diagnosticMedia(t, f)
	s, _, _ := testToolsServer(t, f, f.c)
	id := strings.Repeat("a", 32)
	if err := s.savePlan(MaintenancePlan{ID: id, Mode: "links", Created: time.Now().Unix(), Items: []PlanItem{}}); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(f.data, "strm-backups", id, "media", "电影.strm")
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("https://private.example.test/original?signature=keep-private"), 0600); err != nil {
		t.Fatal(err)
	}
	dir, err := s.backupSnapshot(context.Background(), t.TempDir(), "fixture-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	return f, s, dir
}

func TestBackupEncryptedSnapshotRoundTripAndAuthentication(t *testing.T) {
	f, _, snapshot := backupFixture(t)
	var archive bytes.Buffer
	manifest, err := writeBackupArchive(context.Background(), &archive, snapshot, backupTestPassword, currentSchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-secret", "private.example", "credentials.key", "fixture-admin-password", "电影"} {
		if bytes.Contains(archive.Bytes(), []byte(secret)) {
			t.Fatal("plaintext leaked to encrypted archive")
		}
	}
	stage, got, err := stageBackupArchive(context.Background(), bytes.NewReader(archive.Bytes()), t.TempDir(), backupTestPassword)
	if err != nil || got.Schema != manifest.Schema || len(got.Files) != len(manifest.Files) {
		t.Fatal("roundtrip failed", err)
	}
	validated, err := validateBackupSnapshot(context.Background(), stage, got)
	if err != nil || validated.Accounts != 1 || validated.Media != 3 {
		t.Fatal("isolated validation failed", validated, err)
	}
	// The snapshot is independent from subsequent live writes.
	if err := f.s.setSetting("after_snapshot", "not-in-backup"); err != nil {
		t.Fatal(err)
	}
	db, err := backupReadOnlyDB(filepath.Join(stage, "strmlink.db"))
	if err != nil {
		t.Fatal(err)
	}
	var n int
	err = db.QueryRow("SELECT COUNT(*) FROM settings WHERE id='after_snapshot'").Scan(&n)
	db.Close()
	if err != nil || n != 0 {
		t.Fatal("live write changed snapshot", err)
	}
	credential, err := loadAdminCredential(stage)
	if err != nil || credential == nil {
		t.Fatal("environment-password verifier missing", err)
	}
	probe := &Server{adminCredential: credential}
	if !probe.passwordMatches("fixture-admin-password") || probe.passwordMatches("wrong") {
		t.Fatal("restored login verifier mismatch")
	}
	if _, err := os.Stat(filepath.Join(stage, "initial-admin-password.txt")); !os.IsNotExist(err) {
		t.Fatal("snapshot unnecessarily saved plaintext administrator password")
	}
	for _, test := range []struct {
		name, password string
		data           []byte
	}{
		{"wrong-password", "wrong-but-long-enough", archive.Bytes()},
		{"truncated", backupTestPassword, archive.Bytes()[:archive.Len()-1]},
		{"tampered", backupTestPassword, append(append([]byte(nil), archive.Bytes()[:archive.Len()-1]...), archive.Bytes()[archive.Len()-1]^1)},
		{"appended", backupTestPassword, append(append([]byte(nil), archive.Bytes()...), byte(1))},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			name, _, err := stageBackupArchive(context.Background(), bytes.NewReader(test.data), parent, test.password)
			if err == nil || name != "" {
				t.Fatal("invalid encrypted archive accepted")
			}
			files, _ := os.ReadDir(parent)
			if len(files) != 0 {
				t.Fatal("failed plaintext staging retained")
			}
		})
	}
}

func backupTestArchive(t *testing.T, m backupManifest, headers []*tar.Header, contents [][]byte, trailing string) []byte {
	t.Helper()
	var out bytes.Buffer
	r, _ := age.NewScryptRecipient(backupTestPassword)
	r.SetWorkFactor(1) // malformed-input fixtures only; production always uses18
	w, err := age.Encrypt(&out, r)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(w)
	raw, _ := json.Marshal(m)
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err = tw.Write(raw); err != nil {
		t.Fatal(err)
	}
	for i, h := range headers {
		if err = tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write(contents[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(w, trailing); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestBackupArchiveRejectsUnsafePathsManifestsAndEntries(t *testing.T) {
	_, _, snapshot := backupFixture(t)
	root, _ := os.OpenRoot(snapshot)
	defer root.Close()
	m := backupManifest{Format: 1, Version: appVersion, Schema: currentSchemaVersion, Created: time.Now().Unix()}
	for _, name := range []string{"strmlink.db", "credentials.key", "playback.key", "events.key", "admin-credential.json"} {
		f, err := backupFileDigest(context.Background(), root, name)
		if err != nil {
			t.Fatal(err)
		}
		m.Files = append(m.Files, f)
	}
	for _, name := range []string{"../outside", "/absolute", `C:\outside`, "plans/../credentials.key", "strm-backups/" + strings.Repeat("a", 32) + "/CON.strm", "credentials.key:stream", "credentials.key.", "plans/invalid.json", "compose.yaml"} {
		if backupNameAllowed(name) {
			t.Fatal("unsafe path allowed", name)
		}
	}
	for _, mutate := range []func(*backupManifest){
		func(v *backupManifest) { v.Format++ },
		func(v *backupManifest) { v.Schema = currentSchemaVersion + 1 },
		func(v *backupManifest) { v.Files = append(v.Files, v.Files[0]) },
		func(v *backupManifest) { v.Files[0].Name = "../outside" },
		func(v *backupManifest) { v.Files[0].Size = backupMaxFileBytes + 1 },
		func(v *backupManifest) { v.Files[1].Size = 31 },
		func(v *backupManifest) { v.Files = v.Files[:3] },
	} {
		copy := m
		copy.Files = append([]backupFile(nil), m.Files...)
		mutate(&copy)
		if validateBackupManifest(copy) == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
	for _, typ := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeDir} {
		data := backupTestArchive(t, m, []*tar.Header{{Name: m.Files[0].Name, Typeflag: typ, Linkname: "outside"}}, [][]byte{nil}, "")
		if _, _, err := stageBackupArchive(context.Background(), bytes.NewReader(data), t.TempDir(), backupTestPassword); err == nil {
			t.Fatal("archive link or directory accepted")
		}
	}
	headers, bodies := []*tar.Header{}, [][]byte{}
	for _, file := range m.Files {
		body, _ := os.ReadFile(filepath.Join(snapshot, file.Name))
		headers = append(headers, &tar.Header{Name: file.Name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(body))})
		bodies = append(bodies, body)
	}
	for _, trailing := range []string{"", "extra-plaintext"} {
		data := backupTestArchive(t, m, headers, bodies, trailing)
		_, _, err := stageBackupArchive(context.Background(), bytes.NewReader(data), t.TempDir(), backupTestPassword)
		if trailing == "" && err != nil || trailing != "" && err == nil {
			t.Fatal("archive tail handling incorrect", err)
		}
	}
	bodies[0][0] ^= 1
	data := backupTestArchive(t, m, headers, bodies, "")
	if _, _, err := stageBackupArchive(context.Background(), bytes.NewReader(data), t.TempDir(), backupTestPassword); err == nil {
		t.Fatal("digest mismatch accepted")
	}
}

func TestBackupSnapshotAuthAndRestoreDoNotReplayHistoricalWork(t *testing.T) {
	f, server, snapshot := backupFixture(t)
	if _, err := server.backupSnapshot(context.Background(), t.TempDir(), "wrong-password"); err == nil {
		t.Fatal("snapshot without reauthentication")
	}
	db, err := backupReadOnlyDB(filepath.Join(snapshot, "strmlink.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DELETE FROM accounts"); err == nil {
		t.Fatal("validation database is writable")
	}
	db.Close()
	// Use only the isolated copy; never mutate f's live work or output.
	copyStore, err := openStore(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	r := f.r
	r.Enabled = true
	if err = copyStore.saveRule(r); err != nil {
		t.Fatal(err)
	}
	if err = copyStore.saveJob(Job{ID: "historical", Rule: r.ID, Status: "running", Resumable: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = copyStore.db.Exec("INSERT INTO deliveries(id,job,kind,status,next_at,payload) VALUES('old','historical','webhook','pending',1,'{}')"); err != nil {
		t.Fatal(err)
	}
	copyStore.db.Close()
	if err = pauseRestoredWork(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	reopened, err := openStore(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.db.Close()
	j, _ := reopened.job("historical")
	if j.Status != "interrupted" || j.Resumable {
		t.Fatal("historical task was replayed", j.Status)
	}
	gotRule, _ := reopened.rule(r.ID)
	if gotRule.Enabled || reopened.setting("event_legacy_enabled", "1") != "0" {
		t.Fatal("automatic sources left enabled")
	}
	var status string
	if err = reopened.db.QueryRow("SELECT status FROM deliveries WHERE id='old'").Scan(&status); err != nil || status != "cancelled" {
		t.Fatal("historical delivery not cancelled", err)
	}
	if reopened.signature("same-media") != f.s.signature("same-media") {
		t.Fatal("stable playback key changed")
	}
	account, err := reopened.account(f.a.ID)
	if err != nil || account.Secrets["authorization"] != "fixture-secret" {
		t.Fatal("credentials not preserved", err)
	}
}

func snapshotManifestForTest(t *testing.T, dir string) backupManifest {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m := backupManifest{Format: 1, Version: appVersion, Schema: currentSchemaVersion, Created: time.Now().Unix()}
	for _, name := range []string{"strmlink.db", "credentials.key", "playback.key", "events.key", "admin-credential.json"} {
		f, err := backupFileDigest(context.Background(), r, name)
		if err != nil {
			t.Fatal(err)
		}
		m.Files = append(m.Files, f)
	}
	return m
}

func TestBackupRejectsReplayBypassesAndMaliciousSchema(t *testing.T) {
	for name, attack := range map[string]string{
		"duplicate-rule-enabled": `UPDATE rules SET payload='{"id":"rule","account":"account","enabled":true,"enabled":true}'`,
		"folded-rule-enabled":    `UPDATE rules SET payload='{"id":"rule","account":"account","enabled":true,"Enabled":true}'`,
		"unicode-job-status":     `INSERT INTO jobs VALUES('attack',1,'queued','{"id":"attack","status":"queued","ſtatus":"queued","resumable":true}')`,
		"status-mismatch":        `INSERT INTO jobs VALUES('attack',1,'completed','{"id":"attack","status":"queued","resumable":true}')`,
		"account-id-mismatch":    `UPDATE accounts SET payload='{"id":"some-other-account"}'`,
		"blob-rule-id":           `DELETE FROM rules; INSERT INTO rules(id,payload) VALUES(x'72756c65','{"id":"rule","enabled":true}')`,
		"blob-job-id":            `INSERT INTO jobs(id,created,status,payload) VALUES(x'61747461636b',1,'queued','{"id":"attack","status":"queued"}')`,
		"field-type":             `UPDATE rules SET payload='{"id":"rule","enabled":"true"}'`,
		"schema-trigger":         `CREATE TRIGGER undo_pause AFTER INSERT ON settings BEGIN UPDATE rules SET payload=json_set(payload,'$.enabled',json('true')); END`,
		"schema-wildcard-name":   `CREATE TRIGGER sqliteXundo_pause AFTER INSERT ON settings BEGIN UPDATE rules SET payload=json_set(payload,'$.enabled',json('true')); DELETE FROM settings WHERE id='restore_review_required'; END`,
		"schema-view":            `CREATE VIEW secret_view AS SELECT * FROM accounts`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, dir := backupFixture(t)
			db, err := sql.Open("sqlite", filepath.Join(dir, "strmlink.db"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(attack)
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			m := snapshotManifestForTest(t, dir)
			if _, err = validateBackupSnapshot(context.Background(), dir, m); err == nil {
				t.Fatal("malicious restore accepted")
			}
			if err = pauseRestoredWork(context.Background(), dir); err == nil {
				t.Fatal("quarantine accepted malicious data")
			}
		})
	}
}

func TestBackupBoundsEveryConsumedCredentialAndSetting(t *testing.T) {
	for _, name := range []string{"admin", "initial", "secret", "setting", "multibyte-payload", "status", "output-path"} {
		t.Run(name, func(t *testing.T) {
			_, _, dir := backupFixture(t)
			m := snapshotManifestForTest(t, dir)
			db, err := sql.Open("sqlite", filepath.Join(dir, "strmlink.db"))
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "admin":
				err = os.WriteFile(filepath.Join(dir, "admin-credential.json"), bytes.Repeat([]byte("x"), 4097), 0600)
			case "initial":
				err = os.Remove(filepath.Join(dir, "admin-credential.json"))
				if err == nil {
					err = os.WriteFile(filepath.Join(dir, "initial-admin-password.txt"), bytes.Repeat([]byte("x"), 4097), 0600)
				}
			case "secret":
				_, err = db.Exec("UPDATE accounts SET secret=zeroblob(1048577)")
			case "setting":
				_, err = db.Exec("INSERT INTO settings(id,value) VALUES('integration_config',?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", strings.Repeat("a", 2097153))
			case "multibyte-payload":
				_, err = db.Exec("UPDATE rules SET payload=?", `{"id":"rule","name":"`+strings.Repeat("中", 350000)+`"}`)
			case "status":
				_, err = db.Exec("UPDATE jobs SET status=?", strings.Repeat("a", 1048577))
			case "output-path":
				_, err = db.Exec("UPDATE outputs SET path=? WHERE path=(SELECT path FROM outputs LIMIT 1)", strings.Repeat("a", 32769)+".strm")
			}
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = validateBackupSnapshot(context.Background(), dir, m); err == nil {
				t.Fatal("oversized configuration accepted")
			}
			if name == "status" && err.Error() != "备份任务状态类型或大小无效" {
				t.Fatal("status was consumed before its preflight bound", err)
			}
		})
	}
}

func TestBackupSchemaVersionsAndCancellation(t *testing.T) {
	for version := 1; version <= currentSchemaVersion; version++ {
		db := schemaTestDB(t)
		migrations := []func(*sql.Tx) error{migrateLegacySchema, migrateCompactCheckpoints, migrateAccountHealth, migrateDiagnostics, migrateScanHistory}
		if err := applySchemaMigrations(db, migrations[:version]); err != nil {
			t.Fatal(err)
		}
		if err := validateBackupSchema(context.Background(), db, version); err != nil {
			t.Fatal(version, err)
		}
	}
	_, server, dir := backupFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := server.backupSnapshot(ctx, t.TempDir(), "fixture-admin-password"); err == nil {
		t.Fatal("cancelled snapshot succeeded")
	}
	if _, err := writeBackupArchive(ctx, io.Discard, dir, backupTestPassword, currentSchemaVersion); err == nil {
		t.Fatal("cancelled archive succeeded")
	}
	for _, raw := range []string{`{"x":{"a":1,"a":2}}`, `{"Status":1,"ſtatus":2}`, `{"a":1} {"b":2}`} {
		if validateBackupJSON([]byte(raw)) == nil {
			t.Fatal("ambiguous JSON accepted")
		}
	}
}
