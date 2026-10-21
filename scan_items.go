package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type scanItem struct {
	Run        string   `json:"run"`
	Remote     string   `json:"remote"`
	Key        string   `json:"key"`
	Media      Media    `json:"media"`
	Hash       string   `json:"hash"`
	PriorOwner string   `json:"prior_owner"`
	PriorHash  string   `json:"prior_hash"`
	PriorMedia string   `json:"prior_media"`
	Outcome    string   `json:"outcome"`
	Activity   Activity `json:"activity"`
}

type scanLocalChange struct {
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	Source   string `json:"source"`
	Media    string `json:"media"`
	Expected string `json:"expected"`
	Actual   string `json:"actual,omitempty"`
}

func (e *Engine) hitScanFault(point string) error {
	if e.scanFault != nil {
		return e.scanFault(point)
	}
	return nil
}

func (e *Engine) outputMappingIdentity() string {
	type mapping struct {
		Path    string
		Default bool
	}
	v := make([]mapping, 0, len(e.outputMounts))
	for _, m := range e.outputMounts {
		v = append(v, mapping{m.Path, m.Default})
	}
	return restoreRevision(v)
}

// Read at most one STRM and reject symlink components. Missing is distinct from
// inaccessible, modified or a directory; none of those states authorizes repair.
func (e *Engine) outputHash(key string) (string, error) {
	root, rel, _, err := e.outputLocation(key)
	if err != nil {
		return "", err
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := range parts {
		info, er := root.Lstat(filepath.FromSlash(strings.Join(parts[:i+1], "/")))
		if er != nil {
			return "", er
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("输出路径包含符号链接，已保留")
		}
	}
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return "", errors.New("输出不是可核验的普通 STRM")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return "", errors.New("输出读取失败或过大")
	}
	return contentHash(raw), nil
}

func outputOwnerTx(tx *sql.Tx, key string) (string, string, error) {
	var owner, hash string
	err := tx.QueryRow("SELECT media,hash FROM outputs WHERE path=?", key).Scan(&owner, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return owner, hash, err
}

func scanItemOwnerTx(tx *sql.Tx, item scanItem) error {
	owner, hash, err := outputOwnerTx(tx, item.Key)
	if err != nil {
		return err
	}
	if owner != item.PriorOwner || hash != item.PriorHash {
		return errors.New("输出归属或登记内容已变化，未继续生成")
	}
	var raw string
	err = tx.QueryRow("SELECT payload FROM media WHERE id=?", item.Media.ID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if raw != item.PriorMedia {
		return errors.New("媒体索引已变化，未继续生成")
	}
	return nil
}

func (e *Engine) prepareScanItem(ctx context.Context, session *scanSession, m Media, key, hash string) (scanItem, bool, error) {
	tx, err := e.s.db.BeginTx(ctx, nil)
	if err != nil {
		return scanItem{}, false, err
	}
	defer tx.Rollback()
	if err = session.guard(tx); err != nil {
		return scanItem{}, false, err
	}
	var raw, state string
	err = tx.QueryRow("SELECT state,payload FROM scan_items WHERE run=? AND remote=?", session.Run.ID, m.RemoteID).Scan(&state, &raw)
	if err == nil {
		var item scanItem
		if restoreDecode([]byte(raw), &item) != nil || state != "intent" || item.Run != session.Run.ID || item.Remote != m.RemoteID || item.Key != key || item.Hash != hash || restoreRevision(item.Media) != restoreRevision(m) {
			return item, true, errors.New("条目恢复身份不一致")
		}
		if err = scanItemOwnerTx(tx, item); err != nil {
			return item, true, err
		}
		return item, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return scanItem{}, false, err
	}
	item := scanItem{Run: session.Run.ID, Remote: m.RemoteID, Key: key, Media: m, Hash: hash}
	item.PriorOwner, item.PriorHash, err = outputOwnerTx(tx, key)
	if err != nil {
		return item, false, err
	}
	if item.PriorOwner != "" && item.PriorOwner != m.ID {
		return item, false, errors.New("输出名称归属冲突，已保留")
	}
	err = tx.QueryRow("SELECT payload FROM media WHERE id=?", m.ID).Scan(&item.PriorMedia)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return item, false, err
	}
	encoded, _ := json.Marshal(item)
	if len(encoded) > 1<<20 {
		return item, false, errors.New("生成条目恢复资料过大")
	}
	if _, err = tx.Exec("INSERT INTO scan_items VALUES(?,?,'intent',?)", item.Run, item.Remote, string(encoded)); err != nil {
		return item, false, err
	}
	return item, false, tx.Commit()
}

// Output protection applies even when a limited scan, failed-directory retry,
// or legacy checkpoint cannot establish a complete observation baseline.
func (e *Engine) preserveRegisteredScanOutput(ctx context.Context, a Account, r Rule, j *Job, f Entry, source, dest string, m Media, content []byte, cp *ScanCheckpoint) (bool, error) {
	_, _, key, err := e.outputLocation(dest)
	if err != nil {
		return false, err
	}
	hash := contentHash(content)
	var registeredOwner, registeredHash string
	ownerErr := e.s.db.QueryRowContext(ctx, "SELECT media,hash FROM outputs WHERE path=?", key).Scan(&registeredOwner, &registeredHash)
	if ownerErr != nil && !errors.Is(ownerErr, sql.ErrNoRows) {
		return false, ownerErr
	}
	if ownerErr == nil && registeredOwner == m.ID {
		actual, readErr := e.outputHash(key)
		kind := ""
		if os.IsNotExist(readErr) {
			kind = "local_missing"
		} else if readErr != nil || actual != registeredHash {
			kind = "local_modified"
		} else if registeredHash != hash {
			kind = "outdated"
		}
		var intents int
		if cp.scan != nil {
			if err = e.s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM scan_items WHERE run=? AND remote=? AND state='intent'", cp.scan.Run.ID, f.ID).Scan(&intents); err != nil {
				return false, err
			}
		}
		if kind != "" && intents == 0 {
			if cp.scan != nil {
				cp.local = &scanLocalChange{kind, key, source, m.ID, registeredHash, actual}
			}
			j.LocalIssues++
			outcome := "skipped"
			if kind == "local_missing" {
				j.Skipped++
			} else {
				j.Errors++
				j.Message = "已有文件内容或播放入口不同，已保留：" + key
				j.ErrorKind = "conflict"
				outcome = "failed"
			}
			cp.activity = &Activity{Time: time.Now().UnixMilli(), Kind: "generation", Event: "strm", Outcome: outcome, Media: m.ID, Name: f.Name, Path: source, Output: generationOutputName(e, dest), Account: a.ID, AccountName: a.Name, Provider: a.Provider, Rule: r.ID, RuleName: r.Name, Job: j.ID, Trigger: j.Trigger, Note: "本地输出待核对，未重建或覆盖"}
			if outcome == "failed" {
				cp.activity.Error = j.Message
			}
			return true, nil
		}
	}
	return false, nil
}

