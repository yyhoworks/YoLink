package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type OutputItem struct {
	Path    string `json:"path"`
	Output  string `json:"output"`
	Media   string `json:"media"`
	Name    string `json:"name"`
	Source  string `json:"source"`
	Account string `json:"account"`
	Rule    string `json:"rule"`
	Created int64  `json:"created"`
	Checked int64  `json:"checked"`
	Status  string `json:"status"`
	Note    string `json:"note"`
	URL     string `json:"url"`
}

func (s *Server) outputInventory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pageNum, _ := strconv.Atoi(q.Get("page"))
	if pageNum < 1 {
		pageNum = 1
	}
	limit := 20
	if q.Get("limit") == "50" {
		limit = 50
	}
	where := "lower(o.path) LIKE '%.strm'"
	args := []any{}
	if search := q.Get("q"); search != "" {
		if len(search) > 200 {
			fail(w, 400, "搜索内容过长")
			return
		}
		where += " AND (instr(lower(o.path),lower(?))>0 OR instr(lower(m.source_path),lower(?))>0)"
		args = append(args, search, search)
	}
	for _, f := range []struct{ key, column string }{{"account", "m.account"}, {"media", "o.media"}, {"rule", "d.rule"}, {"status", "COALESCE(d.status,'unchecked')"}} {
		if v := q.Get(f.key); v != "" {
			where += " AND " + f.column + "=?"
			args = append(args, v)
		}
	}
	from := " FROM outputs o JOIN media m ON m.id=o.media LEFT JOIN output_details d ON d.path=o.path WHERE " + where
	var total int
	if err := s.s.db.QueryRow("SELECT COUNT(*)"+from, args...).Scan(&total); err != nil {
		fail(w, 500, "生成清单读取失败")
		return
	}
	pages := (total + limit - 1) / limit
	if pages > 0 && pageNum > pages {
		pageNum = pages
	}
	args = append(args, limit, (pageNum-1)*limit)
	rows, err := s.s.db.Query("SELECT o.path,o.media,json_extract(m.payload,'$.name'),m.source_path,m.account,COALESCE(d.rule,''),COALESCE(d.created,0),COALESCE(d.checked,0),COALESCE(d.status,'unchecked'),COALESCE(d.note,'')"+from+" ORDER BY COALESCE(d.created,0) DESC,o.path LIMIT ? OFFSET ?", args...)
	if err != nil {
		fail(w, 500, "生成清单读取失败")
		return
	}
	defer rows.Close()
	items := []OutputItem{}
	for rows.Next() {
		var v OutputItem
		if err = rows.Scan(&v.Path, &v.Media, &v.Name, &v.Source, &v.Account, &v.Rule, &v.Created, &v.Checked, &v.Status, &v.Note); err != nil {
			fail(w, 500, "输出明细读取失败")
			return
		}
		v.Output = absoluteOutputPath(s.engine, v.Path)
		v.URL = "/s/" + v.Media + "/" + url.PathEscape(v.Name) + "?k=" + s.s.signature(v.Media)
		items = append(items, v)
	}
	jsonOut(w, 200, object{"items": items, "total": total, "page": pageNum, "pages": pages})
}
func (s *Server) inspectOutput(ctx context.Context, key string, remote bool) (string, string) {
	var mid, hash string
	if s.s.db.QueryRow("SELECT media,hash FROM outputs WHERE path=?", key).Scan(&mid, &hash) != nil {
		return "unknown", "不属于本服务登记的输出"
	}
	m, err := s.s.media(mid)
	if err != nil {
		return "index_error", "媒体索引不存在"
	}
	root, rel, _, err := s.engine.outputLocation(key)
	if err != nil {
		return "unreadable", err.Error()
	}
	f, err := root.Open(rel)
	if os.IsNotExist(err) {
		return "missing", "本地 STRM 文件缺失"
	}
	if err != nil {
		return "unreadable", "文件无法读取，请检查映射与权限"
	}
	raw, err := io.ReadAll(io.LimitReader(f, 8193))
	f.Close()
	if err != nil || len(raw) > 8192 {
		return "unreadable", "文件不可读或内容超过 STRM 大小限制"
	}
	if contentHash(raw) != hash {
		return "modified", "文件内容已变化，保留现状；请先核对"
	}
	expected := s.s.setting("public_base", "") + "/s/" + m.ID + "/" + url.PathEscape(m.Name) + "?k=" + s.s.signature(m.ID)
	if strings.TrimSpace(string(raw)) != expected {
		return "outdated", "与当前播放入口不一致，可前往迁移维护预览更新"
	}
	if remote {
		if m.Legacy && m.RemoteID == "" {
			return "source_unknown", "迁移索引尚未解析云端文件标识，请先验证播放入口"
		}
		rules, err := s.s.rules()
		if err != nil {
			return "source_unknown", "无法读取目录规则"
		}
		var chosen *Rule
		for _, r := range rules {
			if r.Account == m.Account && (m.Path == r.SourcePath || strings.HasPrefix(m.Path, strings.TrimSuffix(r.SourcePath, "/")+"/")) && (chosen == nil || len(r.SourcePath) > len(chosen.SourcePath)) {
				v := r
				chosen = &v
			}
		}
		if chosen == nil {
			return "source_unknown", "找不到对应来源规则，无法校验云端"
		}
		folder, err := s.engine.folderAtPath(ctx, *chosen, path.Dir(m.Path))
		if err != nil {
			return "source_unknown", cleanRecordText(err.Error(), 200)
		}
		files, err := s.clouds.List(ctx, m.Account, folder)
		if err != nil {
			return "source_unknown", cleanRecordText(err.Error(), 200)
		}
		for _, v := range files {
			if !v.Dir && v.ID == m.RemoteID {
				return "healthy", "本地内容和云端文件均已核对"
			}
		}
		return "source_missing", "成功读取源目录，未找到对应云端文件"
	}
	return "healthy", "本地文件与登记内容一致；未请求云端校验"
}
func (s *Server) checkOutputs(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paths  []string `json:"paths"`
		Remote bool     `json:"remote"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(in.Paths) == 0 || len(in.Paths) > 50 {
		fail(w, 400, "每次请选择 1 到 50 个文件")
		return
	}
	items := []object{}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	for _, p := range in.Paths {
		if ctx.Err() != nil {
			fail(w, 408, "体检超时，已完成结果已保存，请缩小范围")
			return
		}
		status, note := s.inspectOutput(ctx, p, in.Remote)
		if status == "unknown" {
			fail(w, 400, "仅能检查本服务登记的输出")
			return
		}
		_, err := s.s.db.Exec("INSERT INTO output_details(path,rule,job,created,checked,status,note) VALUES(?,'','',0,?,?,?) ON CONFLICT(path) DO UPDATE SET checked=excluded.checked,status=excluded.status,note=excluded.note", p, time.Now().UnixMilli(), status, note)
		if err != nil {
			fail(w, 500, "体检结果保存失败")
			return
		}
		items = append(items, object{"path": p, "status": status, "note": note})
	}
	jsonOut(w, 200, object{"items": items})
}
