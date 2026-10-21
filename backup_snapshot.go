package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Snapshot locks only auth/filesystem maintenance. SQLite VACUUM INTO supplies a
// consistent database view while normal playback and transactional writes run.
// Keys are immutable for the lifetime of the running Store.
func (s *Server) backupSnapshot(ctx context.Context, parent, adminPassword string) (dir string, err error) {
	s.authMu.RLock()
	defer s.authMu.RUnlock()
	if !s.passwordMatches(adminPassword) {
		return "", errors.New("当前管理员密码不正确")
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if err = ctx.Err(); err != nil {
		return "", errors.New("备份已取消")
	}
	var pages, pageSize int64
	if err = s.s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return "", errors.New("数据库大小无法核对")
	}
	if err = s.s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil || pageSize <= 0 || pages < 0 || pages > backupMaxFileBytes/pageSize {
		return "", errors.New("数据库超过当前备份大小限制")
	}
	dir, err = os.MkdirTemp(parent, ".backup-snapshot-")
	if err != nil {
		return "", errors.New("无法创建私有备份快照目录")
	}
	defer func() {
		if err != nil {
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil { // exact newly created private directory only
				err = errors.Join(err, errors.New("快照清理失败，需管理员处理遗留备份资料"))
			} else {
				dir = ""
			}
		}
	}()
	if _, err = s.s.db.ExecContext(ctx, "VACUUM INTO ?", filepath.Join(dir, "strmlink.db")); err != nil {
		return dir, errors.New("一致性数据库快照失败，请检查磁盘空间")
	}
	if err = os.Chmod(filepath.Join(dir, "strmlink.db"), 0600); err != nil {
		return dir, errors.New("数据库快照权限设置失败")
	}
	source, err := os.OpenRoot(s.s.dir)
	if err != nil {
		return dir, errors.New("应用数据目录无法打开")
	}
	defer source.Close()
	destination, err := os.OpenRoot(dir)
	if err != nil {
		return dir, errors.New("快照目录无法打开")
	}
	defer destination.Close()
	dbInfo, err := destination.Stat("strmlink.db")
	if err != nil || dbInfo.Size() > backupMaxFileBytes {
		return dir, errors.New("数据库快照超过大小限制")
	}
	total, files := dbInfo.Size(), 2 // database and materialized admin verifier
	parents := backupParents{}
	copyFile := func(name string) error {
		if ctx.Err() != nil {
			return errors.New("备份已取消")
		}
		if !backupNameAllowed(name) {
			return errors.New("恢复副本包含不支持的文件名")
		}
		info, er := source.Lstat(name)
		if er != nil || !info.Mode().IsRegular() || info.Size() > backupMaxFileBytes {
			return errors.New("备份来源不是可读取的普通文件")
		}
		total += info.Size()
		files++
		if total > backupMaxTotalBytes-4096 || files > backupMaxFiles {
			return errors.New("备份快照总量或文件数量超过限制")
		}
		if er = parents.admit(name); er != nil {
			return er
		}
		in, er := source.Open(name)
		if er != nil {
			return errors.New("备份来源读取失败")
		}
		defer in.Close()
		if er = destination.MkdirAll(filepath.Dir(name), 0700); er != nil {
			return errors.New("快照子目录创建失败")
		}
		out, er := destination.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if er != nil {
			return errors.New("快照文件创建失败")
		}
		n, er := io.Copy(out, io.LimitReader(backupContextReader{ctx, in}, info.Size()+1))
		synced, closed := out.Sync(), out.Close()
		if er != nil || synced != nil || closed != nil || n != info.Size() {
			return errors.New("快照复制中断或来源变化")
		}
		return nil
	}
	for _, key := range []string{"credentials.key", "playback.key", "events.key"} {
		if err = copyFile(key); err != nil {
			return dir, err
		}
	}
	// An environment-supplied initial password may have no file. Materialize its
	// salted verifier in the snapshot, never the plaintext or an environment value.
	credential := s.adminCredential
	if credential == nil {
		credential = &adminCredential{Version: 1, Iterations: 600000, Salt: make([]byte, 16)}
		if _, err = rand.Read(credential.Salt); err == nil {
			credential.Hash, err = pbkdf2.Key(sha256.New, adminPassword, credential.Salt, credential.Iterations, 32)
		}
		if err != nil {
			return dir, errors.New("登录配置备份失败")
		}
	}
	if err = saveAdminCredential(dir, credential); err != nil {
		return dir, errors.New("登录配置快照保存失败")
	}
	for _, tree := range []string{"plans", "strm-backups"} {
		info, er := source.Lstat(tree)
		if os.IsNotExist(er) {
			continue
		}
		if er != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return dir, errors.New("恢复资料目录不可安全读取")
		}
		err = filepath.WalkDir(filepath.Join(s.s.dir, tree), func(name string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
				return errors.New("恢复资料包含链接或不可读文件")
			}
			if entry.IsDir() {
				return nil
			}
			rel, er := filepath.Rel(s.s.dir, name)
			if er != nil {
				return errors.New("恢复资料路径无效")
			}
			return copyFile(filepath.ToSlash(rel))
		})
		if err != nil {
			return dir, err
		}
	}
	return dir, nil
}

