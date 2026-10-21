package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
)

type IngestEvent struct {
	ID       string `json:"id"`
	Rule     string `json:"rule"`
	Path     string `json:"path"`
	Status   string `json:"status"`
	Job      string `json:"job"`
	Error    string `json:"error"`
	Created  int64  `json:"created"`
	Revision int64  `json:"revision"`
}

func (s *Store) ingestEvents() ([]IngestEvent, error) {
	return s.ingestEventsFiltered(nil)
}

// ingestEventsFiltered is ingestEvents with a rule predicate applied BEFORE
// the result is truncated to the newest 30 matches. Combining the old
// global LIMIT 30 with caller-side rule filtering let one busy rule's
// events crowd every other rule out of a per-rule client's view. The table
// holds one row per (rule, directory) — the event ID is a deterministic
// hash of both — so scanning without a SQL limit stays bounded, and
// iteration stops as soon as 30 matches are collected.
func (s *Store) ingestEventsFiltered(allow func(rule string) bool) ([]IngestEvent, error) {
	rows, e := s.db.Query(`SELECT i.id,i.rule,i.source_path,COALESCE(j.status,i.status),i.job,i.error,i.created,i.revision FROM ingest_events i LEFT JOIN jobs j ON j.id=i.job ORDER BY i.created DESC`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []IngestEvent{}
	for rows.Next() {
		var v IngestEvent
		if e = rows.Scan(&v.ID, &v.Rule, &v.Path, &v.Status, &v.Job, &v.Error, &v.Created, &v.Revision); e != nil {
			return nil, e
		}
		if allow != nil && !allow(v.Rule) {
			continue
		}
		out = append(out, v)
		if len(out) >= 30 {
			break
		}
	}
	return out, rows.Err()
}
func (s *Server) ingestEvent(w http.ResponseWriter, r *http.Request, in jobRequest) {
	if in.DryRun || in.Limit != 0 || in.Folder != "" || in.SourcePath != "" {
		fail(w, 400, "定点入库使用 path 和 kind；预览或数量限制请使用普通生成任务接口")
		return
	}
	if in.Rule == "" || in.Path == "" {
		fail(w, 400, "入库事件需要规则 ID 和完整云端路径")
		return
	}
	rule, e := s.s.rule(in.Rule)
	if e != nil {
		fail(w, 404, "目录规则不存在")
		return
	}
	a, e := s.s.account(rule.Account)
	if e != nil || a.Disabled {
		fail(w, 409, "所属网盘不可用或已停用")
		return
	}
	source, e := validRemotePath(in.Path)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	if in.Kind == "file" {
		source = path.Dir(source)
	} else if in.Kind != "" && in.Kind != "directory" {
		fail(w, 400, "kind 应为 file 或 directory")
		return
	}
	if source != rule.SourcePath && !strings.HasPrefix(source, strings.TrimSuffix(rule.SourcePath, "/")+"/") {
		fail(w, 400, "入库路径不在规则范围内")
		return
	}
	sum := sha256.Sum256([]byte(rule.ID + "\x00" + source))
	id := hex.EncodeToString(sum[:16])
	// Only a finished event may be re-armed: a failed one, or a submitted
	// one whose scan job is no longer queued/running — the terminal state
	// lives on the job row, and a finished job never runs again. Resetting
	// a pending, queued or running event here would let a duplicate or
	// replayed notification detach the in-flight job and orphan its scan.
	_, e = s.s.db.Exec(`INSERT INTO ingest_events(id,rule,source_path,status,job,error,created,revision) VALUES(?,?,?,'pending','','',?,1) ON CONFLICT(id) DO UPDATE SET
		status=CASE WHEN ingest_events.status='failed' OR (ingest_events.status='submitted' AND NOT EXISTS (SELECT 1 FROM jobs WHERE jobs.id=ingest_events.job AND jobs.status IN ('queued','running'))) THEN 'pending' ELSE ingest_events.status END,
		job=CASE WHEN ingest_events.status='failed' OR (ingest_events.status='submitted' AND NOT EXISTS (SELECT 1 FROM jobs WHERE jobs.id=ingest_events.job AND jobs.status IN ('queued','running'))) THEN '' ELSE ingest_events.job END,
		error=CASE WHEN ingest_events.status='failed' OR (ingest_events.status='submitted' AND NOT EXISTS (SELECT 1 FROM jobs WHERE jobs.id=ingest_events.job AND jobs.status IN ('queued','running'))) THEN '' ELSE ingest_events.error END,
		created=excluded.created,revision=ingest_events.revision+1`, id, rule.ID, source, time.Now().Unix())
	if e != nil {
		fail(w, 500, "入库事件保存失败")
		return
	}
	jsonOut(w, 202, object{"id": id, "status": "pending", "source_path": source, "note": "已排队，只扫描该目录及其子目录"})
}
func (e *Engine) ingestWorker() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.processIngest()
		}
	}
}
func (e *Engine) processIngest() {
	if e.s.restoreRestricted {
		return
	}
	// Select one oldest event per idle rule before applying the batch limit.
	// A busy rule's backlog must not hide work for unrelated rules.
	rows, err := e.s.db.Query(`WITH candidates AS (
 SELECT id,rule,source_path,revision,created,
 ROW_NUMBER() OVER (PARTITION BY rule ORDER BY created,id) AS position
 FROM ingest_events WHERE status='pending'
 ) SELECT i.id,i.rule,i.source_path,i.revision FROM candidates i
 WHERE i.position=1 AND NOT EXISTS (
 SELECT 1 FROM jobs j WHERE j.status IN ('running','queued')
 AND json_extract(j.payload,'$.rule')=i.rule
 ) ORDER BY i.created,i.id LIMIT 32`)
	if err != nil {
		return
	}
	pending := []IngestEvent{}
	for rows.Next() {
		var v IngestEvent
		if rows.Scan(&v.ID, &v.Rule, &v.Path, &v.Revision) == nil {
			pending = append(pending, v)
		}
	}
	rows.Close()
	for _, v := range pending {
		if e.ctx.Err() != nil {
			return
		}
		r, er := e.s.rule(v.Rule)
		if er != nil {
			e.ingestFail(v, "目录规则已移除")
			continue
		}
		var active int
		if e.s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status IN ('running','queued') AND json_extract(payload,'$.rule')=?`, v.Rule).Scan(&active) != nil {
			continue
		}
		if active > 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(e.ctx, 90*time.Second)
		folder, er := e.folderAtPathRetry(ctx, r, v.Path)
		cancel()
		if er != nil {
			if e.ctx.Err() == nil {
				e.ingestFail(v, er.Error())
			}
			continue
		}
		j, er := e.enqueueJob(r.ID, folder, v.Path, false, 0, "ingest", "")
		if er != nil {
			if strings.Contains(er.Error(), "已有任务") || strings.Contains(er.Error(), "队列已满") {
				continue
			}
			e.ingestFail(v, er.Error())
			continue
		}
		_, _ = e.s.db.Exec(`UPDATE ingest_events SET status='submitted',job=? WHERE id=? AND revision=?`, j.ID, v.ID, v.Revision)
	}
}
func (e *Engine) ingestFail(v IngestEvent, message string) {
	result, err := e.s.db.Exec(`UPDATE ingest_events SET status='failed',error=? WHERE id=? AND revision=?`, message, v.ID, v.Revision)
	if err != nil {
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return
	}
	body := v.Path + "\n" + cleanRecordText(message, 300)
	e.s.notify("目录读取失败", body, "error", "ingest:"+v.ID)
	r, err := e.s.rule(v.Rule)
	if err == nil && r.Notify {
		e.queueNotifications(fmt.Sprintf("ingest:%s:%d", v.ID, v.Revision), object{"title": "目录读取失败", "text": body, "event": "ingest_failed", "path": v.Path})
	}
}

// folderAtPath error classification: a same-named directory already exists
// in the cloud, so the target is ambiguous no matter how long we wait —
// that error is deterministic and must not be retried. The directory not
// having appeared yet, and any List failure (network, token refresh), are
// transient: the notification typically races the upload that creates the
// directory.
var (
	errIngestDirNotFound   = errors.New("入库目录尚未出现在网盘，请稍后重试事件")
	errIngestDirAmbiguous  = errors.New("云端存在同名目录，无法确定入库位置")
	ingestLocateRetryWaits = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
)

// folderAtPathRetry retries folderAtPath a few times with growing backoff
// for transient failures only; a deterministic ambiguity fails at once, and
// exhausting the retries returns the last error so the caller marks the
// event failed exactly as before.
func (e *Engine) folderAtPathRetry(ctx context.Context, r Rule, source string) (string, error) {
	folder, err := e.folderAtPath(ctx, r, source)
	for _, wait := range ingestLocateRetryWaits {
		if err == nil || errors.Is(err, errIngestDirAmbiguous) {
			return folder, err
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
		folder, err = e.folderAtPath(ctx, r, source)
	}
	return folder, err
}

func (e *Engine) folderAtPath(ctx context.Context, r Rule, source string) (string, error) {
	if source == r.SourcePath {
		return r.Folder, nil
	}
	current := r.Folder
	relative := strings.TrimPrefix(source, strings.TrimRight(r.SourcePath, "/"))
	for _, name := range strings.Split(strings.Trim(relative, "/"), "/") {
		files, err := e.clouds.List(ctx, r.Account, current)
		if err != nil {
			return "", err
		}
		found := ""
		for _, f := range files {
			if f.Dir && f.Name == name {
				if found != "" {
					return "", errIngestDirAmbiguous
				}
				found = f.ID
			}
		}
		if found == "" {
			return "", errIngestDirNotFound
		}
		current = found
	}
	return current, nil
}
func (s *Server) retryIngest(w http.ResponseWriter, r *http.Request) {
	result, e := s.s.db.Exec(`UPDATE ingest_events SET status='pending',job='',error='',revision=revision+1,created=? WHERE id=? AND status='failed'`, time.Now().Unix(), r.PathValue("id"))
	if e != nil {
		fail(w, 500, "入库事件重试失败")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		fail(w, 409, "只有目录读取失败的入库事件可在这里重试；生成失败请重试对应任务")
		return
	}
	jsonOut(w, 202, object{"ok": true})
}
