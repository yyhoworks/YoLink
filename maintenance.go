package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type PlanItem struct {
	Path     string `json:"path"`
	OldHash  string `json:"old_hash,omitempty"`
	Content  string `json:"content,omitempty"`
	Media    *Media `json:"media,omitempty"`
	Error    string `json:"error,omitempty"`
	Applied  bool   `json:"applied"`
	Storage  string `json:"storage,omitempty"`
	Source   string `json:"source,omitempty"`
	Original string `json:"original,omitempty"`
}
type MaintenancePlan struct {
	ID        string     `json:"id"`
	Mode      string     `json:"mode"`
	Base      string     `json:"base"`
	Created   int64      `json:"created"`
	Items     []PlanItem `json:"items"`
	Directory string     `json:"directory,omitempty"`
	Prepared  bool       `json:"prepared,omitempty"`
}

func (s *Server) planPath(id string) (string, error) {
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", errors.New("预览 ID 无效")
	}
	return filepath.Join(s.s.dir, "plans", id+".json"), nil
}
func (s *Server) savePlan(p MaintenancePlan) error {
	dest, e := s.planPath(p.ID)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		return e
	}
	raw, e := json.Marshal(p)
	if e != nil {
		return e
	}
	temp := dest + ".tmp"
	if e = os.WriteFile(temp, raw, 0600); e != nil {
		return e
	}
	return os.Rename(temp, dest)
}
func (s *Server) loadPlan(id string) (MaintenancePlan, error) {
	var p MaintenancePlan
	dest, e := s.planPath(id)
	if e != nil {
		return p, e
	}
	raw, e := os.ReadFile(dest)
	if e != nil {
		return p, errors.New("预览不存在")
	}
	e = json.Unmarshal(raw, &p)
	if e == nil && p.Mode == inplaceMode {
		e = s.loadInplaceProgress(&p)
	}
	return p, e
}
func planSummary(p MaintenancePlan) object {
	ready, applied, failed, pending := 0, 0, 0, 0
	storage := map[string]int{}
	samples := []object{}
	for _, v := range p.Items {
		if v.Error != "" {
			failed++
		}
		if v.Content != "" {
			ready++
		}
		if v.Applied {
			applied++
		}
		if v.Content != "" && !v.Applied && v.Error == "" {
			pending++
		}
		if v.Storage != "" {
			storage[v.Storage]++
		}
		if len(samples) < 12 {
			samples = append(samples, object{"path": v.Path, "error": v.Error, "applied": v.Applied, "storage": v.Storage, "source": v.Source, "target": v.Media, "before": v.Original, "after": v.Content})
		}
	}
	return object{"id": p.ID, "mode": p.Mode, "total": len(p.Items), "ready": ready, "applied": applied, "skipped": failed, "samples": samples, "pending": pending, "storage": storage, "directory": p.Directory, "prepared": p.Prepared}
}
func mediaContent(s *Store, m Media, base string) string {
	return base + "/s/" + m.ID + "/" + url.PathEscape(m.Name) + "?k=" + s.signature(m.ID) + "\n"
}
func (s *Server) previewLinks(w http.ResponseWriter, r *http.Request) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	base, e := validBase(s.s.setting("public_base", ""))
	if e != nil {
		fail(w, 400, "请先设置播放入口")
		return
	}
	p := MaintenancePlan{ID: randomID(), Mode: "links", Base: base, Created: time.Now().Unix()}
	rows, e := s.s.db.Query("SELECT path,media,hash FROM outputs WHERE lower(path) LIKE '%.strm'")
	if e != nil {
		fail(w, 500, "输出记录读取失败")
		return
	}
	type output struct{ path, media, hash string }
	outputs := []output{}
	for rows.Next() {
		var v output
		if rows.Scan(&v.path, &v.media, &v.hash) == nil {
			outputs = append(outputs, v)
		}
	}
	rows.Close()
	for _, v := range outputs {
		item := PlanItem{Path: v.path, OldHash: v.hash}
		f, e := s.engine.openOutput(v.path)
		if e != nil {
			item.Error = "文件不可读取"
		} else {
			raw, er := io.ReadAll(io.LimitReader(f, 65537))
			f.Close()
			if er != nil || len(raw) > 65536 || contentHash(raw) != v.hash {
				item.Error = "文件已被外部修改，保留原内容"
			} else {
				m, er := s.s.media(v.media)
				if er != nil {
					item.Error = "媒体索引不存在"
				} else {
					item.Content = mediaContent(s.s, m, base)
					if string(raw) == item.Content {
						continue
					}
				}
			}
		}
		p.Items = append(p.Items, item)
	}
	if e = s.savePlan(p); e != nil {
		fail(w, 500, "预览保存失败")
		return
	}
	jsonOut(w, 200, planSummary(p))
}
func (s *Server) previewMigration(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PlanID string `json:"plan_id"`
		Files  []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"files"`
		Mapping map[string]string `json:"mapping"`
	}
	if e := decode(w, r, &in); e != nil || len(in.Files) == 0 || len(in.Files) > 500 {
		fail(w, 400, "每批请选择 1 到 500 个 STRM 文件")
		return
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	base, e := validBase(s.s.setting("public_base", ""))
	if e != nil {
		fail(w, 400, "请先设置播放入口")
		return
	}
	p := MaintenancePlan{ID: randomID(), Mode: "migration", Base: base, Created: time.Now().Unix()}
	if in.PlanID != "" {
		p, e = s.loadPlan(in.PlanID)
		if e != nil || p.Mode != "migration" || p.Base != base {
			fail(w, 400, "迁移预览不可继续，请重新选择文件")
			return
		}
	}
	if len(p.Items)+len(in.Files) > 50000 {
		fail(w, 400, "单次预览最多 50000 个文件")
		return
	}
	seen := map[string]bool{}
	for _, v := range p.Items {
		seen[v.Path] = true
	}
	for _, file := range in.Files {
		rel, er := safeRelative(strings.ReplaceAll(file.Path, "\\", "/"))
		if er != nil || !strings.EqualFold(path.Ext(rel), ".strm") {
			fail(w, 400, "迁移文件路径无效")
			return
		}
		dest := path.Join("migrated", p.ID[:8], rel)
		if seen[dest] {
			continue
		}
		seen[dest] = true
		item := PlanItem{Path: dest}
		u, er := url.Parse(strings.TrimSpace(strings.TrimPrefix(file.Content, "\ufeff")))
		if er != nil || u.Path != "/redirect" {
			item.Error = "不是受支持的 /redirect 链接"
		} else {
			source, er := validRemotePath(u.Query().Get("path"))
			aid := in.Mapping[u.Query().Get("storage")]
			a, ae := s.s.account(aid)
			if er != nil {
				item.Error = "原云端路径无效"
			} else if aid == "" || ae != nil || a.Disabled {
				item.Error = "来源网盘尚未映射到可用账号"
			} else {
				mid := s.s.mediaID(a, "legacy:"+source)
				m := Media{ID: mid, Account: a.ID, Name: path.Base(source), Path: source, Legacy: true}
				item.Media = &m
				item.Content = mediaContent(s.s, m, base)
			}
		}
		p.Items = append(p.Items, item)
	}
	if e = s.savePlan(p); e != nil {
		fail(w, 500, "迁移预览保存失败")
		return
	}
	jsonOut(w, 200, planSummary(p))
}
func (s *Server) applyPlan(w http.ResponseWriter, r *http.Request) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	p, e := s.loadPlan(r.PathValue("id"))
	if e != nil {
		fail(w, 404, e.Error())
		return
	}
	if p.Base != s.s.setting("public_base", "") {
		fail(w, 409, "播放入口已改变，请重新预览")
		return
	}
	if p.Mode == inplaceMode {
		s.applyInplace(w, r, &p)
		return
	}
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	var active int
	if e = s.s.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE status IN ('running','queued')").Scan(&active); e != nil || active > 0 {
		fail(w, 409, "请等待生成任务结束后再应用预览")
		return
	}
	for i := range p.Items {
		if r.Context().Err() != nil {
			break
		}
		v := &p.Items[i]
		if v.Content == "" || v.Applied {
			continue
		}
		v.Error = ""
		if p.Mode == "migration" {
			if v.Media == nil {
				v.Error = "媒体映射缺失"
				continue
			}
			a, er := s.s.account(v.Media.Account)
			if er != nil || a.Disabled {
				v.Error = "映射账号不可用"
				continue
			}
			if er = s.s.saveMedia(*v.Media); er == nil {
				_, er = s.engine.publish(v.Path, v.Media.ID, []byte(v.Content))
			}
			e = er
		} else {
			e = s.replacePlanned(p, *v, false)
		}
		if e != nil {
			v.Error = e.Error()
		} else {
			v.Applied = true
		}
	}
	if e = s.savePlan(p); e != nil {
		fail(w, 500, "结果保存失败；备份仍保留，请勿重复覆盖")
		return
	}
	jsonOut(w, 200, planSummary(p))
}
func (s *Server) replacePlanned(p MaintenancePlan, v PlanItem, rollback bool) error {
	_, rel, _, e := s.engine.outputLocation(v.Path)
	if e != nil {
		return e
	}
	f, e := s.engine.openOutput(v.Path)
	if e != nil {
		return errors.New("输出文件不可读取")
	}
	raw, e := io.ReadAll(io.LimitReader(f, 65537))
	f.Close()
	if e != nil || len(raw) > 65536 {
		return errors.New("输出文件内容异常")
	}
	backupRel := rel
	if filepath.IsAbs(filepath.FromSlash(v.Path)) {
		backupRel = filepath.Join("mapped", contentHash([]byte(v.Path))[:32]+".strm")
	}
	backup := filepath.Join(s.s.dir, "strm-backups", p.ID, backupRel)
	expected := v.OldHash
	next := []byte(v.Content)
	if rollback {
		expected = contentHash(next)
		next, e = os.ReadFile(backup)
		if e != nil || contentHash(next) != v.OldHash {
			return errors.New("备份不存在或校验失败")
		}
	} else {
		if contentHash(raw) == contentHash(next) {
			if b, er := os.ReadFile(backup); er == nil && contentHash(b) == v.OldHash {
				return nil
			}
		}
		var owner, hash string
		if e = s.s.db.QueryRow("SELECT media,hash FROM outputs WHERE path=?", v.Path).Scan(&owner, &hash); e != nil || hash != v.OldHash {
			return errors.New("输出归属记录已改变，请重新预览")
		}
	}
	if contentHash(raw) != expected {
		return errors.New("预览后文件已改变，已保留当前文件")
	}
	if !rollback {
		if e = os.MkdirAll(filepath.Dir(backup), 0700); e != nil {
			return errors.New("备份目录创建失败")
		}
		b, er := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if er == nil {
			_, e = b.Write(raw)
			if e == nil {
				e = b.Sync()
			}
			b.Close()
			if e != nil {
				return errors.New("备份写入失败")
			}
		} else {
			old, re := os.ReadFile(backup)
			if re != nil || contentHash(old) != v.OldHash {
				return errors.New("已有备份校验失败")
			}
		}
	}
	if e = s.engine.replaceFile(v.Path, next); e != nil {
		return e
	}
	_, e = s.s.db.Exec("UPDATE outputs SET hash=? WHERE path=?", contentHash(next), v.Path)
	return e
}
func (e *Engine) replaceFile(dest string, content []byte) error {
	root, relative, _, err := e.outputLocation(dest)
	if err != nil {
		return err
	}
	temp := filepath.Join(filepath.Dir(relative), ".strmlink-update-"+randomID()+".tmp")
	f, er := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if er != nil {
		return errors.New("更新临时文件创建失败")
	}
	defer root.Remove(temp)
	_, er = f.Write(content)
	if er == nil {
		er = f.Sync()
	}
	closeErr := f.Close()
	if er != nil || closeErr != nil {
		return errors.New("更新文件写入失败")
	}
	if er = root.Rename(temp, relative); er != nil {
		return errors.New("原子更新失败")
	}
	return nil
}
func (s *Server) rollbackPlan(w http.ResponseWriter, r *http.Request) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	p, e := s.loadPlan(r.PathValue("id"))
	if e != nil || (p.Mode != "links" && p.Mode != "migration-inplace") {
		fail(w, 400, "此预览没有可恢复的覆盖备份")
		return
	}
	if p.Mode == "migration-inplace" {
		s.rollbackInplace(w, r, &p)
		return
	}
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	var active int
	if e = s.s.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE status IN ('running','queued')").Scan(&active); e != nil || active > 0 {
		fail(w, 409, "请先等待生成任务结束")
		return
	}
	for i := range p.Items {
		v := &p.Items[i]
		if !v.Applied {
			continue
		}
		if er := s.replacePlanned(p, *v, true); er != nil {
			v.Error = er.Error()
		} else {
			v.Applied = false
			v.Error = ""
		}
	}
	if e = s.savePlan(p); e != nil {
		fail(w, 500, "恢复结果保存失败")
		return
	}
	jsonOut(w, 200, planSummary(p))
}
func (s *Server) resolveLegacy(ctx context.Context, m Media) (Media, error) {
	a, e := s.s.account(m.Account)
	if e != nil || a.Disabled {
		return m, errors.New("迁移账号不可用")
	}
	parent := path.Dir(m.Path)
	folder, e := s.engine.folderAtPath(ctx, Rule{Folder: a.Root, SourcePath: "/", Account: a.ID}, parent)
	if e != nil {
		return m, e
	}
	files, e := s.clouds.List(ctx, a.ID, folder)
	if e != nil {
		return m, e
	}
	found := ""
	for _, f := range files {
		if !f.Dir && f.Name == m.Name {
			if found != "" {
				return m, errors.New("迁移路径存在同名文件")
			}
			found = f.ID
			m.Size = f.Size
			m.PickCode = f.PickCode
		}
	}
	if found == "" {
		return m, errors.New("迁移路径中未找到媒体，请检查网盘映射")
	}
	m.RemoteID = found
	e = s.s.saveMedia(m)
	return m, e
}
