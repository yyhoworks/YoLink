package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Account struct {
	Revision   int64             `json:"revision"`
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Provider   string            `json:"provider"`
	Root       string            `json:"root"`
	Drive      string            `json:"drive"`
	AllowProxy bool              `json:"allow_proxy"`
	Disabled   bool              `json:"disabled"`
	Secrets    map[string]string `json:"secrets,omitempty"`
}
type Rule struct {
	Filter         FileFilter `json:"filter"`
	RefreshLibrary bool       `json:"refresh_library"`
	Notify         bool       `json:"notify"`
	CopyNonMedia   bool       `json:"copy_non_media"`
	CopySubtitles  bool       `json:"copy_subtitles"`
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Account        string     `json:"account"`
	Folder         string     `json:"folder"`
	SourcePath     string     `json:"source_path"`
	Output         string     `json:"output"`
	Interval       int        `json:"interval"`
	Enabled        bool       `json:"enabled"`
	LastRun        int64      `json:"last_run"`
}
type Entry struct {
	PickCode string `json:"pickcode,omitempty"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Dir      bool   `json:"dir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}
type Media struct {
	PickCode string `json:"pickcode,omitempty"`
	Legacy   bool   `json:"legacy,omitempty"`
	ID       string `json:"id"`
	Account  string `json:"account"`
	RemoteID string `json:"remote_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
}
type Job struct {
	Started          int64         `json:"started,omitempty"`
	Resumable        bool          `json:"resumable,omitempty"`
	Resumes          int           `json:"resumes,omitempty"`
	Filtered         int           `json:"filtered"`
	ErrorKind        string        `json:"error_kind,omitempty"`
	PreviewSame      int           `json:"preview_same"`
	PreviewConflicts int           `json:"preview_conflicts"`
	Preview          []PreviewItem `json:"preview,omitempty"`
	Snapshot         *Rule         `json:"snapshot,omitempty"`
	RetryDirs        []ScanDir     `json:"retry_dirs,omitempty"`
	Trigger          string        `json:"trigger,omitempty"`
	NonMediaFiles    int           `json:"non_media_files,omitempty"`
	NonMediaSkipped  int           `json:"non_media_skipped,omitempty"`
	SidecarsSkipped  int           `json:"sidecars_skipped,omitempty"`
	Sidecars         int           `json:"sidecars,omitempty"`
	ID               string        `json:"id"`
	Rule             string        `json:"rule"`
	Folder           string        `json:"folder"`
	SourcePath       string        `json:"source_path"`
	DryRun           bool          `json:"dry_run"`
	Limit            int           `json:"limit"`
	Status           string        `json:"status"`
	Created          int64         `json:"created"`
	Finished         int64         `json:"finished"`
	Seen             int           `json:"seen"`
	CreatedFiles     int           `json:"created_files"`
	Skipped          int           `json:"skipped"`
	Errors           int           `json:"errors"`
	Message          string        `json:"message"`
	Samples          []string      `json:"samples"`
	Failures         []string      `json:"failures"`
	CurrentPath      string        `json:"current_path,omitempty"`
	RetryOf          string        `json:"retry_of,omitempty"`
	RetriedBy        string        `json:"retried_by,omitempty"`
}
type Store struct {
	startupRestore restoreRuntime
	// Immutable for this Store lifetime. A restored database starts without any
	// background workers; clearing the durable gate requires a controlled restart.
	restoreRestricted bool
	channelsMu        sync.Mutex
	activityMu        sync.Mutex
	activityError     string
	dir               string
	db                *sql.DB
	box               cipher.AEAD
	playKey           []byte
	mu                sync.Mutex
}

func randomID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func keyFile(dir, name string, n int) ([]byte, error) {
	p := filepath.Join(dir, name)
	b, e := os.ReadFile(p)
	if e == nil {
		if len(b) != n {
			return nil, fmt.Errorf("invalid key file %s", name)
		}
		return b, nil
	}
	if !os.IsNotExist(e) {
		return nil, e
	}
	b = make([]byte, n)
	if _, e = rand.Read(b); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, e
	}
	_, e = f.Write(b)
	ce := f.Close()
	if e != nil {
		return nil, e
	}
	return b, ce
}
func openStore(dir string) (result *Store, err error) {
	return openStoreForStartup(dir, restoreRuntime{})
}

func openStoreForStartup(dir string, recovery restoreRuntime) (result *Store, err error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", filepath.Join(dir, "strmlink.db"))
	if e != nil {
		return nil, e
	}
	defer func() {
		if result == nil {
			_ = db.Close()
		}
	}()
	db.SetMaxOpenConns(1)
	var version int
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
		return nil, e
	}
	if version < 0 || version > currentSchemaVersion {
		return nil, errors.New("数据库版本高于当前程序或版本无效，请使用匹配版本；未修改数据库")
	}
	key, e := keyFile(dir, "credentials.key", 32)
	if e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	box, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	play, e := keyFile(dir, "playback.key", 32)
	if e != nil {
		return nil, e
	}
	s := &Store{db: db, box: box, playKey: play, dir: dir, startupRestore: recovery}
	if e = migrateSchema(db); e != nil {
		return nil, e
	}
	var restoreReview string
	e = db.QueryRow("SELECT value FROM settings WHERE id='restore_review_required'").Scan(&restoreReview)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return nil, errors.New("恢复保护状态无法读取，拒绝启动")
	}
	if e == nil && restoreReview != "0" && restoreReview != "1" {
		return nil, errors.New("恢复保护状态无效，拒绝启动")
	}
	s.restoreRestricted = restoreReview == "1" || recovery.held()
	if !s.restoreRestricted {
		jobs, e := s.jobs(10000)
		if e != nil {
			return nil, e
		}
		for _, j := range jobs {
			if j.Status == "running" || j.Status == "queued" || (j.Status == "interrupted" && j.Resumable) {
				if j.Resumable {
					j.Status = "queued"
					j.Resumes++
					j.Message = "服务恢复，将从已保存断点继续"
					j.Finished = 0
					if e = s.saveJob(j); e != nil {
						return nil, e
					}
					continue
				}
				j.Status = "interrupted"
				j.Message = "服务已重启；可重新生成，已生成的文件会跳过"
				j.Finished = time.Now().Unix()
				if e = s.saveJob(j); e != nil {
					return nil, e
				}
			}
		}
	}
	// Legacy output backfill is deferred until restored data has been reviewed.
	// Inspection/startup under hold must not materialize historical activities.
	if !s.restoreRestricted {
		if e = s.initActivities(); e != nil {
			db.Close()
			return nil, e
		}
	}
	return s, nil
}
func (s *Store) saveAccount(a Account) error {
	raw, e := json.Marshal(a.Secrets)
	if e != nil {
		return e
	}
	nonce := make([]byte, s.box.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	sealed := s.box.Seal(nonce, nonce, raw, []byte(a.ID))
	a.Secrets = nil
	p, e := json.Marshal(a)
	if e != nil {
		return e
	}
	_, e = s.db.Exec("INSERT INTO accounts VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,secret=excluded.secret", a.ID, string(p), sealed)
	return e
}
func (s *Store) account(id string) (Account, error) {
	var a Account
	var p string
	var sec []byte
	e := s.db.QueryRow("SELECT payload,secret FROM accounts WHERE id=?", id).Scan(&p, &sec)
	if e != nil {
		return a, e
	}
	if e = json.Unmarshal([]byte(p), &a); e != nil {
		return a, e
	}
	n := s.box.NonceSize()
	if len(sec) < n {
		return a, errors.New("凭据数据损坏")
	}
	raw, e := s.box.Open(nil, sec[:n], sec[n:], []byte(id))
	if e != nil {
		return a, errors.New("无法解密账号，请检查 credentials.key")
	}
	e = json.Unmarshal(raw, &a.Secrets)
	return a, e
}
func (s *Store) accounts() ([]Account, error) {
	rows, e := s.db.Query("SELECT payload FROM accounts ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var p string
		var a Account
		if e = rows.Scan(&p); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(p), &a); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) saveRule(r Rule) error {
	p, e := json.Marshal(r)
	if e != nil {
		return e
	}
	_, e = s.db.Exec("INSERT INTO rules VALUES(?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", r.ID, string(p))
	return e
}
func (s *Store) rule(id string) (Rule, error) {
	var r Rule
	var p string
	e := s.db.QueryRow("SELECT payload FROM rules WHERE id=?", id).Scan(&p)
	if e != nil {
		return r, e
	}
	e = json.Unmarshal([]byte(p), &r)
	return r, e
}
func (s *Store) rules() ([]Rule, error) {
	rows, e := s.db.Query("SELECT payload FROM rules ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var p string
		var r Rule
		if e = rows.Scan(&p); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(p), &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) saveJob(j Job) error {
	p, e := json.Marshal(j)
	if e != nil {
		return e
	}
	_, e = s.db.Exec("INSERT INTO jobs VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,payload=excluded.payload", j.ID, j.Created, j.Status, string(p))
	return e
}
func (s *Store) job(id string) (Job, error) {
	var j Job
	var p string
	e := s.db.QueryRow("SELECT payload FROM jobs WHERE id=?", id).Scan(&p)
	if e != nil {
		return j, e
	}
	e = json.Unmarshal([]byte(p), &j)
	return j, e
}
func (s *Store) jobs(limit int) ([]Job, error) {
	rows, e := s.db.Query("SELECT payload FROM jobs ORDER BY created DESC,rowid DESC LIMIT ?", limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var p string
		var j Job
		if e = rows.Scan(&p); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(p), &j); e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) saveMedia(m Media) error {
	p, e := json.Marshal(m)
	if e != nil {
		return e
	}
	_, e = s.db.Exec("INSERT INTO media VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET source_path=excluded.source_path,payload=excluded.payload", m.ID, m.Account, m.RemoteID, m.Path, string(p))
	return e
}
func (s *Store) media(id string) (Media, error) {
	var m Media
	var p string
	e := s.db.QueryRow("SELECT payload FROM media WHERE id=?", id).Scan(&p)
	if e != nil {
		return m, e
	}
	e = json.Unmarshal([]byte(p), &m)
	return m, e
}
func (s *Store) setting(id, def string) string {
	var v string
	if s.db.QueryRow("SELECT value FROM settings WHERE id=?", id).Scan(&v) != nil {
		return def
	}
	return v
}
func (s *Store) setSetting(id, v string) error {
	_, e := s.db.Exec("INSERT INTO settings VALUES(?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", id, v)
	return e
}
func (s *Store) signature(id string) string {
	h := hmac.New(sha256.New, s.playKey)
	h.Write([]byte(id))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Store) mediaID(a Account, remote string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{a.ID, a.Provider, a.Drive, remote}, "\x00")))
	return hex.EncodeToString(h[:16])
}
