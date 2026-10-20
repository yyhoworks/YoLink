package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var mediaExtensions = map[string]bool{}

func init() {
	for _, x := range strings.Fields("mkv mp4 m4v avi mov ts m2ts mts wmv flv mpg mpeg webm iso rmvb flac m4a mp3 wav ape aac ogg opus wma dsf dff") {
		mediaExtensions["."+x] = true
	}
}

type Engine struct {
	companionDownload func(context.Context, PlaybackTarget) ([]byte, error)
	subtitleDownload  func(context.Context, PlaybackTarget) ([]byte, error)
	s                 *Store
	clouds            Clouds
	root              *os.Root
	outputMounts      []outputMount
	queue             chan string
	mu                sync.Mutex
	cancel            map[string]context.CancelFunc
	ctx               context.Context
	cancelEngine      context.CancelFunc
	wg                sync.WaitGroup
}

func newEngine(ctx context.Context, s *Store, c Clouds, output string) (*Engine, error) {
	if e := os.MkdirAll(output, 0755); e != nil {
		return nil, e
	}
	root, e := os.OpenRoot(output)
	if e != nil {
		return nil, e
	}
	child, stop := context.WithCancel(ctx)
	en := &Engine{s: s, clouds: c, root: root, queue: make(chan string, 128), cancel: map[string]context.CancelFunc{}, ctx: child, cancelEngine: stop}
	abs, _ := filepath.Abs(output)
	en.outputMounts = []outputMount{{Path: filepath.ToSlash(filepath.Clean(abs)), Default: true, root: root}}
	en.discoverOutputMounts()
	en.wg.Add(4)
	go func() { defer en.wg.Done(); en.worker() }()
	go func() { defer en.wg.Done(); en.scheduler() }()
	go func() { defer en.wg.Done(); en.ingestWorker() }()
	go func() { defer en.wg.Done(); en.deliveryWorker() }()
	jobs, _ := s.jobs(10000)
	for i := len(jobs) - 1; i >= 0; i-- {
		if jobs[i].Status == "queued" {
			en.queue <- jobs[i].ID
		}
	}
	return en, nil
}
func (e *Engine) Close() {
	e.cancelEngine()
	e.wg.Wait()
	for _, m := range e.outputMounts {
		_ = m.root.Close()
	}
}
func safeRelative(v string) (string, error) {
	v = strings.ReplaceAll(strings.TrimSpace(v), "\\", "/")
	if v == "" || path.IsAbs(v) || strings.Contains(v, ":") || strings.ContainsRune(v, 0) {
		return "", errors.New("输出子目录必须是相对路径，例如 test/电影")
	}
	for _, part := range strings.Split(v, "/") {
		if part == ".." || part == "." || part == "" {
			return "", errors.New("输出目录不能包含空层级、. 或 ..")
		}
	}
	return v, nil
}
func validRemotePath(v string) (string, error) {
	if v == "" || !strings.HasPrefix(v, "/") || strings.Contains(v, "\\") || strings.ContainsRune(v, 0) {
		return "", errors.New("云端路径必须从 / 开始")
	}
	for _, s := range strings.Split(v, "/") {
		if s == ".." || s == "." {
			return "", errors.New("云端路径不能包含 . 或 ..")
		}
	}
	return path.Clean(v), nil
}
func safeName(v string) (string, error) {
	if v == "" || v == "." || v == ".." || strings.ContainsAny(v, "/\\\x00") {
		return "", errors.New("云端文件名包含路径分隔符，已跳过")
	}
	v = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(":*?\"<>|", r) {
			return '_'
		}
		return r
	}, v)
	v = strings.TrimRight(v, ". ")
	if v == "" {
		return "", errors.New("文件名无法用于本地输出")
	}
	return v, nil
}
func (e *Engine) enqueue(ruleID, folder, sourcePath string, dry bool, limit int, retryOf ...string) (Job, error) {
	trigger, retry := "manual", ""
	if len(retryOf) > 0 {
		trigger = "retry"
		retry = retryOf[0]
	}
	return e.enqueueJob(ruleID, folder, sourcePath, dry, limit, trigger, retry)
}
func (e *Engine) enqueueJob(ruleID, folder, sourcePath string, dry bool, limit int, trigger, retryOf string) (Job, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := e.s.rule(ruleID)
	if err != nil {
		return Job{}, errors.New("同步规则不存在")
	}
	a, err := e.s.account(r.Account)
	if err != nil || a.Disabled {
		return Job{}, errors.New("规则所属网盘不可用或已停用，请先在网盘管理中启用")
	}
	if folder == "" {
		folder = r.Folder
		sourcePath = r.SourcePath
	} else {
		p, er := validRemotePath(sourcePath)
		if er != nil {
			return Job{}, er
		}
		sourcePath = p
		if sourcePath != r.SourcePath && !strings.HasPrefix(sourcePath, strings.TrimSuffix(r.SourcePath, "/")+"/") {
			return Job{}, errors.New("所选目录不在规则范围内")
		}
	}
	// Folder identity is verified by walking from the rule root during execution.
	jobs, err := e.s.jobs(1000)
	if err != nil {
		return Job{}, err
	}
	for _, j := range jobs {
		if j.Rule == ruleID && (j.Status == "running" || j.Status == "queued") {
			return Job{}, errors.New("此规则已有任务正在执行")
		}
	}
	if limit < 0 || limit > 1000000 {
		return Job{}, errors.New("生成上限应在 0 到 1000000 之间，0 表示不限")
	}
	if len(e.queue) == cap(e.queue) {
		return Job{}, errors.New("任务队列已满，请稍后重试")
	}
	j := Job{Resumable: true, Snapshot: &r, ID: randomID(), Rule: ruleID, Folder: folder, SourcePath: sourcePath, DryRun: dry, Limit: limit, Status: "queued", Created: time.Now().Unix(), Samples: []string{}, Trigger: trigger}
	if retryOf != "" {
		previous, er := e.s.job(retryOf)
		if er != nil {
			return j, errors.New("原任务不存在")
		}
		if previous.RetriedBy != "" {
			return j, errors.New("这个失败已有重试任务")
		}
		if previous.Status != "failed" && previous.Status != "partial" && previous.Status != "interrupted" {
			return j, errors.New("仅失败或中断的任务可以重试")
		}
		if previous.Rule != ruleID || previous.Folder != folder || previous.SourcePath != sourcePath || previous.Limit != limit || previous.DryRun != dry {
			return j, errors.New("重试范围与原任务不一致")
		}
		// Directory IDs belong to the original account and mapping. Never combine
		// saved failure/checkpoint directories with a different rule snapshot.
		if previous.Snapshot == nil {
			return j, errors.New("原任务缺少目录规则快照，无法安全重试，请按当前规则重新生成")
		}
		old := previous.Snapshot
		if trigger == "health-recovery" && !recoveryRuleMatches(old, r) {
			return j, errors.New("目录规则已改变，无法恢复旧任务，请按当前规则重新生成")
		}
		if old.Account != r.Account || old.Folder != r.Folder || old.SourcePath != r.SourcePath || old.Output != r.Output {
			return j, errors.New("目录规则映射已改变，无法重试旧任务，请按当前规则重新生成")
		}
		j.RetryOf = previous.ID
		if previous.Trigger == "ingest" {
			j.Trigger = "ingest"
		}
		j.RetryDirs, err = e.retryDirectories(previous)
		if err != nil {
			return j, err
		}
		err = e.s.saveRetriedJob(j, previous)
	} else {
		err = e.s.saveJob(j)
	}
	if err != nil {
		return j, err
	}
	e.queue <- j.ID
	return j, nil
}
func (e *Engine) stop(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, err := e.s.job(id)
	if err != nil {
		return err
	}
	if j.Status != "running" && j.Status != "queued" {
		return errors.New("此任务已经结束")
	}
	if c := e.cancel[id]; c != nil {
		c()
	}
	j.Status = "cancelled"
	j.Message = "任务已取消，已生成文件保留"
	j.Finished = time.Now().Unix()
	return e.s.saveJob(j)
}
func (e *Engine) worker() {
	for {
		select {
		case <-e.ctx.Done():
			return
		case id := <-e.queue:
			e.run(id)
		}
	}
}
func (e *Engine) scheduler() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-t.C:
			rules, err := e.s.rules()
			if err != nil {
				continue
			}
			for _, r := range rules {
				if r.Enabled && r.Interval >= 60 && time.Now().Unix()-r.LastRun >= int64(r.Interval) {
					_, _ = e.enqueueJob(r.ID, "", "", false, 0, "scheduled", "")
				}
			}
		}
	}
}
func (e *Engine) saveProgress(j Job) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cur, err := e.s.job(j.ID)
	if err != nil {
		return err
	}
	if cur.Status == "cancelled" {
		return context.Canceled
	}
	return e.s.saveJob(j)
}
func (e *Engine) run(id string) {
	e.mu.Lock()
	j, err := e.s.job(id)
	if err != nil || j.Status != "queued" {
		e.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(e.ctx)
	e.cancel[id] = cancel
	j.Status = "running"
	if j.Started == 0 {
		j.Started = time.Now().Unix()
	}
	err = e.s.saveJob(j)
	e.mu.Unlock()
	if err != nil {
		cancel()
		return
	}
	defer func() { cancel(); e.mu.Lock(); delete(e.cancel, id); e.mu.Unlock() }()
	r, err := e.s.rule(j.Rule)
	if err != nil {
		j.Status = "failed"
		j.Message = "规则不存在"
		j.Finished = time.Now().Unix()
		_ = e.saveProgress(j)
		return
	}
	if j.Snapshot != nil {
		r = *j.Snapshot
	}
	// Timestamp even failed scans so invalid credentials do not cause a hot retry loop.
	e.s.mu.Lock()
	currentRule, _ := e.s.rule(r.ID)
	currentRule.LastRun = time.Now().Unix()
	_ = e.s.saveRule(currentRule)
	e.s.mu.Unlock()
	a, err := e.s.account(r.Account)
	if err == nil {
		err = e.verifyScope(ctx, r, j.Folder, j.SourcePath)
	}
	if err == nil {
		err = e.scan(ctx, a, r, &j)
	}
	if errors.Is(err, context.Canceled) && e.ctx.Err() != nil {
		j.Status = "interrupted"
		j.Message = "服务停止，断点已保存，下次启动自动续跑"
	} else if errors.Is(err, context.Canceled) {
		j.Status = "cancelled"
		j.Message = "任务已取消，已生成文件保留"
	} else if err != nil {
		j.Status = "failed"
		j.Message = err.Error()
	} else if j.Errors > 0 {
		j.Status = "partial"
		j.Message = "部分文件未生成：" + j.Message
	} else {
		j.Status = "completed"
		if j.Limit > 0 && j.Seen >= j.Limit {
			j.Message = "已达到本次生成上限"
		} else {
			j.Message = "扫描完成"
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		j.Errors++
	}
	if j.Status == "failed" || j.Status == "partial" {
		if err != nil {
			j.ErrorKind = classifyError(err)
		} else if j.ErrorKind == "" {
			j.ErrorKind = errorKind(j.Message)
		}
	} else {
		j.ErrorKind = ""
	}
	j.Finished = time.Now().Unix()
	outcome := "completed"
	if j.Status != "completed" {
		outcome = j.Status
	} else if j.DryRun {
		outcome = "preview"
	}
	note := fmt.Sprintf("%s；STRM 新增 %d、跳过 %d、失败 %d；字幕 %d；非媒体 %d", j.Message, j.CreatedFiles, j.Skipped, j.Errors, j.Sidecars, j.NonMediaFiles)
	if j.DryRun {
		note = fmt.Sprintf("范围预览，未写入文件；匹配 %d 个媒体；%s", j.Seen, j.Message)
	}
	e.s.appendActivity(Activity{Kind: "generation", Event: "task", Outcome: outcome, Name: r.Name, Path: j.SourcePath, Output: generationOutputName(e, r.Output), Account: a.ID, AccountName: a.Name, Provider: a.Provider, Rule: r.ID, RuleName: r.Name, Job: j.ID, Trigger: j.Trigger, Millis: (j.Finished - j.Created) * 1000, Note: note})
	if j.Status != "interrupted" {
		e.afterGeneration(j, r)
	}
	if saveErr := e.saveProgress(j); saveErr == nil && (j.Status == "completed" || j.Status == "cancelled") {
		_, _ = e.s.db.Exec("DELETE FROM job_checkpoints WHERE job=?", j.ID)
	}
}
func (e *Engine) verifyScope(ctx context.Context, r Rule, folder, sourcePath string) error {
	if folder == r.Folder && sourcePath == r.SourcePath {
		return nil
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(sourcePath, r.SourcePath), "/")
	if rel == "" {
		return errors.New("目录 ID 与规则根目录不一致")
	}
	current := r.Folder
	for _, part := range strings.Split(rel, "/") {
		entries, err := e.clouds.List(ctx, r.Account, current)
		if err != nil {
			return err
		}
		found := ""
		for _, f := range entries {
			if f.Dir && f.Name == part {
				if found != "" {
					return errors.New("云端存在同名目录，请缩小规则范围")
				}
				found = f.ID
			}
		}
		if found == "" {
			return errors.New("无法验证指定目录属于当前规则")
		}
		current = found
	}
	if current != folder {
		return errors.New("指定目录 ID 与云端路径不一致")
	}
	return nil
}
func contentHash(b []byte) string { x := sha256.Sum256(b); return hex.EncodeToString(x[:]) }
func (e *Engine) publish(dest, mid string, content []byte) (bool, error) {
	root, relative, key, locationErr := e.outputLocation(dest)
	if locationErr != nil {
		return false, locationErr
	}
	dest = key
	var owner, hash string
	er := e.s.db.QueryRow("SELECT media,hash FROM outputs WHERE path=?", dest).Scan(&owner, &hash)
	if er != nil && !errors.Is(er, sql.ErrNoRows) {
		return false, errors.New("无法读取输出归属记录")
	}
	if er == nil && owner != mid {
		return false, errors.New("输出名称冲突，已保留原文件：" + dest)
	}
	existing, er := root.Open(relative)
	if er == nil {
		digest := sha256.New()
		n, readErr := io.Copy(digest, io.LimitReader(existing, int64(len(content))+1))
		existing.Close()
		if readErr != nil {
			return false, errors.New("已有文件无法读取：" + dest)
		}
		if n != int64(len(content)) || hex.EncodeToString(digest.Sum(nil)) != contentHash(content) {
			return false, errors.New("已有文件内容不同，已保留：" + dest)
		}
		_, er = e.s.db.Exec("INSERT INTO outputs VALUES(?,?,?) ON CONFLICT(path) DO UPDATE SET hash=excluded.hash", dest, mid, contentHash(content))
		return false, er
	}
	if !os.IsNotExist(er) {
		return false, errors.New("输出文件不可访问：" + dest)
	}
	if er = root.MkdirAll(filepath.Dir(relative), 0755); er != nil {
		return false, errors.New("无法创建输出目录，请检查挂载权限")
	}
	temp := filepath.Join(filepath.Dir(relative), ".strmlink-"+randomID()+".tmp")
	f, er := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if er != nil {
		return false, errors.New("无法创建输出文件，请检查挂载权限")
	}
	defer root.Remove(temp)
	_, er = f.Write(content)
	if er == nil {
		er = f.Sync()
	}
	ce := f.Close()
	if er != nil || ce != nil {
		return false, errors.New("输出写入失败，可能磁盘已满")
	}
	// Hard-link publication is atomic and fails if the destination already exists.
	// Both paths are confined by os.Root, including symlink resolution.
	if er = root.Link(temp, relative); er != nil {
		return false, errors.New("发布文件失败（目标已存在，或卷不支持硬链接）：" + dest)
	}
	_, er = e.s.db.Exec("INSERT INTO outputs VALUES(?,?,?) ON CONFLICT(path) DO UPDATE SET hash=excluded.hash", dest, mid, contentHash(content))
	if er != nil {
		return true, errors.New("文件已生成但归属记录保存失败，请重新执行")
	}
	return true, nil
}
