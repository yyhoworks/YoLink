package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

type ScanDir struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Relative string `json:"relative"`
}
type ScanCheckpoint struct {
	Format         int
	Snapshot       bool
	Todo           []ScanDir
	Current        *ScanDir
	Files          []Entry
	Index          int
	Visited        map[string]bool
	Claimed        map[string]string
	persisted      bool
	structureDirty bool
	filesDirty     bool
	claimPath      string
	claimMedia     string
}
type PreviewItem struct {
	Source  string `json:"source"`
	Output  string `json:"output"`
	Outcome string `json:"outcome"`
}

func (e *Engine) saveScan(j Job, cp *ScanCheckpoint) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	old, err := e.s.job(j.ID)
	if err != nil {
		return err
	}
	if old.Status == "cancelled" {
		return context.Canceled
	}
	tx, err := e.s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = saveJobTx(tx, j); err != nil {
		return err
	}
	if err = saveCheckpointTx(tx, j.ID, cp); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	cp.persisted, cp.structureDirty, cp.filesDirty = true, false, false
	cp.claimPath, cp.claimMedia = "", ""
	return nil
}
func (e *Engine) noteFailure(j *Job, d ScanDir, message string) {
	if j.ErrorKind == "" {
		j.ErrorKind = errorKind(message)
	}
	_, _ = e.s.db.Exec("INSERT INTO job_failures VALUES(?,?,?,?) ON CONFLICT(job,folder,source) DO UPDATE SET message=excluded.message", j.ID, d.ID, d.Source, cleanRecordText(message, 300))
}

// Derive output components exactly as recursive scanning does, without changing
// the original cloud path used for directory lookup and media identity.
func scanRelativePath(root, source string) (string, error) {
	root, err := validRemotePath(root)
	if err != nil {
		return "", err
	}
	source, err = validRemotePath(source)
	if err != nil {
		return "", err
	}
	if source == root {
		return "", nil
	}
	prefix := strings.TrimSuffix(root, "/") + "/"
	if !strings.HasPrefix(source, prefix) {
		return "", errors.New("所选目录不在规则范围内")
	}
	parts := strings.Split(strings.TrimPrefix(source, prefix), "/")
	for i, part := range parts {
		parts[i], err = safeName(part)
		if err != nil {
			return "", err
		}
	}
	return strings.Join(parts, "/"), nil
}

