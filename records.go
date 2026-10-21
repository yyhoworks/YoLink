package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Each row describes an observed step, never an inferred player success.
type Activity struct {
	ID          int64  `json:"id"`
	Time        int64  `json:"time"`
	Kind        string `json:"kind"`
	Event       string `json:"event"`
	Outcome     string `json:"outcome"`
	Media       string `json:"media,omitempty"`
	Account     string `json:"account,omitempty"`
	AccountName string `json:"account_name,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Rule        string `json:"rule,omitempty"`
	RuleName    string `json:"rule_name,omitempty"`
	Job         string `json:"job,omitempty"`
	Trigger     string `json:"trigger,omitempty"`
	Request     string `json:"request,omitempty"`
	Name        string `json:"name"`
	Path        string `json:"path,omitempty"`
	Output      string `json:"output,omitempty"`
	IP          string `json:"ip,omitempty"`
	UserAgent   string `json:"user_agent,omitempty"`
	Client      string `json:"client,omitempty"`
	Method      string `json:"method,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Host        string `json:"host,omitempty"`
	Millis      int64  `json:"millis"`
	HTTP        int    `json:"http,omitempty"`
	Cache       bool   `json:"cache"`
	Bytes       int64  `json:"bytes,omitempty"`
	Error       string `json:"error,omitempty"`
	Note        string `json:"note,omitempty"`
}

type requestInfo struct{ ID, IP, UA, Client, Method, Event string }
type requestInfoKey struct{}

func activityRequest(r *http.Request) *http.Request {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	ua := cleanRecordText(r.UserAgent(), 256)
	client := "未知客户端"
	for _, label := range []string{"PotPlayer", "SenPlayer", "Filmy", "Emby", "Jellyfin", "Kodi", "Lavf", "FFmpeg", "VLC", "Python", "Mozilla"} {
		if strings.Contains(strings.ToLower(ua), strings.ToLower(label)) {
			client = label
			break
		}
	}
	event := strings.ToLower(r.Method)
	if strings.HasPrefix(r.URL.Path, "/api/resolve/") {
		event = "resolve"
	}
	if strings.HasPrefix(r.URL.Path, "/api/playback/") {
		event = "check"
	}
	info := requestInfo{ID: randomID(), IP: cleanRecordText(ip, 64), UA: ua, Client: client, Method: r.Method, Event: event}
	ctx := context.WithValue(r.Context(), pan115UserAgentKey{}, r.UserAgent())
	return r.WithContext(context.WithValue(ctx, requestInfoKey{}, info))
}

var recordURL = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)

func cleanRecordText(text string, limit int) string {
	text = recordURL.ReplaceAllString(text, "[地址已隐藏]")
	text = strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, text)
	runes := []rune(text)
	if len(runes) > limit {
		text = string(runes[:limit])
	}
	return text
}

const activitySchema = `CREATE TABLE IF NOT EXISTS activity_events(id INTEGER PRIMARY KEY AUTOINCREMENT,kind TEXT NOT NULL,time INTEGER NOT NULL,account TEXT NOT NULL,media TEXT NOT NULL,outcome TEXT NOT NULL,request TEXT NOT NULL,payload TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS activity_kind_time ON activity_events(kind,time DESC,id DESC);
CREATE INDEX IF NOT EXISTS activity_media ON activity_events(media,time DESC,id DESC);
CREATE INDEX IF NOT EXISTS activity_request ON activity_events(request);`

