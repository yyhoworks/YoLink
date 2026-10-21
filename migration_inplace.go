package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const inplaceMode = "migration-inplace"

// inplaceOriginalBudget bounds how much original STRM text one plan retains
// in memory and on disk across all of its items. Items past the budget keep
// only their content hash; preview and conversion re-read those files and
// verify the hash before replacing anything (see previewInplace and
// convertInplace), so a missing Original never weakens the safety checks.
const inplaceOriginalBudget = 64 << 20

// A small durable checkpoint per item avoids rewriting the entire library plan
// for every STRM and allows recovery if the connection closes during a batch.
type inplaceProgress struct {
	Index    int    `json:"index"`
	Applied  bool   `json:"applied"`
	Error    string `json:"error,omitempty"`
	Restored bool   `json:"restored,omitempty"`
	Media    *Media `json:"media,omitempty"`
}

func (s *Server) checkpointInplace(p MaintenancePlan, index int) error {
	v := p.Items[index]
	name := filepath.Join(s.s.dir, "plans", p.ID+".progress")
	_, statErr := os.Stat(name)
	created := os.IsNotExist(statErr)
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	err = json.NewEncoder(f).Encode(inplaceProgress{Index: index, Applied: v.Applied, Error: v.Error, Restored: v.Content == "", Media: v.Media})
	if err == nil {
		err = f.Sync()
	}
	if err == nil && created {
		// The file's own Sync does not make its first directory entry
		// durable. Sync the plans directory once, best effort: some
		// platforms cannot sync a directory handle, and the record itself
		// is already synced above.
		if dir, openErr := os.Open(filepath.Dir(name)); openErr == nil {
			_ = dir.Sync()
			dir.Close()
		}
	}
	return err
}
func (s *Server) loadInplaceProgress(p *MaintenancePlan) error {
	f, err := os.Open(filepath.Join(s.s.dir, "plans", p.ID+".progress"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return errors.New("迁移进度无法读取，请保留备份")
	}
	// Records are appended one JSON object per line, each followed by Sync,
	// so a crash can tear only the final line, and only when it never got
	// its newline. Tolerate exactly that tail; every complete record stays
	// usable, while corruption anywhere else still refuses to load.
	trailingNewline := len(raw) == 0 || raw[len(raw)-1] == '\n'
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event inplaceProgress
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			if i == len(lines)-1 && !trailingNewline {
				break
			}
			return errors.New("迁移进度无法读取，请保留备份")
		}
		if event.Index < 0 || event.Index >= len(p.Items) {
			return errors.New("迁移进度范围无效")
		}
		v := &p.Items[event.Index]
		v.Applied = event.Applied
		v.Error = event.Error
		if event.Media != nil {
			v.Media = event.Media
		}
		if event.Restored {
			v.Content = ""
			v.Media = nil
		}
	}
	return nil
}