type backupValidation struct {
	Schema   int `json:"schema"`
	Accounts int `json:"accounts"`
	Rules    int `json:"rules"`
	Media    int `json:"media"`
	Outputs  int `json:"outputs"`
}

// SQLite JSON functions and Go disagree on duplicate keys. Reject duplicates at
// every object level before either implementation interprets restored records.
func validateBackupJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON 嵌套过深")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				// Match Go's field folding, including Unicode equivalents.
				name = strings.Map(func(r rune) rune {
					minimum := r
					for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
						if next < minimum {
							minimum = next
						}
					}
					return minimum
				}, name)
				if err != nil || !ok || seen[name] {
					return errors.New("JSON 键重复或无效")
				}
				seen[name] = true
				if err = value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err = value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("JSON 分隔符无效")
		}
		_, err = d.Token()
		return err
	}
	if err := value(0); err != nil {
		return errors.New("备份 JSON 无效、过深或包含重复键")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("备份 JSON 包含多余内容")
	}
	return nil
}

func backupReadOnlyDB(name string) (*sql.DB, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	file := filepath.ToSlash(abs)
	if !strings.HasPrefix(file, "/") {
		file = "/" + file
	}
	u := url.URL{Scheme: "file", Path: file, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", u.String())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

// Only schemas emitted by this application version's migration chain are
// accepted. Integrity_check alone says nothing about malicious triggers/views.
func validateBackupSchema(ctx context.Context, db *sql.DB, version int) error {
	if version < 1 || version > currentSchemaVersion {
		return errors.New("备份数据库版本不受支持")
	}
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return errors.New("无法建立恢复结构校验")
	}
	defer reference.Close()
	reference.SetMaxOpenConns(1)
	migrations := []func(*sql.Tx) error{migrateLegacySchema, migrateCompactCheckpoints, migrateAccountHealth, migrateDiagnostics, migrateScanHistory}
	if err = applySchemaMigrations(reference, migrations[:version]); err != nil {
		return errors.New("无法建立恢复结构基线")
	}
	read := func(conn *sql.DB) (map[string]string, error) {
		var invalid int
		if e := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE length(CAST(sql AS BLOB))>65536 OR length(CAST(name AS BLOB))>4096 OR length(CAST(tbl_name AS BLOB))>4096").Scan(&invalid); e != nil || invalid != 0 {
			return nil, errors.New("schema too large")
		}
		// Include every object, even autoindexes/internal tables. A name filter
		// can hide legal attacker names (SQL LIKE '_' matches any character).
		rows, e := conn.QueryContext(ctx, "SELECT type,name,tbl_name,COALESCE(sql,'') FROM sqlite_master")
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var kind, name, table, source string
			if e = rows.Scan(&kind, &name, &table, &source); e != nil {
				return nil, e
			}
			if len(out) >= 200 || len(source) > 65536 {
				return nil, errors.New("schema too large")
			}
			out[kind+":"+name] = table + ":" + strings.Join(strings.Fields(source), " ")
		}
		return out, rows.Err()
	}
	want, err := read(reference)
	if err != nil {
		return errors.New("恢复结构基线无法读取")
	}
	got, err := read(db)
	if err != nil || len(got) != len(want) {
		return errors.New("备份数据库包含不受支持的表、索引、视图或触发器")
	}
	for key, definition := range want {
		if got[key] != definition {
			return errors.New("备份数据库结构与受支持版本不一致")
		}
	}
	return nil
}

