package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const scanEntryLimit = 100000
const scanTableRows = 1000000
const scanTableBytes = 128 << 20
const scanScopeBytes = 128 << 10
const scanRunBytes = 48 << 20

// A baseline describes observed metadata from one complete covered scan. It
// never proves unchanged cloud bytes or authorizes deleting a local file.
type scanObservation struct {
	Entry  Entry  `json:"entry"`
	Source string `json:"source"`
}

type scanIdentity struct {
	Rule            Rule   `json:"rule"`
	AccountRevision int64  `json:"account_revision"`
	Folder          string `json:"folder"`
	Source          string `json:"source"`
	Base            string `json:"base"`
	Key             string `json:"key"`
	Mapping         string `json:"mapping"`
	Generation      string `json:"generation"`
}

type scanRun struct {
	ID       string         `json:"id"`
	Identity scanIdentity   `json:"identity"`
	Previous string         `json:"previous"`
	Baseline string         `json:"baseline"`
	Full     bool           `json:"full"`
	Created  int64          `json:"created"`
	Covered  []scanBaseline `json:"covered,omitempty"`
}

type scanBaseline struct {
	Scope    string `json:"scope"`
	Run      string `json:"run"`
	Identity string `json:"identity"`
	Compare  bool   `json:"compare"`
}

func scanContentIdentity(v scanIdentity) string {
	v.Folder, v.Source = v.Rule.Folder, v.Rule.SourcePath
	return restoreRevision(v)
}