// The NAS folder is read on the server. Browser file uploads cannot identify
// an original server file safely enough to authorize replacing it.
func (s *Server) scanInplace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Directory string `json:"directory"`
	}
	if decode(w, r, &in) != nil || !filepath.IsAbs(filepath.FromSlash(in.Directory)) {
		fail(w, 400, "请选择容器内旧 STRM 目录的完整路径")
		return
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	root, rel, _, err := s.engine.outputLocation(in.Directory)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	folder, err := root.OpenRoot(rel)
	if err != nil {
		fail(w, 400, "旧 STRM 目录不可读取，请检查映射和权限")
		return
	}
	defer folder.Close()
	p := MaintenancePlan{ID: randomID(), Mode: inplaceMode, Directory: filepath.ToSlash(filepath.Clean(filepath.FromSlash(in.Directory))), Created: time.Now().Unix()}
	visited := 0
	var retainedOriginals int64
	err = fs.WalkDir(folder.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if r.Context().Err() != nil {
			return r.Context().Err()
		}
		if walkErr != nil {
			return errors.New("扫描目录失败，请检查子目录读取权限")
		}
		visited++
		if visited > 250000 {
			return errors.New("目录项目过多，请选择较小的子目录分批迁移")
		}
		if d.IsDir() {
			if name != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "@eaDir" || d.Name() == "#recycle") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(path.Ext(name), ".strm") {
			return nil
		}
		if len(p.Items) >= 50000 {
			return errors.New("每次最多扫描 50000 个 STRM，请选择子目录分批迁移")
		}
		dest := path.Join(p.Directory, name)
		_, _, key, e := s.engine.outputLocation(dest)
		if e != nil {
			return e
		}
		item := PlanItem{Path: key}
		if !d.Type().IsRegular() {
			item.Error = "不是普通文件，保留原文件"
		} else {
			raw, e := readMigrationFile(s.engine, item.Path)
			if e != nil {
				item.Error = e.Error()
			} else {
				item.OldHash = contentHash(raw)
				item.Storage, item.Source, e = parseMuvyo(raw)
				if e != nil {
					item.Error = e.Error()
				}
				// Retain the original text only within the plan-wide
				// budget. An over-budget item is recognizable by
				// Original == "" with OldHash set; preview re-reads and
				// hash-verifies it before use, and conversion always
				// re-reads before replacing.
				if retainedOriginals+int64(len(raw)) <= inplaceOriginalBudget {
					item.Original = string(raw)
					retainedOriginals += int64(len(raw))
				}
			}
		}
		p.Items = append(p.Items, item)
		return nil
	})
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(p.Items) == 0 {
		fail(w, 400, "此目录中没有 STRM 文件，请重新选择")
		return
	}
	if err = s.savePlan(p); err != nil {
		fail(w, 500, "扫描结果保存失败")
		return
	}
	jsonOut(w, 200, planSummary(p))
}

func readMigrationFile(e *Engine, name string) ([]byte, error) {
	root, rel, _, err := e.outputLocation(name)
	if err != nil {
		return nil, err
	}
	info, err := root.Lstat(rel)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("原 STRM 不是可读取的普通文件")
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, errors.New("原 STRM 不可读取")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, errors.New("STRM 读取失败或超过 64KB")
	}
	return raw, nil
}

func parseMuvyo(raw []byte) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(strings.TrimPrefix(string(raw), "\ufeff")))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "/redirect" || u.Fragment != "" {
		return "", "", errors.New("不是受支持的 /redirect 链接，保留原内容")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["storage"]) != 1 || len(q["path"]) != 1 || q.Get("storage") == "" {
		return "", "", errors.New("旧链接的来源或路径参数无效")
	}
	source, err := validRemotePath(q.Get("path"))
	if err != nil {
		return "", "", err
	}
	return q.Get("storage"), source, nil
}

func legacyProvider(storage string) string {
	v := strings.ToLower(storage)
	switch {
	case strings.HasPrefix(v, "yun139"):
		return "139"
	case strings.HasPrefix(v, "quark"):
		return "quark"
	case strings.HasPrefix(v, "ali"):
		return "ali"
	}
	return ""
}