// This path never calls openStore/newEngine: inspection must not create keys,
// migrate production data, queue jobs, invoke providers or deliver notifications.
func validateBackupRows(ctx context.Context, db *sql.DB) error {
	var err error
	var invalid int
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= 5 {
		if err = validateBackupScanRows(ctx, db); err != nil {
			return err
		}
	}
	// Check status BEFORE selecting it into Go. SQLite column affinity is not a
	// type constraint: BLOB IDs break the textual keyset used by quarantine.
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM jobs WHERE typeof(status)<>'text' OR length(CAST(status AS BLOB))>64").Scan(&invalid); err != nil || invalid != 0 {
		return errors.New("备份任务状态类型或大小无效")
	}
	// Reject malformed/oversized serialized rows before Go allocates/unmarshals
	// their contents; these limits exceed the normal guarded API payload limits.
	for _, table := range []string{"accounts", "rules", "media", "jobs"} {
		var invalid int
		var count, total int64
		if err = db.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(length(CAST(payload AS BLOB))),0) FROM "+table).Scan(&count, &total); err != nil || count > 1000000 || total > 512<<20 {
			return errors.New("备份记录总量超过恢复检查限制")
		}
		if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE typeof(id)<>'text' OR typeof(payload)<>'text' OR NOT json_valid(payload) OR length(CAST(payload AS BLOB))>1048576 OR length(CAST(id AS BLOB))>4096").Scan(&invalid); err != nil || invalid != 0 {
			return errors.New("备份数据库包含无效或过大的配置记录")
		}
		statusColumn := "''"
		if table == "jobs" {
			statusColumn = "status"
		}
		rows, e := db.QueryContext(ctx, "SELECT id,"+statusColumn+",payload FROM "+table)
		if e != nil {
			return errors.New("备份记录无法读取")
		}
		for rows.Next() {
			var id, status, raw string
			var identity struct{ ID, Status string }
			if e = rows.Scan(&id, &status, &raw); e != nil || validateBackupJSON([]byte(raw)) != nil || json.Unmarshal([]byte(raw), &identity) != nil || id == "" || identity.ID != id || table == "jobs" && identity.Status != status {
				rows.Close()
				return errors.New("备份记录身份或状态不一致，或 JSON 键重复")
			}
			var typed any
			switch table {
			case "accounts":
				typed = new(Account)
			case "rules":
				typed = new(Rule)
			case "media":
				typed = new(Media)
			case "jobs":
				typed = new(Job)
			}
			if json.Unmarshal([]byte(raw), typed) != nil {
				rows.Close()
				return errors.New("备份记录字段类型无效")
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return errors.New("备份记录读取中断")
		}
	}
	for _, pair := range [][2]string{{"accounts", "secret"}, {"settings", "value"}} {
		var count, total int64
		if err = db.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(length(CAST("+pair[1]+" AS BLOB))),0) FROM "+pair[0]).Scan(&count, &total); err != nil || count > 1000000 || total > 64<<20 {
			return errors.New("备份凭据或设置总量超过恢复检查限制")
		}
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM outputs WHERE typeof(path)<>'text' OR typeof(media)<>'text' OR typeof(hash)<>'text' OR length(CAST(path AS BLOB))>32768 OR length(CAST(media AS BLOB))>4096 OR length(CAST(hash AS BLOB))>128").Scan(&invalid); err != nil || invalid != 0 {
		return errors.New("备份输出索引类型或大小无效")
	}
	var outputsCount, outputsBytes int64
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(length(CAST(path AS BLOB))+length(CAST(media AS BLOB))+length(CAST(hash AS BLOB))),0) FROM outputs").Scan(&outputsCount, &outputsBytes); err != nil || outputsCount > 1000000 || outputsBytes > 512<<20 {
		return errors.New("备份输出索引总量超过恢复检查限制")
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts WHERE length(CAST(secret AS BLOB))>1048576").Scan(&invalid); err != nil || invalid != 0 {
		return errors.New("备份加密凭据大小无效")
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM settings WHERE length(CAST(value AS BLOB))>2097152").Scan(&invalid); err != nil || invalid != 0 {
		return errors.New("备份设置大小无效")
	}
	return nil
}

