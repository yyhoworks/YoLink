package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

type FileFilter struct {
	Preset       string `json:"preset"`
	Extensions   string `json:"extensions"`
	ExcludeDirs  string `json:"exclude_dirs"`
	ExcludeWords string `json:"exclude_words"`
	Pattern      string `json:"pattern"`
	MinBytes     int64  `json:"min_bytes"`
}

func filterWords(v string) []string {
	return strings.FieldsFunc(strings.ToLower(v), func(r rune) bool { return r == ',' || r == '，' || r == '\n' || r == ';' })
}
func (f FileFilter) validate() error {
	if !strings.Contains("|all|video|audio|custom|", "|"+f.Preset+"|") && f.Preset != "" {
		return errors.New("文件过滤预设无效")
	}
	if f.MinBytes < 0 || len(f.Pattern) > 500 || len(f.Extensions)+len(f.ExcludeDirs)+len(f.ExcludeWords) > 4000 {
		return errors.New("文件过滤条件过长或大小无效")
	}
	if f.Preset == "custom" && len(filterWords(f.Extensions)) == 0 {
		return errors.New("自定义预设请填写媒体扩展名")
	}
	if f.Pattern != "" {
		if _, err := regexp.Compile(f.Pattern); err != nil {
			return errors.New("排除正则表达式无效")
		}
	}
	return nil
}
func (f FileFilter) excluded(name, relative string) bool {
	for _, v := range filterWords(f.ExcludeDirs) {
		v = strings.Trim(strings.TrimSpace(v), "/")
		if v != "" {
			for _, part := range strings.Split(strings.ToLower(relative), "/") {
				if part == v {
					return true
				}
			}
		}
	}
	for _, v := range filterWords(f.ExcludeWords) {
		if v = strings.TrimSpace(v); v != "" && strings.Contains(strings.ToLower(name), v) {
			return true
		}
	}
	if f.Pattern != "" {
		re, err := compiledFilterPattern(f.Pattern)
		if err == nil && re.MatchString(relative) {
			return true
		}
	}
	return false
}

type filterPatternResult struct {
	re  *regexp.Regexp
	err error
}

// filterPatterns caches compiled exclusion regexes by pattern so scanning
// does not recompile the same pattern for every file. A compile failure is
// logged once when the pattern is first compiled (validate() already rejects
// bad patterns at save time; this only guards stale stored data).
var filterPatterns sync.Map // map[string]filterPatternResult

func compiledFilterPattern(pattern string) (*regexp.Regexp, error) {
	if v, ok := filterPatterns.Load(pattern); ok {
		r := v.(filterPatternResult)
		return r.re, r.err
	}
	re, err := regexp.Compile(pattern)
	filterPatterns.Store(pattern, filterPatternResult{re, err})
	if err != nil {
		log.Printf("文件过滤排除正则编译失败，该规则暂不生效: %v", err)
	}
	return re, err
}
func (f FileFilter) media(e Entry) bool {
	ext := strings.ToLower(path.Ext(e.Name))
	if f.MinBytes > 0 && e.Size < f.MinBytes {
		return false
	}
	switch f.Preset {
	case "video":
		return strings.Contains("|mkv|mp4|m4v|avi|mov|ts|m2ts|mts|wmv|flv|mpg|mpeg|webm|iso|rmvb|", "|"+strings.TrimPrefix(ext, ".")+"|")
	case "audio":
		return strings.Contains("|flac|m4a|mp3|wav|ape|aac|ogg|opus|wma|dsf|dff|", "|"+strings.TrimPrefix(ext, ".")+"|")
	case "custom":
		for _, v := range filterWords(f.Extensions) {
			if "."+strings.TrimPrefix(strings.TrimSpace(v), ".") == ext {
				return true
			}
		}
		return false
	default:
		return mediaExtensions[ext]
	}
}
func errorKind(message string) string {
	s := strings.ToLower(message)
	for _, item := range []struct{ kind, words string }{{"credentials", "凭据|登录|授权|cookie|token|401"}, {"rate_limit", "限流|风控|429|too many"}, {"permission", "权限|permission|只读|拒绝"}, {"space", "磁盘|空间|no space"}, {"conflict", "冲突|不同|已存在"}, {"network", "超时|连接|网络|timeout"}, {"source", "目录|不存在|未找到"}} {
		for _, w := range strings.Split(item.words, "|") {
			if strings.Contains(s, w) {
				return item.kind
			}
		}
	}
	return "other"
}

const featureSchema = `CREATE TABLE IF NOT EXISTS metric_buckets(bucket INTEGER,kind TEXT,event TEXT,outcome TEXT,cache INTEGER,n INTEGER,millis INTEGER,PRIMARY KEY(bucket,kind,event,outcome,cache));
 CREATE TABLE IF NOT EXISTS job_checkpoints(job TEXT PRIMARY KEY,payload TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS job_failures(job TEXT,folder TEXT,source TEXT,message TEXT,PRIMARY KEY(job,folder,source));
 CREATE TABLE IF NOT EXISTS output_details(path TEXT PRIMARY KEY,rule TEXT,job TEXT,created INTEGER,checked INTEGER DEFAULT 0,status TEXT DEFAULT 'unchecked',note TEXT DEFAULT '');
 CREATE TABLE IF NOT EXISTS notifications(id TEXT PRIMARY KEY,created INTEGER,title TEXT,body TEXT,level TEXT,read INTEGER DEFAULT 0,dedupe TEXT UNIQUE);
 CREATE TABLE IF NOT EXISTS deliveries(id TEXT PRIMARY KEY,job TEXT,kind TEXT,status TEXT,attempts INTEGER DEFAULT 0,next_at INTEGER,payload TEXT,error TEXT DEFAULT '',UNIQUE(job,kind));`