func (s *Server) previewInplace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PlanID   string            `json:"plan_id"`
		Mapping  map[string]string `json:"mapping"`
		Prefixes map[string]struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"prefixes"`
	}
	if decode(w, r, &in) != nil {
		fail(w, 400, "迁移映射请求无效")
		return
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	original, err := s.loadPlan(in.PlanID)
	if err != nil || original.Mode != inplaceMode {
		fail(w, 400, "请先扫描 NAS 上的旧 STRM 目录")
		return
	}
	base, err := validBase(s.s.setting("public_base", ""))
	if err != nil {
		fail(w, 400, "请先设置播放入口")
		return
	}
	// Keep each prepared plan immutable: old tabs retain their exact mappings.
	p := MaintenancePlan{ID: randomID(), Mode: inplaceMode, Base: base, Directory: original.Directory, Created: time.Now().Unix(), Prepared: true}
	var retainedOriginals int64
	for _, old := range original.Items {
		item := PlanItem{Path: old.Path, OldHash: old.OldHash, Storage: old.Storage, Source: old.Source}
		originalText := old.Original
		var rereadErr error
		if originalText == "" && old.OldHash != "" {
			// The scan did not retain this original (plan-wide size
			// budget). Re-read it now and trust it only if it still
			// matches the hash recorded at scan time.
			raw, re := readMigrationFile(s.engine, old.Path)
			if re != nil || contentHash(raw) != old.OldHash {
				rereadErr = errors.New("原文未随扫描保留，重新读取后校验不一致，请重新扫描")
			} else {
				originalText = string(raw)
			}
		}
		storage, source, e := parseMuvyo([]byte(originalText))
		if rereadErr != nil {
			e = rereadErr
		}
		if e == nil && (old.OldHash == "" || contentHash([]byte(originalText)) != old.OldHash) {
			e = errors.New("原文件扫描校验无效，请重新扫描")
		}
		// The prepared plan observes the same retention budget as the scan.
		// Conversion never reads Original (it re-reads the file and checks
		// OldHash), so an unretained original costs only the "before" text
		// in plan summaries.
		if retainedOriginals+int64(len(originalText)) <= inplaceOriginalBudget {
			item.Original = originalText
			retainedOriginals += int64(len(originalText))
		}
		var a Account
		if e == nil {
			provider := legacyProvider(storage)
			a, err = s.s.account(in.Mapping[storage])
			if provider == "" {
				e = errors.New("此来源尚不支持原位迁移，保留原内容")
			} else if err != nil || a.Disabled || !strings.HasPrefix(a.Provider, provider) {
				e = errors.New("请映射到同类型的可用网盘账号")
			}
		}
		if e == nil {
			prefix := in.Prefixes[storage]
			if prefix.From != "" || prefix.To != "" {
				from, fe := validRemotePath(prefix.From)
				to, te := validRemotePath(prefix.To)
				if fe != nil || te != nil || (source != from && !strings.HasPrefix(source, strings.TrimRight(from, "/")+"/")) {
					e = errors.New("云端路径前缀无效或不匹配，保留原内容")
				} else {
					source = path.Join(to, strings.TrimPrefix(strings.TrimPrefix(source, from), "/"))
				}
			}
		}
		if e == nil {
			var owner string
			dbErr := s.s.db.QueryRow("SELECT media FROM outputs WHERE path=?", item.Path).Scan(&owner)
			if dbErr == nil {
				e = errors.New("此文件已有 YoLink 归属，请使用托管入口更新")
			} else if !errors.Is(dbErr, sql.ErrNoRows) {
				e = errors.New("输出归属读取失败")
			}
		}
		if e == nil {
			m := Media{ID: s.s.mediaID(a, "legacy:"+source), Account: a.ID, Name: path.Base(source), Path: source, Legacy: true}
			item.Media = &m
			item.Content = mediaContent(s.s, m, base)
		} else {
			item.Error = e.Error()
		}
		p.Items = append(p.Items, item)
	}
	if err = s.savePlan(p); err != nil {
		fail(w, 500, "迁移预览保存失败")
		return
	}
	jsonOut(w, 200, planSummary(p))
}

// Cache listings only within a bounded batch, including parent traversal.
type migrationListings struct {
	Clouds
	entries map[string][]Entry
}

func (c *migrationListings) List(ctx context.Context, account, folder string) ([]Entry, error) {
	key := account + "\x00" + folder
	if entries, ok := c.entries[key]; ok {
		return entries, nil
	}
	entries, err := c.Clouds.List(ctx, account, folder)
	if err == nil {
		c.entries[key] = entries
	}
	return entries, err
}

func (s *Server) inplaceBackup(p MaintenancePlan, item PlanItem) string {
	return filepath.Join(s.s.dir, "strm-backups", p.ID, "inplace", contentHash([]byte(item.Path))+".strm")
}

func (s *Server) applyInplace(w http.ResponseWriter, r *http.Request, p *MaintenancePlan) {
	var in struct {
		ConfirmDirectory string `json:"confirm_directory"`
		Limit            int    `json:"limit"`
		Retry            bool   `json:"retry"`
	}
	if decode(w, r, &in) != nil || !p.Prepared || in.ConfirmDirectory != p.Directory {
		fail(w, 400, "请先预览，并确认将覆盖的旧 STRM 目录")
		return
	}
	if in.Limit < 0 || in.Limit > 20 {
		fail(w, 400, "每批最多迁移 20 个 STRM")
		return
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	var active int
	if s.s.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE status IN ('running','queued')").Scan(&active) != nil || active > 0 {
		fail(w, 409, "请等待生成任务结束，并暂停新旧服务的自动生成")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	cached := &migrationListings{Clouds: s.clouds, entries: map[string][]Entry{}}
	if in.Retry {
		for i := range p.Items {
			if p.Items[i].Content != "" && !p.Items[i].Applied {
				p.Items[i].Error = ""
			}
		}
	}
	attempted := 0
	for i := range p.Items {
		item := &p.Items[i]
		if item.Content == "" || item.Applied || item.Error != "" {
			continue
		}
		if attempted >= in.Limit || ctx.Err() != nil {
			break
		}
		attempted++
		err := s.convertInplace(ctx, *p, item, cached)
		if err != nil {
			item.Error = err.Error()
		} else {
			item.Applied = true
		}
		if err = s.checkpointInplace(*p, i); err != nil {
			fail(w, 500, "本批结果保存失败，备份仍保留；请重新读取同一预览后继续")
			return
		}
	}
	jsonOut(w, 200, planSummary(*p))
}

func (s *Server) convertInplace(ctx context.Context, p MaintenancePlan, item *PlanItem, cached Clouds) error {
	if item.Media == nil || !strings.EqualFold(path.Ext(item.Path), ".strm") {
		return errors.New("迁移媒体映射无效")
	}
	if !withinPath(filepath.FromSlash(absoluteOutputPath(s.engine, item.Path)), filepath.FromSlash(p.Directory)) {
		return errors.New("原文件不在本次迁移目录内")
	}
	raw, err := readMigrationFile(s.engine, item.Path)
	if err != nil {
		return err
	}
	nextHash := contentHash([]byte(item.Content))
	currentHash := contentHash(raw)
	backup := s.inplaceBackup(p, *item)
	backed, backupErr := os.ReadFile(backup)
	recovering := currentHash == nextHash && backupErr == nil && contentHash(backed) == item.OldHash
	if currentHash != item.OldHash && !recovering {
		return errors.New("扫描后原文件已改变，保留当前内容，请重新扫描")
	}
	var owner, hash string
	dbErr := s.s.db.QueryRow("SELECT media,hash FROM outputs WHERE path=?", item.Path).Scan(&owner, &hash)
	if dbErr == nil && (!recovering || owner != item.Media.ID || hash != nextHash) {
		return errors.New("文件已被其他任务接管，保留当前内容")
	}
	if dbErr != nil && !errors.Is(dbErr, sql.ErrNoRows) {
		return errors.New("输出归属读取失败")
	}
	a, err := s.s.account(item.Media.Account)
	if err != nil || a.Disabled {
		return errors.New("映射账号不可用")
	}
	if !recovering {
		resolver := &Server{s: s.s, engine: &Engine{clouds: cached}, clouds: cached}
		resolved, e := resolver.resolveLegacy(ctx, *item.Media)
		if e != nil {
			return errors.New("云端文件核对失败，保留原内容：" + e.Error())
		}
		item.Media = &resolved
	} else {
		m, e := s.s.media(item.Media.ID)
		if e != nil || m.RemoteID == "" {
			return errors.New("已写入文件的媒体索引缺失，请从备份恢复")
		}
		item.Media = &m
	}
	if err = os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
		return errors.New("备份目录创建失败")
	}
	if !recovering {
		f, e := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e == nil {
			_, e = f.Write(raw)
			if e == nil {
				e = f.Sync()
			}
			closeErr := f.Close()
			if e != nil || closeErr != nil {
				return errors.New("原 STRM 备份写入失败")
			}
		} else if backupErr != nil || contentHash(backed) != item.OldHash {
			return errors.New("已有备份校验失败，保留原内容")
		}
		// Provider requests can be slow; recheck after resolving and before publishing.
		current, e := readMigrationFile(s.engine, item.Path)
		if e != nil || contentHash(current) != item.OldHash {
			return errors.New("核对期间文件已改变，保留当前内容")
		}
		if err = s.engine.replaceFile(item.Path, []byte(item.Content)); err != nil {
			return err
		}
	}
	_, err = s.s.db.Exec("INSERT INTO outputs VALUES(?,?,?) ON CONFLICT(path) DO NOTHING", item.Path, item.Media.ID, nextHash)
	if err != nil {
		return errors.New("文件已更新，归属保存失败；请继续同一预览以恢复登记")
	}
	return nil
}

func (s *Server) rollbackInplace(w http.ResponseWriter, r *http.Request, p *MaintenancePlan) {
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	var active int
	if s.s.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE status IN ('running','queued')").Scan(&active) != nil || active > 0 {
		fail(w, 409, "请先等待生成任务结束")
		return
	}
	count := 0
	for i := range p.Items {
		item := &p.Items[i]
		if item.Content == "" || item.Media == nil {
			continue
		}
		if !item.Applied {
			raw, e := readMigrationFile(s.engine, item.Path)
			if e != nil || contentHash(raw) != contentHash([]byte(item.Content)) {
				continue
			}
		}
		if count >= 100 || r.Context().Err() != nil {
			break
		}
		count++
		item.Error = ""
		backup, err := os.ReadFile(s.inplaceBackup(*p, *item))
		raw, readErr := readMigrationFile(s.engine, item.Path)
		if err != nil || contentHash(backup) != item.OldHash {
			item.Error = "备份不存在或校验失败"
			if s.checkpointInplace(*p, i) != nil {
				fail(w, 500, "恢复进度保存失败")
				return
			}
			continue
		}
		if item.Media == nil || readErr != nil || (contentHash(raw) != contentHash([]byte(item.Content)) && contentHash(raw) != item.OldHash) {
			item.Error = "迁移后文件已被修改，保留当前内容"
			if s.checkpointInplace(*p, i) != nil {
				fail(w, 500, "恢复进度保存失败")
				return
			}
			continue
		}
		var owner, hash string
		dbErr := s.s.db.QueryRow("SELECT media,hash FROM outputs WHERE path=?", item.Path).Scan(&owner, &hash)
		if (dbErr != nil && !errors.Is(dbErr, sql.ErrNoRows)) || (dbErr == nil && (owner != item.Media.ID || hash != contentHash([]byte(item.Content)))) {
			item.Error = "输出归属已改变，保留当前内容"
			if s.checkpointInplace(*p, i) != nil {
				fail(w, 500, "恢复进度保存失败")
				return
			}
			continue
		}
		if contentHash(raw) != item.OldHash {
			err = s.engine.replaceFile(item.Path, backup)
		}
		if err == nil {
			_, err = s.s.db.Exec("DELETE FROM outputs WHERE path=? AND media=? AND hash=?", item.Path, item.Media.ID, contentHash([]byte(item.Content)))
		}
		if err != nil {
			item.Error = "恢复失败，请保留备份并重试"
			if s.checkpointInplace(*p, i) != nil {
				fail(w, 500, "恢复进度保存失败")
				return
			}
			continue
		}
		item.Applied = false
		item.Content = ""
		item.Media = nil
		if err = s.checkpointInplace(*p, i); err != nil {
			fail(w, 500, "恢复结果保存失败，请重新读取同一预览")
			return
		}
	}
	if err := s.savePlan(*p); err != nil {
		fail(w, 500, "恢复结果保存失败，请保留备份并重新读取预览")
		return
	}
	jsonOut(w, 200, planSummary(*p))
}