func validateBackupSnapshot(ctx context.Context, dir string, manifest backupManifest) (result backupValidation, err error) {
	if err = validateBackupManifest(manifest); err != nil {
		return result, err
	}
	keys := make(map[string][]byte)
	for _, name := range []string{"credentials.key", "playback.key", "events.key"} {
		key, e := readBackupSmallFile(filepath.Join(dir, name), 32)
		if e != nil || len(key) != 32 {
			return result, errors.New("隔离快照密钥无效")
		}
		keys[name] = key
	}
	rawCredential, credentialErr := readBackupSmallFile(filepath.Join(dir, "admin-credential.json"), 4096)
	if os.IsNotExist(credentialErr) {
		initial, e := readBackupSmallFile(filepath.Join(dir, "initial-admin-password.txt"), 4096)
		if e != nil || strings.TrimSpace(string(initial)) == "" {
			return result, errors.New("隔离快照缺少有效登录配置")
		}
	} else {
		var c adminCredential
		if credentialErr != nil || validateBackupJSON(rawCredential) != nil || json.Unmarshal(rawCredential, &c) != nil || c.Version != 1 || c.Iterations != 600000 || len(c.Salt) != 16 || len(c.Hash) != 32 {
			return result, errors.New("隔离快照登录校验值无效")
		}
	}
	db, err := backupReadOnlyDB(filepath.Join(dir, "strmlink.db"))
	if err != nil {
		return result, errors.New("隔离数据库无法打开")
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, "PRAGMA trusted_schema=OFF"); err != nil {
		return result, errors.New("隔离数据库安全设置失败")
	}
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&result.Schema); err != nil || result.Schema != manifest.Schema {
		return result, errors.New("数据库版本与备份清单不一致")
	}
	if err = validateBackupSchema(ctx, db, result.Schema); err != nil {
		return result, err
	}
	var integrity string
	if err = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return result, errors.New("数据库完整性检查失败")
	}
	if err = validateBackupRows(ctx, db); err != nil {
		return result, err
	}
	block, _ := aes.NewCipher(keys["credentials.key"])
	box, _ := cipher.NewGCM(block)
	s := &Store{dir: dir, db: db, box: box, playKey: keys["playback.key"]}
	accounts, err := db.QueryContext(ctx, "SELECT id,secret FROM accounts")
	if err != nil {
		return result, errors.New("备份账号索引不可读取")
	}
	for accounts.Next() {
		var id string
		var secret []byte
		var credentials map[string]string
		if err = accounts.Scan(&id, &secret); err != nil || len(secret) < box.NonceSize() {
			accounts.Close()
			return result, errors.New("备份账号凭据不可读取")
		}
		plain, e := box.Open(nil, secret[:box.NonceSize()], secret[box.NonceSize():], []byte(id))
		if e != nil || validateBackupJSON(plain) != nil || json.Unmarshal(plain, &credentials) != nil {
			accounts.Close()
			return result, errors.New("备份账号凭据与密钥不匹配")
		}
	}
	err = accounts.Err()
	accounts.Close()
	if err != nil {
		return result, errors.New("恢复账号检查中断")
	}
	for key, aad := range map[string]string{"integration_config": "integrations", "notification_channels": "notification_channels"} {
		value := s.setting(key, "")
		if value == "" {
			continue
		}
		sealed, e := base64.StdEncoding.DecodeString(value)
		if e != nil || len(sealed) < box.NonceSize() {
			return result, errors.New("备份加密设置无效")
		}
		plain, e := box.Open(nil, sealed[:box.NonceSize()], sealed[box.NonceSize():], []byte(aad))
		if e != nil || validateBackupJSON(plain) != nil {
			return result, errors.New("备份加密设置无效或键重复")
		}
	}
	if _, err = s.integrationConfig(); err != nil {
		return result, errors.New("备份联动配置无法解密")
	}
	if _, err = s.channels(); err != nil {
		return result, errors.New("备份通知配置无法解密")
	}
	for table, target := range map[string]*int{"accounts": &result.Accounts, "rules": &result.Rules, "media": &result.Media, "outputs": &result.Outputs} {
		if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(target); err != nil {
			return result, errors.New("备份索引统计失败")
		}
	}
	return result, nil
}

func readBackupSmallFile(name string, maximum int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, errors.New("备份配置文件类型或大小无效")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil || int64(len(raw)) > maximum {
		return nil, errors.New("备份配置文件读取失败或过大")
	}
	return raw, nil
}

