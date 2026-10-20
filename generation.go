package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
)

// The overview counts persisted records; it never claims a whole-drive total.
type GenerationOverview struct {
	Media   int `json:"media"`
	Outputs int `json:"outputs"`
	Queued  int `json:"queued"`
	Running int `json:"running"`
	Failed  int `json:"failed"`
}

func (s *Store) generationOverview() (GenerationOverview, error) {
	var o GenerationOverview
	queries := []struct {
		q    string
		dest *int
	}{
		{"SELECT COUNT(*) FROM media", &o.Media},
		{"SELECT COUNT(*) FROM outputs WHERE lower(path) LIKE '%.strm'", &o.Outputs},
		{"SELECT COUNT(*) FROM jobs WHERE status='queued'", &o.Queued},
		{"SELECT COUNT(*) FROM jobs WHERE status='running'", &o.Running},
		{`SELECT COUNT(*) FROM jobs WHERE status IN ('failed','partial','interrupted') AND COALESCE(json_extract(payload,'$.retried_by'),'')=''`, &o.Failed},
	}
	for _, q := range queries {
		if e := s.db.QueryRow(q.q).Scan(q.dest); e != nil {
			return o, e
		}
	}
	return o, nil
}
func (s *Store) unresolvedJobs() ([]Job, error) {
	rows, e := s.db.Query(`SELECT payload FROM jobs WHERE status IN ('failed','partial','interrupted') AND COALESCE(json_extract(payload,'$.retried_by'),'')='' ORDER BY created DESC,rowid DESC LIMIT 1000`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var raw string
		var j Job
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &j); e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func saveJobTx(tx *sql.Tx, j Job) error {
	p, e := json.Marshal(j)
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO jobs VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,payload=excluded.payload", j.ID, j.Created, j.Status, string(p))
	return e
}
func (s *Store) saveRetriedJob(next Job, previous Job) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	previous.RetriedBy = next.ID
	if e = saveJobTx(tx, next); e != nil {
		return e
	}
	if e = saveJobTx(tx, previous); e != nil {
		return e
	}
	return tx.Commit()
}
func (e *Engine) retry(id string) (Job, error) {
	j, er := e.s.job(id)
	if er != nil {
		return Job{}, errors.New("任务不存在")
	}
	return e.enqueue(j.Rule, j.Folder, j.SourcePath, j.DryRun, j.Limit, id)
}
func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	j, e := s.engine.retry(r.PathValue("id"))
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	jsonOut(w, 202, j)
}
func (s *Server) retryFailed(w http.ResponseWriter, r *http.Request) {
	// One retried scope per rule can be active. The response identifies any
	// deferred scopes so a bulk action never silently loses failures.
	jobs, e := s.s.unresolvedJobs()
	if e != nil {
		fail(w, 500, "失败任务读取失败")
		return
	}
	started := []Job{}
	deferred := []object{}
	for _, old := range jobs {
		next, er := s.engine.retry(old.ID)
		if er != nil {
			deferred = append(deferred, object{"id": old.ID, "reason": er.Error()})
			continue
		}
		started = append(started, next)
	}
	jsonOut(w, 202, object{"started": started, "deferred": deferred})
}
func (s *Server) removeRule(w http.ResponseWriter, r *http.Request) {
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	s.s.mu.Lock()
	defer s.s.mu.Unlock()
	id := r.PathValue("id")
	if _, e := s.s.rule(id); e != nil {
		fail(w, 404, "目录规则不存在")
		return
	}
	var active int
	if e := s.s.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE status IN ('running','queued') AND json_extract(payload,'$.rule')=?", id).Scan(&active); e != nil {
		fail(w, 500, "任务状态读取失败")
		return
	}
	if active > 0 {
		fail(w, 409, "该目录还有生成任务，请先取消或等待完成")
		return
	}
	if _, e := s.s.db.Exec("DELETE FROM rules WHERE id=?", id); e != nil {
		fail(w, 500, "移除目录规则失败")
		return
	}
	jsonOut(w, 200, object{"ok": true})
}