func (e *Engine) retryDirectories(j Job) ([]ScanDir, error) {
	out := []ScanDir{}
	seen := map[string]bool{}
	sourceRoot := j.SnapshotPath(e.s)
	add := func(d ScanDir) error {
		if !seen[d.ID] {
			// Reconstruct canonically: valid checkpoint paths remain unchanged,
			// including when older targeted jobs saved unsanitized components.
			var err error
			d.Relative, err = scanRelativePath(sourceRoot, d.Source)
			if err != nil {
				return err
			}
			out = append(out, d)
			seen[d.ID] = true
		}
		return nil
	}
	var cp ScanCheckpoint
	if err := e.s.loadCheckpoint(j.ID, &cp); err == nil {
		if cp.Current != nil {
			if err := add(*cp.Current); err != nil {
				return nil, err
			}
		}
		for _, d := range cp.Todo {
			if err := add(d); err != nil {
				return nil, err
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	rows, err := e.s.db.Query("SELECT folder,source FROM job_failures WHERE job=?", j.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d ScanDir
		if err := rows.Scan(&d.ID, &d.Source); err != nil {
			return nil, err
		}
		if err := add(d); err != nil {
			return nil, err
		}
	}
	return out, rows.Err()
}
func (j Job) SnapshotPath(s *Store) string {
	if j.Snapshot != nil {
		return j.Snapshot.SourcePath
	}
	r, _ := s.rule(j.Rule)
	return r.SourcePath
}
func (e *Engine) previewOutput(dest, mid string, content []byte) string {
	root, rel, key, err := e.outputLocation(dest)
	if err != nil {
		return "conflict"
	}
	var owner string
	err = e.s.db.QueryRow("SELECT media FROM outputs WHERE path=?", key).Scan(&owner)
	if err == nil && owner != mid {
		return "conflict"
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "conflict"
	}
	f, err := root.Open(rel)
	if os.IsNotExist(err) {
		return "new"
	}
	if err != nil {
		return "conflict"
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, int64(len(content))+1))
	if err == nil && string(raw) == string(content) {
		return "same"
	}
	return "conflict"
}
func addPreview(j *Job, source, output, outcome string) {
	if len(j.Preview) < 100 {
		j.Preview = append(j.Preview, PreviewItem{source, output, outcome})
	}
}
func (e *Engine) scan(ctx context.Context, a Account, r Rule, j *Job) error {
	base := e.s.setting("public_base", "")
	if !j.DryRun && base == "" {
		return errors.New("请先在设置中填写播放器可访问的服务地址")
	}
	relative, err := scanRelativePath(r.SourcePath, j.SourcePath)
	if err != nil {
		return err
	}
	cp := ScanCheckpoint{Todo: []ScanDir{{j.Folder, j.SourcePath, relative}}, Visited: map[string]bool{}, Claimed: map[string]string{}}
	if len(j.RetryDirs) > 0 {
		cp.Todo = j.RetryDirs
	}
	if err := e.s.loadCheckpoint(j.ID, &cp); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Also repair output paths in pre-fix checkpoints on automatic resume.
	for i := range cp.Todo {
		cp.Todo[i].Relative, err = scanRelativePath(r.SourcePath, cp.Todo[i].Source)
		if err != nil {
			return err
		}
	}
	if cp.Current != nil {
		cp.Current.Relative, err = scanRelativePath(r.SourcePath, cp.Current.Source)
		if err != nil {
			return err
		}
	}
	if cp.Visited == nil {
		cp.Visited = map[string]bool{}
	}
	if cp.Claimed == nil {
		cp.Claimed = map[string]string{}
	}
	// Canonical path repairs above must be persisted even on a migrated cursor.
	cp.structureDirty = true
	for cp.Current != nil || len(cp.Todo) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if cp.Current == nil {
			d := cp.Todo[len(cp.Todo)-1]
			cp.Todo = cp.Todo[:len(cp.Todo)-1]
			if cp.Visited[d.ID] {
				continue
			}
			if len(cp.Visited) > 100000 {
				return errors.New("目录数超过上限，请缩小范围")
			}
			cp.Current = &d
			cp.Files = nil
			cp.Index = 0
			cp.structureDirty, cp.filesDirty = true, true
			if err := e.saveScan(*j, &cp); err != nil {
				return err
			}
		}
		d := *cp.Current
		j.CurrentPath = d.Source
		if cp.Files == nil {
			files, err := e.clouds.List(ctx, a.ID, d.ID)
			if err != nil {
				e.noteFailure(j, d, err.Error())
				return fmt.Errorf("列目录失败：%w", err)
			}
			cp.Files = files
			if cp.Files == nil {
				cp.Files = []Entry{}
			}
			cp.structureDirty, cp.filesDirty = true, true
			if err = e.saveScan(*j, &cp); err != nil {
				return err
			}
		}
		for cp.Index < len(cp.Files) {
			if err := ctx.Err(); err != nil {
				return err
			}
			f := cp.Files[cp.Index]
			source := path.Join(d.Source, f.Name)
			relative := path.Join(d.Relative, f.Name)
			j.CurrentPath = source
			before := j.Errors
			committed := *j
			err := e.scanEntry(ctx, a, r, j, d, f, source, relative, &cp)
			if err != nil {
				// The item cursor has not advanced. Persisting its tentative
				// counters would count it twice on restart and could satisfy a
				// generation limit before later media have been processed.
				*j = committed
				if ctx.Err() != nil {
					return ctx.Err()
				}
				e.noteFailure(j, d, err.Error())
				return err
			}
			if j.Errors > before {
				e.noteFailure(j, d, j.Message)
			}
			cp.Index++
			if err = e.saveScan(*j, &cp); err != nil {
				*j = committed
				return err
			}
			if j.Limit > 0 && j.Seen >= j.Limit {
				return nil
			}
		}
		cp.Visited[d.ID] = true
		cp.Current = nil
		cp.Files = nil
		cp.Index = 0
		cp.structureDirty, cp.filesDirty = true, true
		if err := e.saveScan(*j, &cp); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) scanEntry(ctx context.Context, a Account, r Rule, j *Job, d ScanDir, f Entry, source, relative string, cp *ScanCheckpoint) error {
	if r.Filter.excluded(f.Name, relative) {
		j.Filtered++
		if j.DryRun {
			addPreview(j, source, "", "filtered")
		}
		return nil
	}
	name, err := safeName(f.Name)
	if err != nil {
		j.Errors++
		j.Message = err.Error()
		return nil
	}
	if f.Dir {
		cp.Todo = append(cp.Todo, ScanDir{f.ID, source, path.Join(d.Relative, name)})
		cp.structureDirty = true
		return nil
	}
	if !r.Filter.media(f) {
		if mediaExtensions[strings.ToLower(path.Ext(f.Name))] {
			j.Filtered++
			if j.DryRun {
				addPreview(j, source, "", "filtered")
			}
		} else {
			return e.copyNonMedia(ctx, a, f, d.Relative, r, j)
		}
		return nil
	}
	j.Seen++
	dest := path.Join(r.Output, strings.TrimSuffix(path.Join(d.Relative, name), path.Ext(name))+".strm")
	m := e.mediaForGeneration(a, f, source, dest)
	mid := m.ID
	if len(j.Samples) < 10 {
		j.Samples = append(j.Samples, dest)
	}
	if other, ok := cp.Claimed[dest]; ok && other != mid {
		if j.DryRun {
			j.PreviewConflicts++
			addPreview(j, source, absoluteOutputPath(e, dest), "conflict")
			e.generationActivity(a, r, *j, f, source, generationOutputName(e, dest), "preview", "strm", nil)
			return nil
		}
		j.Errors++
		j.Message = "同名媒体输出冲突：" + dest
		e.generationActivity(a, r, *j, f, source, generationOutputName(e, dest), "failed", "strm", errors.New(j.Message))
		return nil
	}
	cp.Claimed[dest] = mid
	cp.claimPath, cp.claimMedia = dest, mid
	content := []byte(e.s.setting("public_base", "") + "/s/" + mid + "/" + url.PathEscape(f.Name) + "?k=" + e.s.signature(mid) + "\n")
	if j.DryRun {
		outcome := e.previewOutput(dest, mid, content)
		switch outcome {
		case "new":
			j.CreatedFiles++
		case "same":
			j.PreviewSame++
		case "conflict":
			j.PreviewConflicts++
		}
		addPreview(j, source, generationOutputName(e, dest), outcome)
		e.generationActivity(a, r, *j, f, source, generationOutputName(e, dest), "preview", "strm", nil)
		return nil
	}
	if err = e.s.saveMedia(m); err != nil {
		return errors.New("保存媒体索引失败")
	}
	created, err := e.publish(dest, mid, content)
	outcome := "created"
	if err != nil {
		j.Errors++
		j.Message = err.Error()
		j.ErrorKind = classifyError(err)
		if len(j.Failures) < 20 {
			j.Failures = append(j.Failures, err.Error())
		}
		outcome = "failed"
	} else if created {
		j.CreatedFiles++
	} else {
		j.Skipped++
		outcome = "skipped"
	}
	e.generationActivity(a, r, *j, f, source, generationOutputName(e, dest), outcome, "strm", err)
	if err == nil {
		_, _, key, _ := e.outputLocation(dest)
		createdAt := int64(0)
		if created {
			createdAt = time.Now().UnixMilli()
		}
		_, dbErr := e.s.db.Exec("INSERT INTO output_details(path,rule,job,created) VALUES(?,?,?,?) ON CONFLICT(path) DO UPDATE SET rule=excluded.rule,job=excluded.job", key, r.ID, j.ID, createdAt)
		if dbErr != nil {
			return errors.New("保存输出明细失败")
		}
		return e.copyMediaSubtitles(ctx, a, cp.Files, f, d.Relative, r, j)
	}
	return nil
}