func recordMetric(tx *sql.Tx, v Activity) error {
	if !(v.Kind == "generation" && v.Event == "strm" && v.Outcome == "created") && v.Kind != "playback" && v.Kind != "direct" {
		return nil
	}
	_, err := tx.Exec("INSERT INTO metric_buckets VALUES(?,?,?,?,?,1,?) ON CONFLICT(bucket,kind,event,outcome,cache) DO UPDATE SET n=n+1,millis=millis+excluded.millis", v.Time/60000, v.Kind, v.Event, v.Outcome, v.Cache, v.Millis)
	return err
}
func (s *Store) notify(title, body, level, dedupe string) {
	_, _ = s.db.Exec("INSERT INTO notifications(id,created,title,body,level,dedupe) VALUES(?,?,?,?,?,?) ON CONFLICT(dedupe) DO UPDATE SET created=excluded.created,title=excluded.title,body=excluded.body,level=excluded.level,read=0", randomID(), time.Now().UnixMilli(), title, cleanRecordText(body, 500), level, dedupe)
	_, _ = s.db.Exec("DELETE FROM notifications WHERE id IN (SELECT id FROM notifications ORDER BY created DESC LIMIT -1 OFFSET 500)")
}
func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		_, err := s.s.db.Exec("UPDATE notifications SET read=1")
		if err != nil {
			fail(w, 500, "通知状态保存失败")
			return
		}
		jsonOut(w, 200, object{"ok": true})
		return
	}
	rows, err := s.s.db.Query("SELECT id,created,title,body,level,read FROM notifications ORDER BY created DESC LIMIT 50")
	if err != nil {
		fail(w, 500, "通知读取失败")
		return
	}
	defer rows.Close()
	items := []object{}
	for rows.Next() {
		var id, title, body, level string
		var created int64
		var read bool
		if rows.Scan(&id, &created, &title, &body, &level, &read) == nil {
			items = append(items, object{"id": id, "created": created, "title": title, "body": body, "level": level, "read": read})
		}
	}
	jsonOut(w, 200, object{"items": items})
}
func (s *Server) filterPreview(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Filter FileFilter `json:"filter"`
		Names  []string   `json:"names"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := in.Filter.validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	items := []object{}
	for i, n := range in.Names {
		if i >= 30 {
			break
		}
		items = append(items, object{"name": n, "included": !in.Filter.excluded(path.Base(n), n) && in.Filter.media(Entry{Name: n, Size: 1 << 62})})
	}
	jsonOut(w, 200, object{"items": items})
}
func (s *Server) preflight(w http.ResponseWriter, r *http.Request) {
	rule, err := s.s.rule(r.PathValue("id"))
	if err != nil {
		fail(w, 404, "规则不存在")
		return
	}
	checks := []object{}
	add := func(name string, err error) {
		note := "检查通过"
		if err != nil {
			note = err.Error()
		}
		checks = append(checks, object{"name": name, "ok": err == nil, "note": note})
	}
	_, err = s.engine.normalizeOutputDirectory(rule.Output)
	add("输出目录映射", err)
	if s.s.setting("public_base", "") == "" {
		err = errors.New("请在服务设置填写播放器可访问的地址")
	} else {
		err = nil
	}
	add("播放入口配置", err)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	_, err = s.clouds.List(ctx, rule.Account, rule.Folder)
	add("源目录读取", err)
	add("文件过滤", rule.Filter.validate())
	jsonOut(w, 200, object{"checks": checks, "note": "映射检查不写入文件；实际写入权限会在生成时检查。", "output": generationOutputName(s.engine, rule.Output)})
}
func scanJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }
func (s *Store) consoleJobs() ([]Job, error) {
	recent, err := s.jobs(100)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query("SELECT payload FROM jobs WHERE status IN ('queued','running') ORDER BY created,rowid")
	if err != nil {
		return nil, err
	}
	out := []Job{}
	active := map[string]bool{}
	for rows.Next() {
		var raw string
		var j Job
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &j) == nil {
			out = append(out, j)
			active[j.ID] = true
		}
	}
	rows.Close()
	for _, j := range recent {
		if !active[j.ID] {
			out = append(out, j)
		}
	}
	return out, nil
}
func (s *Store) latestRules() map[string]Job {
	out := map[string]Job{}
	rows, err := s.db.Query("SELECT payload FROM jobs WHERE rowid IN (SELECT MAX(rowid) FROM jobs WHERE COALESCE(json_extract(payload,'$.dry_run'),0)=0 GROUP BY json_extract(payload,'$.rule'))")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var j Job
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &j) == nil {
			out[j.Rule] = j
		}
	}
	return out
}