func (s *Store) initActivities() error {
	if s.setting("activity_backfill_v1", "") == "done" {
		return nil
	}
	rows, err := s.db.Query(`SELECT o.path,m.payload,a.payload FROM outputs o JOIN media m ON m.id=o.media JOIN accounts a ON a.id=m.account WHERE lower(o.path) LIKE '%.strm' ORDER BY o.rowid DESC LIMIT 20000`)
	if err != nil {
		return err
	}
	items := []Activity{}
	for rows.Next() {
		var output, raw, accountRaw string
		var m Media
		var a Account
		if err = rows.Scan(&output, &raw, &accountRaw); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal([]byte(raw), &m) != nil || json.Unmarshal([]byte(accountRaw), &a) != nil {
			rows.Close()
			return errors.New("已有媒体索引无法读取")
		}
		items = append(items, Activity{Kind: "generation", Event: "strm", Outcome: "existing", Media: m.ID, Account: m.Account, AccountName: a.Name, Provider: a.Provider, Name: m.Name, Path: m.Path, Output: strings.ReplaceAll(output, `\`, "/"), Note: "升级前已有的生成索引，原生成时间未记录"})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, v := range items {
		raw, _ := json.Marshal(v)
		if _, err = tx.Exec("INSERT INTO activity_events(kind,time,account,media,outcome,request,payload) VALUES(?,?,?,?,?,?,?)", v.Kind, 0, v.Account, v.Media, v.Outcome, "", string(raw)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("INSERT INTO settings VALUES('activity_backfill_v1','done') ON CONFLICT(id) DO UPDATE SET value='done'"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) appendActivity(v Activity) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	s.appendActivityContext(ctx, v)
}

// Keep the online logging budget separate from storage-policy tests. A slow
// instrumented database must not make playback wait for unbounded log pruning.
func (s *Store) appendActivityContext(ctx context.Context, v Activity) {
	if v.Time == 0 {
		v.Time = time.Now().UnixMilli()
	}
	v.Error = cleanRecordText(v.Error, 300)
	v.Note = cleanRecordText(v.Note, 500)
	raw, err := json.Marshal(v)
	if err == nil {
		var tx *sql.Tx
		tx, err = s.db.BeginTx(ctx, nil)
		if err == nil {
			defer tx.Rollback()
		}
		var result interface{ LastInsertId() (int64, error) }
		if err == nil {
			result, err = tx.ExecContext(ctx, "INSERT INTO activity_events(kind,time,account,media,outcome,request,payload) VALUES(?,?,?,?,?,?,?)", v.Kind, v.Time, v.Account, v.Media, v.Outcome, v.Request, string(raw))
		}
		if err == nil {
			err = recordMetric(tx, v)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err == nil {
			if id, _ := result.LastInsertId(); id%128 == 0 {
				_, _ = s.db.ExecContext(ctx, "DELETE FROM metric_buckets WHERE bucket<?", time.Now().AddDate(0, 0, -32).UnixMilli()/60000)
				_, err = s.db.ExecContext(ctx, `DELETE FROM activity_events WHERE kind=? AND (time>0 AND time<? OR id IN (SELECT id FROM activity_events WHERE kind=? ORDER BY id DESC LIMIT -1 OFFSET 20000))`, v.Kind, time.Now().AddDate(0, 0, -90).UnixMilli(), v.Kind)
			}
		}
	}
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if err != nil {
		s.activityError = "记录写入失败，请检查数据目录权限或剩余空间"
	} else {
		s.activityError = ""
	}
}

func (s *Server) mediaActivity(ctx context.Context, m Media) Activity {
	v := Activity{Media: m.ID, Name: m.Name, Path: m.Path, Account: m.Account}
	if a, err := s.s.account(m.Account); err == nil {
		v.AccountName = a.Name
		v.Provider = a.Provider
	}
	if info, ok := ctx.Value(requestInfoKey{}).(requestInfo); ok {
		v.Request = info.ID
		v.IP = info.IP
		v.UserAgent = info.UA
		v.Client = info.Client
		v.Method = info.Method
		v.Event = info.Event
	}
	return v
}

func (e *Engine) generationActivity(a Account, r Rule, j Job, f Entry, source, output, outcome, event string, err error) {
	v := Activity{Kind: "generation", Event: event, Outcome: outcome, Media: e.s.mediaID(a, f.ID), Name: f.Name, Path: source, Output: strings.ReplaceAll(output, `\`, "/"), Account: a.ID, AccountName: a.Name, Provider: a.Provider, Rule: r.ID, RuleName: r.Name, Job: j.ID}
	if err != nil {
		v.Error = err.Error()
	}
	v.Trigger = j.Trigger
	if j.DryRun {
		v.Note = "范围预览，未写入文件"
	} else if j.Limit > 0 {
		v.Note = "试生成"
	} else if j.Folder == r.Folder {
		v.Note = "规则全量范围"
	} else {
		v.Note = "指定子目录范围"
	}
	e.s.appendActivity(v)
}

func recordFilter(q url.Values) (string, []any, int, int, error) {
	kind := q.Get("kind")
	if kind != "generation" && kind != "playback" && kind != "direct" {
		return "", nil, 0, 0, errors.New("请选择生成、播放或诊断记录")
	}
	page, limit := 1, 20
	if q.Get("page") != "" {
		var err error
		page, err = strconv.Atoi(q.Get("page"))
		if err != nil || page < 1 || page > 1000000 {
			return "", nil, 0, 0, errors.New("页码无效")
		}
	}
	if q.Get("limit") != "" {
		var err error
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			return "", nil, 0, 0, errors.New("每页数量应为1到100")
		}
	}
	where := "kind=?"
	args := []any{kind}
	for _, field := range []string{"account", "media", "request", "outcome"} {
		if val := q.Get(field); val != "" {
			if len(val) > 128 {
				return "", nil, 0, 0, errors.New("筛选值过长")
			}
			where += " AND " + field + "=?"
			args = append(args, val)
		}
	}
	if event := q.Get("event"); event != "" {
		if len(event) > 32 {
			return "", nil, 0, 0, errors.New("记录类型无效")
		}
		where += " AND json_extract(payload,'$.event')=?"
		args = append(args, event)
	}
	for _, field := range []string{"rule", "job"} {
		if v := q.Get(field); v != "" {
			if len(v) > 128 {
				return "", nil, 0, 0, errors.New("筛选值过长")
			}
			where += " AND json_extract(payload,'$." + field + "')=?"
			args = append(args, v)
		}
	}
	if value := q.Get("date"); value != "" {
		// Day boundaries follow the service's local timezone; hardcoding a
		// zone here made the filter disagree with the host clock on any
		// deployment outside that zone.
		day, err := time.ParseInLocation("2006-01-02", value, time.Local)
		if err != nil {
			return "", nil, 0, 0, errors.New("日期格式无效")
		}
		where += " AND time>=? AND time<?"
		args = append(args, day.UnixMilli(), day.AddDate(0, 0, 1).UnixMilli())
	}
	if text := q.Get("q"); text != "" {
		if len([]rune(text)) > 200 {
			return "", nil, 0, 0, errors.New("搜索词过长")
		}
		text = strings.NewReplacer("~", "~~", "%", "~%", "_", "~_").Replace(text)
		where += ` AND (json_extract(payload,'$.name') LIKE ? ESCAPE '~' OR json_extract(payload,'$.path') LIKE ? ESCAPE '~' OR json_extract(payload,'$.output') LIKE ? ESCAPE '~' OR json_extract(payload,'$.ip') LIKE ? ESCAPE '~' OR json_extract(payload,'$.client') LIKE ? ESCAPE '~' OR json_extract(payload,'$.account_name') LIKE ? ESCAPE '~')`
		for i := 0; i < 6; i++ {
			args = append(args, "%"+text+"%")
		}
	}
	if days := q.Get("days"); days != "" && days != "0" {
		n, err := strconv.Atoi(days)
		if err != nil || n < 1 || n > 90 {
			return "", nil, 0, 0, errors.New("时间范围应为1到90天")
		}
		where += " AND time>=?"
		args = append(args, time.Now().AddDate(0, 0, -n).UnixMilli())
	}
	if kind == "direct" && q.Get("scope") == "diagnostic" {
		// Keep the final result of each manual resolve/check request. Its
		// intermediate attempts remain available through request correlation.
		where += ` AND id IN (SELECT MAX(id) FROM activity_events WHERE kind='direct' AND json_extract(payload,'$.event') IN ('resolve','check') GROUP BY CASE WHEN request='' THEN CAST(id AS TEXT) ELSE request END)`
	}
	return where, args, page, limit, nil
}

func (s *Server) records(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("kind") == "playback" && r.URL.Query().Get("summary") == "1" {
		s.playbackSummaries(w, r)
		return
	}
	where, args, page, limit, err := recordFilter(r.URL.Query())
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var total int
	if err = s.s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM activity_events WHERE "+where, args...).Scan(&total); err != nil {
		fail(w, 500, "记录统计读取失败")
		return
	}
	pages := (total + limit - 1) / limit
	if pages == 0 {
		page = 1
	}
	if pages > 0 && page > pages {
		page = pages
	}
	queryArgs := append(append([]any{}, args...), limit, (page-1)*limit)
	rows, err := s.s.db.QueryContext(r.Context(), "SELECT id,payload FROM activity_events WHERE "+where+" ORDER BY time DESC,id DESC LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		fail(w, 500, "记录读取失败")
		return
	}
	items := []Activity{}
	for rows.Next() {
		var id int64
		var raw string
		var v Activity
		if rows.Scan(&id, &raw) != nil || json.Unmarshal([]byte(raw), &v) != nil {
			rows.Close()
			fail(w, 500, "记录内容读取失败")
			return
		}
		v.ID = id
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		fail(w, 500, "记录读取失败")
		return
	}
	s.s.activityMu.Lock()
	writeError := s.s.activityError
	s.s.activityMu.Unlock()
	jsonOut(w, 200, object{"items": items, "total": total, "page": page, "pages": pages, "limit": limit, "write_error": writeError, "retention_days": 90, "retention_per_kind": 20000})
}

func (s *Server) recordTrace(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("group") != "" {
		s.playbackGroupTrace(w, r)
		return
	}
	media := r.URL.Query().Get("media")
	if media == "" || len(media) > 128 {
		fail(w, 400, "请指定媒体记录")
		return
	}
	request := r.URL.Query().Get("request")
	if len(request) > 128 {
		fail(w, 400, "请求标识无效")
		return
	}
	result := object{}
	for _, kind := range []string{"generation", "playback", "direct"} {
		query := "SELECT id,payload FROM activity_events WHERE media=? AND kind=?"
		args := []any{media, kind}
		if request != "" && kind != "generation" {
			query += " AND request=?"
			args = append(args, request)
		}
		rows, err := s.s.db.QueryContext(r.Context(), query+" ORDER BY time DESC,id DESC LIMIT 10", args...)
		if err != nil {
			fail(w, 500, "链路记录读取失败")
			return
		}
		items := []Activity{}
		for rows.Next() {
			var raw string
			var id int64
			var v Activity
			if rows.Scan(&id, &raw) != nil || json.Unmarshal([]byte(raw), &v) != nil {
				rows.Close()
				fail(w, 500, "链路记录内容读取失败")
				return
			}
			v.ID = id
			items = append(items, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			fail(w, 500, "链路记录读取失败")
			return
		}
		result[kind] = items
	}
	jsonOut(w, 200, result)
}

func targetHost(target PlaybackTarget) string {
	u, err := url.Parse(target.URL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
func generationOutputName(e *Engine, output string) string {
	_, _, key, err := e.outputLocation(output)
	if err != nil {
		return path.Clean(output)
	}
	return strings.ReplaceAll(key, `\`, "/")
}