// Operates ONLY on validated isolated staging before engine initialization.
// Historical work is retained for inspection/manual retry, never auto-replayed.
func pauseRestoredWork(ctx context.Context, dir string) error {
	db, err := sql.Open("sqlite", filepath.Join(dir, "strmlink.db"))
	if err != nil {
		return errors.New("恢复数据库无法打开")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA trusted_schema=OFF"); err != nil {
		return errors.New("恢复数据库安全设置失败")
	}
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return errors.New("恢复数据库版本读取失败")
	}
	if err = validateBackupSchema(ctx, db, version); err != nil {
		return err
	}
	if err = validateBackupRows(ctx, db); err != nil {
		return err
	}
	if err = migrateSchema(db); err != nil {
		return errors.New("隔离恢复迁移失败")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("恢复保护事务无法开始")
	}
	defer tx.Rollback()
	if err = quarantineBackupPayloads(ctx, tx); err != nil {
		return err
	}
	for _, statement := range []string{
		`UPDATE ingest_events SET status='failed',error='从备份恢复，未自动重放',revision=revision+1 WHERE status='pending'`,
		`UPDATE deliveries SET status='cancelled',next_at=0,error='从备份恢复，未自动重放' WHERE status IN ('pending','paused')`,
		`UPDATE event_clients SET enabled=0`,
		`INSERT INTO settings(id,value) VALUES('event_legacy_enabled','0') ON CONFLICT(id) DO UPDATE SET value='0'`,
		`INSERT INTO settings(id,value) VALUES('restore_review_required','1') ON CONFLICT(id) DO UPDATE SET value='1'`,
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return errors.New("恢复保护事务失败，未激活备份")
		}
	}
	if err = tx.Commit(); err != nil {
		return errors.New("恢复保护事务提交失败")
	}
	return nil
}

// Use the same typed readers as startup, then canonicalize. JSON paths are
// case-sensitive whereas encoding/json also accepts folded field names. Even a
// single "Enabled" field could otherwise survive json_set('$.enabled',false).
// Bounded keyset batches avoid materializing all restored jobs at once.
func quarantineBackupPayloads(ctx context.Context, tx *sql.Tx) error {
	// Completed observations remain available for inspection, but no historical
	// pointer or per-item intent can authorize work in this restored generation.
	for _, q := range []string{"DELETE FROM scan_baselines", "UPDATE scan_runs SET status='quarantined'", "UPDATE scan_items SET state='quarantined'", "UPDATE change_plans SET state='quarantined'", "UPDATE change_items SET state='quarantined'"} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return errors.New("恢复扫描与修复隔离失败")
		}
	}
	for _, table := range []string{"rules", "jobs"} {
		last := ""
		for {
			rows, err := tx.QueryContext(ctx, "SELECT id,payload FROM "+table+" WHERE id>? ORDER BY id LIMIT 16", last)
			if err != nil {
				return errors.New("恢复保护记录无法读取")
			}
			type record struct{ id, raw string }
			batch := []record{}
			for rows.Next() {
				var v record
				if err = rows.Scan(&v.id, &v.raw); err != nil {
					break
				}
				batch = append(batch, v)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				return errors.New("恢复保护记录读取中断")
			}
			if len(batch) == 0 {
				break
			}
			for _, v := range batch {
				if table == "rules" {
					var r Rule
					if json.Unmarshal([]byte(v.raw), &r) != nil {
						return errors.New("恢复规则无效")
					}
					r.Enabled = false
					raw, er := json.Marshal(r)
					if er != nil {
						return errors.New("恢复规则编码失败")
					}
					var changed sql.Result
					changed, err = tx.ExecContext(ctx, "UPDATE rules SET payload=? WHERE id=?", string(raw), v.id)
					if err == nil {
						var n int64
						n, err = changed.RowsAffected()
						if n != 1 {
							err = errors.New("恢复规则身份不唯一")
						}
					}
				} else {
					var j Job
					if json.Unmarshal([]byte(v.raw), &j) != nil {
						return errors.New("恢复任务无效")
					}
					j.Resumable = false
					if j.Status == "running" || j.Status == "queued" || j.Status == "interrupted" {
						j.Status = "interrupted"
						j.Message = "从备份恢复，任务未自动重放，请核对后手动重试"
					}
					raw, er := json.Marshal(j)
					if er != nil {
						return errors.New("恢复任务编码失败")
					}
					var changed sql.Result
					changed, err = tx.ExecContext(ctx, "UPDATE jobs SET status=?,payload=? WHERE id=?", j.Status, string(raw), v.id)
					if err == nil {
						var n int64
						n, err = changed.RowsAffected()
						if n != 1 {
							err = errors.New("恢复任务身份不唯一")
						}
					}
				}
				if err != nil {
					return errors.New("恢复保护记录保存失败")
				}
				last = v.id
			}
		}
	}
	return nil
}