// Capture every covered pointer, including incompatible content identities, so
// publication cannot silently discard a subtree completed after admission.
func scanCoveredBaselines(tx *sql.Tx, identity scanIdentity) ([]scanBaseline, error) {
	// Publish retires covered descendant pointers, so surviving descendants are
	// newer than their ancestor. Chronological overlay also selects the latest
	// positive identity when separate sibling scans observed a moved file.
	rows, err := tx.Query(`SELECT b.scope,b.run,b.identity,r.payload,r.status FROM scan_baselines b JOIN scan_runs r ON r.id=b.run WHERE b.rule=? ORDER BY r.completed,r.rowid,b.scope`, identity.Rule.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []scanBaseline
	bytes := 0
	for rows.Next() {
		var b scanBaseline
		var raw, status string
		if err = rows.Scan(&b.Scope, &b.Run, &b.Identity, &raw, &status); err != nil {
			return nil, err
		}
		if !scanSourceCovered(identity.Source, b.Scope) && !scanSourceCovered(b.Scope, identity.Source) {
			continue
		}
		if len(result) >= 1024 {
			return nil, errors.New("扫描子目录基线过多，请先合并较小范围")
		}
		var prior scanRun
		bytes += len(b.Scope) + len(b.Run) + len(b.Identity)
		if bytes > scanScopeBytes {
			return nil, errors.New("扫描范围索引过大，请缩小定点目录数量")
		}
		if restoreDecode([]byte(raw), &prior) != nil || prior.ID != b.Run || prior.Identity.Source != b.Scope || status != "complete" || restoreRevision(prior.Identity) != b.Identity {
			return nil, errors.New("扫描基线资料不一致")
		}
		b.Compare = scanContentIdentity(prior.Identity) == scanContentIdentity(identity)
		result = append(result, b)
	}
	return result, rows.Err()
}

type scanSession struct {
	s        *Store
	Run      scanRun
	Complete bool
}

func migrateScanHistory(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE scan_runs(id TEXT PRIMARY KEY,rule TEXT NOT NULL,scope TEXT NOT NULL,identity TEXT NOT NULL,status TEXT NOT NULL,created INTEGER NOT NULL,completed INTEGER NOT NULL,payload TEXT NOT NULL);
CREATE INDEX scan_run_rule ON scan_runs(rule,created);
CREATE TABLE scan_entries(run TEXT NOT NULL,remote TEXT NOT NULL,source TEXT NOT NULL,payload TEXT NOT NULL,listed TEXT NOT NULL,done INTEGER NOT NULL CHECK(done IN (0,1)),PRIMARY KEY(run,remote));
CREATE INDEX scan_entry_path ON scan_entries(run,source);
CREATE TABLE scan_baselines(rule TEXT NOT NULL,scope TEXT NOT NULL,run TEXT NOT NULL,identity TEXT NOT NULL,completed INTEGER NOT NULL,full_completed INTEGER NOT NULL,PRIMARY KEY(rule,scope));
CREATE TABLE scan_changes(run TEXT NOT NULL,remote TEXT NOT NULL,kind TEXT NOT NULL,before_payload TEXT NOT NULL,after_payload TEXT NOT NULL,PRIMARY KEY(run,remote,kind));
CREATE TABLE scan_items(run TEXT NOT NULL,remote TEXT NOT NULL,state TEXT NOT NULL,payload TEXT NOT NULL,PRIMARY KEY(run,remote));
CREATE TABLE change_plans(id TEXT PRIMARY KEY,created INTEGER NOT NULL,state TEXT NOT NULL,payload TEXT NOT NULL);
CREATE TABLE change_items(plan TEXT NOT NULL,position INTEGER NOT NULL,state TEXT NOT NULL,payload TEXT NOT NULL,PRIMARY KEY(plan,position));
CREATE TRIGGER change_plan_cleanup AFTER DELETE ON change_plans BEGIN DELETE FROM change_items WHERE plan=OLD.id; END;
CREATE TRIGGER scan_run_cleanup AFTER DELETE ON scan_runs BEGIN DELETE FROM scan_entries WHERE run=OLD.id; DELETE FROM scan_changes WHERE run=OLD.id; DELETE FROM scan_items WHERE run=OLD.id; END;`)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(fmt.Sprintf("ALTER TABLE scan_runs ADD COLUMN bytes INTEGER NOT NULL DEFAULT 0 CHECK(bytes>=0 AND bytes<=%d)", scanRunBytes)); err != nil {
		return err
	}
	if _, err = tx.Exec(fmt.Sprintf("CREATE TABLE scan_storage(name TEXT PRIMARY KEY,rows INTEGER NOT NULL CHECK(rows>=0 AND rows<=%d),bytes INTEGER NOT NULL CHECK(bytes>=0 AND bytes<=%d));", scanTableRows, scanTableBytes)); err != nil {
		return err
	}
	for _, spec := range scanTableSpecs() {
		columns := make([]string, 0, len(spec.text))
		for column := range spec.text {
			columns = append(columns, column)
		}
		sort.Strings(columns)
		size := func(prefix string) string {
			parts := []string{}
			for _, c := range columns {
				if c == "state" || c == "status" {
					parts = append(parts, "64")
				} else {
					parts = append(parts, "length(CAST("+prefix+"."+c+" AS BLOB))")
				}
			}
			return strings.Join(parts, "+")
		}
		q := fmt.Sprintf(`INSERT INTO scan_storage VALUES('%[1]s',0,0);
CREATE TRIGGER %[1]s_quota_insert AFTER INSERT ON %[1]s BEGIN UPDATE scan_storage SET rows=rows+1,bytes=bytes+(%[2]s) WHERE name='%[1]s'; END;
CREATE TRIGGER %[1]s_quota_delete AFTER DELETE ON %[1]s BEGIN UPDATE scan_storage SET rows=rows-1,bytes=bytes-(%[3]s) WHERE name='%[1]s'; END;
CREATE TRIGGER %[1]s_quota_update AFTER UPDATE ON %[1]s BEGIN UPDATE scan_storage SET bytes=bytes+(%[2]s)-(%[3]s) WHERE name='%[1]s'; END;`, spec.name, size("NEW"), size("OLD"))
		if _, err = tx.Exec(q); err != nil {
			return err
		}
		if spec.name == "scan_entries" || spec.name == "scan_items" || spec.name == "scan_changes" {
			q = fmt.Sprintf(`CREATE TRIGGER %[1]s_run_insert AFTER INSERT ON %[1]s BEGIN UPDATE scan_runs SET bytes=bytes+(%[2]s) WHERE id=NEW.run; END;
CREATE TRIGGER %[1]s_run_update AFTER UPDATE ON %[1]s BEGIN UPDATE scan_runs SET bytes=bytes+(%[2]s)-(%[3]s) WHERE id=NEW.run; END;
CREATE TRIGGER %[1]s_run_delete AFTER DELETE ON %[1]s BEGIN UPDATE scan_runs SET bytes=bytes-(%[3]s) WHERE id=OLD.run; END;`, spec.name, size("NEW"), size("OLD"))
			if _, err = tx.Exec(q); err != nil {
				return err
			}
		}
	}
	return nil
}

func scanRuleIdentity(r Rule) Rule { r.LastRun = 0; return r }

func (s *Store) scanIdentityTx(tx *sql.Tx, r Rule, folder, source, mapping string) (scanIdentity, error) {
	var accountRaw, ruleRaw string
	var current Rule
	var account Account
	if err := tx.QueryRow("SELECT payload FROM accounts WHERE id=?", r.Account).Scan(&accountRaw); err != nil {
		return scanIdentity{}, err
	}
	if err := json.Unmarshal([]byte(accountRaw), &account); err != nil || account.Disabled {
		return scanIdentity{}, errors.New("扫描账号已变化或停用")
	}
	if err := tx.QueryRow("SELECT payload FROM rules WHERE id=?", r.ID).Scan(&ruleRaw); err != nil {
		return scanIdentity{}, err
	}
	if err := json.Unmarshal([]byte(ruleRaw), &current); err != nil || restoreRevision(scanRuleIdentity(current)) != restoreRevision(scanRuleIdentity(r)) {
		return scanIdentity{}, errors.New("扫描规则已变化，请重新生成")
	}
	get := func(id string) (string, error) {
		var value string
		err := tx.QueryRow("SELECT value FROM settings WHERE id=?", id).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return value, err
	}
	base, err := get("public_base")
	if err != nil {
		return scanIdentity{}, err
	}
	generation := s.startupRestore.Generation
	return scanIdentity{scanRuleIdentity(r), account.Revision, folder, source, base, contentHash(s.playKey), mapping, generation}, nil
}

// startScanHistory excludes previews, limited scans and failed-directory retry
// jobs: their Job scope does not prove complete root coverage. Same-job restart
// reopens its durable observations and retains the original frozen identity.
func (s *Store) startScanHistory(ctx context.Context, j Job, r Rule, mapping string) (*scanSession, error) {
	if s.restoreRestricted {
		return nil, errors.New("恢复核对尚未完成，未创建扫描会话")
	}
	if j.DryRun || j.Limit != 0 || len(j.RetryDirs) > 0 {
		return nil, nil
	}
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	identity, err := s.scanIdentityTx(tx, r, j.Folder, j.SourcePath, mapping)
	if err != nil {
		return nil, err
	}
	digest := restoreRevision(identity)
	var raw, status string
	err = tx.QueryRow("SELECT payload,status FROM scan_runs WHERE id=?", j.ID).Scan(&raw, &status)
	if err == nil {
		var run scanRun
		if restoreDecode([]byte(raw), &run) != nil || run.ID != j.ID || restoreRevision(run.Identity) != digest || status != "active" && status != "complete" {
			return nil, errors.New("扫描身份已变化或已隔离，未复用旧基线")
		}
		return &scanSession{s, run, status == "complete"}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err = pruneScanHistoryTx(tx); err != nil {
		return nil, err
	}
	var scopes int
	if err = tx.QueryRow("SELECT COUNT(*) FROM scan_baselines WHERE rule=?", r.ID).Scan(&scopes); err != nil {
		return nil, err
	}
	if scopes >= 1024 && j.SourcePath != r.SourcePath {
		return nil, errors.New("定点目录基线已达上限，请先扫描规则根目录合并")
	}
	run := scanRun{ID: j.ID, Identity: identity, Full: true, Created: time.Now().Unix()}
	var previous, previousIdentity string
	var fullAt int64
	err = tx.QueryRow("SELECT run,identity,full_completed FROM scan_baselines WHERE rule=? AND scope=?", r.ID, j.SourcePath).Scan(&previous, &previousIdentity, &fullAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if previousIdentity == digest {
		run.Previous = previous
		run.Full = j.FullReconcile || !r.Incremental || fullAt < time.Now().Add(-24*time.Hour).Unix()
	}
	run.Baseline = previous
	run.Covered, err = scanCoveredBaselines(tx, identity)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(run)
	if len(payload) > 1<<20 || !validScanObservation(scanObservation{Entry: Entry{ID: j.Folder, Dir: true}, Source: j.SourcePath}) {
		return nil, errors.New("扫描范围或基线资料过大，请缩小范围")
	}
	if _, err = tx.Exec("INSERT INTO scan_runs(id,rule,scope,identity,status,created,completed,payload) VALUES(?,?,?,?,?,?,0,?)", run.ID, r.ID, j.SourcePath, digest, "active", run.Created, string(payload)); err != nil {
		return nil, err
	}
	root := scanObservation{Entry: Entry{ID: j.Folder, Dir: true}, Source: j.SourcePath}
	encoded, _ := json.Marshal(root)
	if _, err = tx.Exec("INSERT INTO scan_entries VALUES(?,?,?,?,'',0)", run.ID, j.Folder, j.SourcePath, string(encoded)); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &scanSession{s, run, false}, nil
}

func (session *scanSession) guard(tx *sql.Tx) error {
	if session.s.restoreRestricted {
		return errors.New("恢复核对尚未完成，未修改扫描资料")
	}
	current, err := session.s.scanIdentityTx(tx, session.Run.Identity.Rule, session.Run.Identity.Folder, session.Run.Identity.Source, session.Run.Identity.Mapping)
	if err != nil {
		return err
	}
	if restoreRevision(current) != restoreRevision(session.Run.Identity) {
		return errors.New("扫描期间账号、规则或播放入口已变化，未发布新基线")
	}
	var status, identity string
	if err = tx.QueryRow("SELECT status,identity FROM scan_runs WHERE id=?", session.Run.ID).Scan(&status, &identity); err != nil {
		return err
	}
	if status != "active" || identity != restoreRevision(session.Run.Identity) {
		return errors.New("扫描会话已失效")
	}
	return nil
}

func validScanObservation(v scanObservation) bool {
	if v.Entry.ID == "" || len(v.Entry.ID) > 4096 || !utf8.ValidString(v.Entry.ID) || len(v.Source) > 32768 || v.Entry.Size < 0 || len(v.Entry.Modified) > 4096 || len(v.Entry.Name) > 4096 || len(v.Entry.PickCode) > 4096 || !utf8.ValidString(v.Source+v.Entry.Name+v.Entry.Modified+v.Entry.PickCode) {
		return false
	}
	cleaned, err := validRemotePath(v.Source)
	return err == nil && cleaned == v.Source
}

// Listing admission happens before Visited or media writes. A remote identity
// has exactly one observation in a run; an identical replay is idempotent.
func (session *scanSession) recordListing(ctx context.Context, d ScanDir, files []Entry) error {
	session.s.configMu.RLock()
	defer session.s.configMu.RUnlock()
	tx, err := session.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = session.guard(tx); err != nil {
		return err
	}
	if !scanSourceCovered(session.Run.Identity.Source, d.Source) {
		return errors.New("目录不在当前扫描覆盖范围内")
	}
	var directoryRaw, listed string
	if err = tx.QueryRow("SELECT payload,listed FROM scan_entries WHERE run=? AND remote=?", session.Run.ID, d.ID).Scan(&directoryRaw, &listed); err != nil {
		return err
	}
	var directory scanObservation
	if restoreDecode([]byte(directoryRaw), &directory) != nil || !directory.Entry.Dir || directory.Source != d.Source {
		return errors.New("目录身份与已观测路径不一致")
	}
	ordered := append([]Entry(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	listingRevision := restoreRevision(ordered)
	if listed != "" && listed != listingRevision {
		return errors.New("重放的目录观测已变化，请重新完整扫描")
	}
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM scan_entries WHERE run=?", session.Run.ID).Scan(&count); err != nil {
		return err
	}
	identities := map[string]bool{}
	for _, f := range files {
		if identities[f.ID] {
			return errors.New("目录中存在重复远端身份，未发布扫描基线")
		}
		identities[f.ID] = true
		if _, err = safeName(f.Name); err != nil {
			return err
		}
		v := scanObservation{f, path.Join(d.Source, f.Name)}
		if !validScanObservation(v) {
			return errors.New("目录观测字段无效或过长")
		}
		encoded, _ := json.Marshal(v)
		var prior string
		err = tx.QueryRow("SELECT payload FROM scan_entries WHERE run=? AND remote=?", session.Run.ID, f.ID).Scan(&prior)
		if err == nil {
			if prior != string(encoded) {
				return errors.New("同一远端身份出现在不同路径或内容中，未发布扫描基线")
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		count++
		if count > scanEntryLimit {
			return errors.New("单次扫描观测数量超过限制，请缩小目录范围")
		}
		excluded := ""
		if f.Dir && session.Run.Identity.Rule.Filter.excluded(f.Name, path.Join(d.Relative, f.Name)) {
			excluded = "excluded"
		}
		if _, err = tx.Exec("INSERT INTO scan_entries VALUES(?,?,?,?,?,0)", session.Run.ID, f.ID, v.Source, string(encoded), excluded); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE scan_entries SET listed=? WHERE run=? AND remote=?", listingRevision, session.Run.ID, d.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (session *scanSession) unchanged(ctx context.Context, f Entry, source string) (bool, error) {
	if session.Run.Full || session.Run.Previous == "" {
		return false, nil
	}
	var raw string
	err := session.s.db.QueryRowContext(ctx, "SELECT payload FROM scan_entries WHERE run=? AND remote=? AND done=1", session.Run.Previous, f.ID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	encoded, _ := json.Marshal(scanObservation{f, source})
	return raw == string(encoded), nil
}

// Called inside the same transaction as the item cursor/claim. Filesystem
// publication and output registration are verified before this final commit.
func markScanObservationTx(tx *sql.Tx, run, remote string) error {
	if run == "" || remote == "" {
		return nil
	}
	result, err := tx.Exec("UPDATE scan_entries SET done=1 WHERE run=? AND remote=? AND (json_extract(payload,'$.entry.dir')=0 OR listed<>'')", run, remote)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("扫描条目完成记录缺失")
	}
	return nil
}

func (session *scanSession) publish(ctx context.Context) error {
	session.s.configMu.RLock()
	defer session.s.configMu.RUnlock()
	tx, err := session.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = session.guard(tx); err != nil {
		return err
	}
	var baseline string
	err = tx.QueryRow("SELECT run FROM scan_baselines WHERE rule=? AND scope=?", session.Run.Identity.Rule.ID, session.Run.Identity.Source).Scan(&baseline)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if baseline != session.Run.Baseline {
		return errors.New("扫描发布基线已变化，请重新扫描")
	}
	covered, err := scanCoveredBaselines(tx, session.Run.Identity)
	if err != nil {
		return err
	}
	if restoreRevision(covered) != restoreRevision(session.Run.Covered) {
		return errors.New("扫描期间子目录基线已变化，请重新扫描")
	}
	var incomplete int
	if err = tx.QueryRow("SELECT COUNT(*) FROM scan_entries WHERE run=? AND done=0", session.Run.ID).Scan(&incomplete); err != nil {
		return err
	}
	if incomplete != 0 {
		return errors.New("目录或条目尚未完整处理，未发布扫描基线")
	}
	previous := session.Run.Previous
	// Overlay complete subtrees, including their negative observations, before
	// retiring their pointers. Work stays in SQLite rather than a large Go map.
	if _, err = tx.Exec("CREATE TEMP TABLE scan_compare(remote TEXT PRIMARY KEY,source TEXT NOT NULL,payload TEXT NOT NULL)"); err != nil {
		return err
	}
	for _, b := range covered {
		if !b.Compare {
			continue
		}
		prefix := strings.TrimSuffix(b.Scope, "/") + "/"
		if _, err = tx.Exec("DELETE FROM scan_compare WHERE source=? OR substr(source,1,length(?))=?", b.Scope, prefix, prefix); err != nil {
			return err
		}
		comparePrefix := strings.TrimSuffix(session.Run.Identity.Source, "/") + "/"
		if _, err = tx.Exec(`INSERT INTO scan_compare SELECT remote,source,payload FROM scan_entries WHERE run=? AND json_extract(payload,'$.entry.dir')=0 AND (source=? OR substr(source,1,length(?))=?) ON CONFLICT(remote) DO UPDATE SET source=excluded.source,payload=excluded.payload`, b.Run, session.Run.Identity.Source, comparePrefix, comparePrefix); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT INTO scan_changes SELECT n.run,n.remote,CASE WHEN o.remote IS NULL THEN 'added' WHEN o.source<>n.source THEN 'moved' ELSE 'metadata' END,COALESCE(o.payload,''),n.payload FROM scan_entries n LEFT JOIN scan_compare o ON o.remote=n.remote WHERE n.run=? AND json_extract(n.payload,'$.entry.dir')=0 AND (o.remote IS NULL OR o.payload<>n.payload)`, session.Run.ID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO scan_changes SELECT ?,o.remote,'source_absent',o.payload,'' FROM scan_compare o LEFT JOIN scan_entries n ON n.run=? AND n.remote=o.remote WHERE n.remote IS NULL`, session.Run.ID, session.Run.ID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("DROP TABLE scan_compare"); err != nil {
		return err
	}
	completed := time.Now().Unix()
	var fullAt int64
	if session.Run.Full {
		fullAt = completed
	} else if err = tx.QueryRow("SELECT full_completed FROM scan_baselines WHERE rule=? AND scope=? AND run=?", session.Run.Identity.Rule.ID, session.Run.Identity.Source, previous).Scan(&fullAt); err != nil {
		return errors.New("扫描比较基线已变化")
	}
	if _, err = tx.Exec("UPDATE scan_runs SET status='complete',completed=? WHERE id=?", completed, session.Run.ID); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO scan_baselines VALUES(?,?,?,?,?,?) ON CONFLICT(rule,scope) DO UPDATE SET run=excluded.run,identity=excluded.identity,completed=excluded.completed,full_completed=excluded.full_completed", session.Run.Identity.Rule.ID, session.Run.Identity.Source, session.Run.ID, restoreRevision(session.Run.Identity), completed, fullAt); err != nil {
		return err
	}
	for _, b := range covered {
		if b.Scope == session.Run.Identity.Source || !scanSourceCovered(session.Run.Identity.Source, b.Scope) {
			continue
		}
		if _, err = tx.Exec("DELETE FROM scan_baselines WHERE rule=? AND scope=? AND run=?", session.Run.Identity.Rule.ID, b.Scope, b.Run); err != nil {
			return err
		}
	}
	var scopeBytes int
	if err = tx.QueryRow("SELECT COALESCE(SUM(length(CAST(scope AS BLOB))+length(CAST(run AS BLOB))+length(CAST(identity AS BLOB))),0) FROM scan_baselines WHERE rule=?", session.Run.Identity.Rule.ID).Scan(&scopeBytes); err != nil {
		return err
	}
	if scopeBytes > scanScopeBytes {
		return errors.New("定点目录索引已达容量限制，请扫描规则根目录合并")
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	session.Complete = true
	return nil
}

func scanSourceCovered(root, source string) bool {
	return source == root || strings.HasPrefix(source, strings.TrimSuffix(root, "/")+"/")
}

func pruneScanHistoryTx(tx *sql.Tx) error {
	// Also collect pointers left by old versions or out-of-band rule removal.
	if _, err := tx.Exec("DELETE FROM scan_baselines WHERE NOT EXISTS (SELECT 1 FROM rules WHERE rules.id=scan_baselines.rule)"); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM change_plans WHERE created<?", time.Now().Add(-24*time.Hour).Unix()); err != nil {
		return err
	}
	var pressure int
	if err := tx.QueryRow("SELECT COUNT(*) FROM scan_storage WHERE rows>? OR bytes>?", scanTableRows/2, scanTableBytes/2).Scan(&pressure); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM scan_runs WHERE (?=1 OR id IN (
 SELECT id FROM (SELECT id,ROW_NUMBER() OVER(PARTITION BY rule ORDER BY created DESC,rowid DESC) AS rank FROM scan_runs) WHERE rank>10
)) AND id NOT IN (SELECT run FROM scan_baselines)
AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.id=scan_runs.id AND (j.status IN ('queued','running') OR j.status='interrupted' AND json_extract(j.payload,'$.resumable')=1))
AND NOT EXISTS (SELECT 1 FROM scan_runs a JOIN jobs j ON j.id=a.id,json_tree(a.payload) ref WHERE a.status='active' AND (j.status IN ('queued','running') OR j.status='interrupted' AND json_extract(j.payload,'$.resumable')=1) AND ref.key IN ('run','previous') AND ref.value=scan_runs.id)
AND NOT EXISTS (SELECT 1 FROM change_plans p,json_tree(p.payload) ref WHERE ref.key='run' AND ref.value=scan_runs.id)`, boolInt(pressure > 0))
	return err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