func (e *Engine) generateScanMedia(ctx context.Context, a Account, r Rule, j *Job, d ScanDir, f Entry, source, dest string, m Media, content []byte, cp *ScanCheckpoint) error {
	if preserved, err := e.preserveRegisteredScanOutput(ctx, a, r, j, f, source, dest, m, content, cp); err != nil || preserved {
		return err
	}
	_, _, key, err := e.outputLocation(dest)
	if err != nil {
		return err
	}
	hash := contentHash(content)
	unchanged, err := cp.scan.unchanged(ctx, f, source)
	if err != nil {
		return err
	}
	// unchanged already compares the full previous observation (size and
	// modified included) against the current one, so a field that was
	// missing last time and is still missing compares equal and may use
	// the fast path; the registration and on-disk hash checks inside still
	// anchor the skip decision to the actual output.
	if unchanged {
		var owner, registered, raw string
		err = e.s.db.QueryRowContext(ctx, "SELECT o.media,o.hash,m.payload FROM outputs o JOIN media m ON m.id=o.media WHERE o.path=?", key).Scan(&owner, &registered, &raw)
		var old Media
		actual, readErr := e.outputHash(key)
		if err == nil && json.Unmarshal([]byte(raw), &old) == nil && owner == m.ID && registered == hash && actual == hash && readErr == nil && restoreRevision(old) == restoreRevision(m) {
			j.Skipped++
			j.IncrementalSkipped++
			return e.copyMediaSubtitles(ctx, a, cp.Files, f, d.Relative, r, j)
		}
	}
	// No provider calls while holding configMu: token refresh itself persists an
	// account. The same lock excludes config edits around intent and publication.
	e.s.configMu.RLock()
	item, replay, err := e.prepareScanItem(ctx, cp.scan, m, key, hash)
	if err != nil {
		e.s.configMu.RUnlock()
		return err
	}
	if err = e.hitScanFault("item.after_intent"); err != nil {
		e.s.configMu.RUnlock()
		return err
	}
	created, err := e.publishOutput(dest, m.ID, content, false)
	if err == nil {
		err = e.hitScanFault("item.after_file")
	}
	e.s.configMu.RUnlock()
	if err != nil {
		if ctx.Err() != nil || e.scanFault != nil {
			return err
		}
		j.Errors++
		j.Message = err.Error()
		j.ErrorKind = classifyError(err)
		cp.activity = &Activity{Time: time.Now().UnixMilli(), Kind: "generation", Event: "strm", Outcome: "failed", Media: m.ID, Name: f.Name, Path: source, Output: generationOutputName(e, dest), Account: a.ID, AccountName: a.Name, Provider: a.Provider, Rule: r.ID, RuleName: r.Name, Job: j.ID, Trigger: j.Trigger, Error: cleanRecordText(err.Error(), 300)}
		return nil
	}
	item.Outcome = "skipped"
	if created {
		j.CreatedFiles++
		item.Outcome = "created"
	} else if replay {
		j.RecoveredFiles++
		item.Outcome = "recovered"
	} else {
		j.Skipped++
	}
	item.Activity = Activity{Time: time.Now().UnixMilli(), Kind: "generation", Event: "strm", Outcome: item.Outcome, Media: m.ID, Name: f.Name, Path: source, Output: generationOutputName(e, dest), Account: a.ID, AccountName: a.Name, Provider: a.Provider, Rule: r.ID, RuleName: r.Name, Job: j.ID, Trigger: j.Trigger}
	if replay {
		item.Activity.Note = "恢复后确认输出；未推断中断前的写入次数"
	}
	cp.item = &item
	return e.copyMediaSubtitles(ctx, a, cp.Files, f, d.Relative, r, j)
}

func (e *Engine) commitScanItemTx(tx *sql.Tx, j Job, item scanItem) error {
	if err := scanItemOwnerTx(tx, item); err != nil {
		return err
	}
	if actual, err := e.outputHash(item.Key); err != nil || actual != item.Hash {
		return errors.New("输出在提交前已变化，已保留现场")
	}
	raw, _ := json.Marshal(item.Media)
	if _, err := tx.Exec("INSERT INTO media VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET account=excluded.account,remote_id=excluded.remote_id,source_path=excluded.source_path,payload=excluded.payload", item.Media.ID, item.Media.Account, item.Media.RemoteID, item.Media.Path, string(raw)); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO outputs VALUES(?,?,?) ON CONFLICT(path) DO UPDATE SET hash=excluded.hash", item.Key, item.Media.ID, item.Hash); err != nil {
		return err
	}
	if err := e.hitScanFault("item.after_registration"); err != nil {
		return err
	}
	created := int64(0)
	if item.Outcome == "created" {
		created = time.Now().UnixMilli()
	}
	if _, err := tx.Exec("INSERT INTO output_details(path,rule,job,created) VALUES(?,?,?,?) ON CONFLICT(path) DO UPDATE SET rule=excluded.rule,job=excluded.job", item.Key, j.Rule, j.ID, created); err != nil {
		return err
	}
	if err := writeScanActivityTx(tx, item.Activity); err != nil {
		return err
	}
	raw, _ = json.Marshal(item)
	if _, err := tx.Exec("UPDATE scan_items SET state='done',payload=? WHERE run=? AND remote=? AND state='intent'", string(raw), item.Run, item.Remote); err != nil {
		return err
	}
	return e.hitScanFault("item.before_commit")
}

func writeScanActivityTx(tx *sql.Tx, v Activity) error {
	v.Error = cleanRecordText(v.Error, 300)
	v.Note = cleanRecordText(v.Note, 500)
	raw, _ := json.Marshal(v)
	result, err := tx.Exec("INSERT INTO activity_events(kind,time,account,media,outcome,request,payload) VALUES(?,?,?,?,?,'',?)", v.Kind, v.Time, v.Account, v.Media, v.Outcome, string(raw))
	if err != nil {
		return err
	}
	if err = recordMetric(tx, v); err != nil {
		return err
	}
	if id, _ := result.LastInsertId(); id%128 == 0 {
		if _, err = tx.Exec(`DELETE FROM activity_events WHERE kind=? AND (time>0 AND time<? OR id IN (SELECT id FROM activity_events WHERE kind=? ORDER BY id DESC LIMIT -1 OFFSET 20000))`, v.Kind, time.Now().AddDate(0, 0, -90).UnixMilli(), v.Kind); err != nil {
			return err
		}
		_, err = tx.Exec("DELETE FROM metric_buckets WHERE bucket<?", time.Now().AddDate(0, 0, -32).UnixMilli()/60000)
	}
	return err
}
