package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Group adjacent requests, never infer an actual user viewing session. Full
// requests stay in activity_events; filtering and pagination operate on groups.
const groupedPlaybackCTE = `WITH resolutions AS (
 SELECT request,
 SUM(CASE WHEN COALESCE(json_extract(payload,'$.cache'),0)=0 THEN 1 ELSE 0 END) AS fresh,
 SUM(CASE WHEN json_extract(payload,'$.cache')=1 THEN 1 ELSE 0 END) AS reused,
 SUM(COALESCE(json_extract(payload,'$.millis'),0)) AS resolve_ms
 FROM activity_events WHERE kind='direct' AND request<>'' AND json_extract(payload,'$.event') IN ('get','head') GROUP BY request
), base AS (
 SELECT e.*,COALESCE(d.fresh,0) AS fresh,COALESCE(d.reused,0) AS reused,COALESCE(d.resolve_ms,0) AS resolve_ms,
 json_array(e.account,e.media,
 COALESCE(json_extract(e.payload,'$.ip'),''),COALESCE(json_extract(e.payload,'$.user_agent'),''),COALESCE(json_extract(e.payload,'$.client'),''),
 COALESCE(json_extract(e.payload,'$.event'),''),COALESCE(json_extract(e.payload,'$.mode'),''),
 e.outcome,COALESCE(json_extract(e.payload,'$.error'),''),CASE WHEN e.media='' THEN e.id ELSE 0 END) AS source_key
 FROM activity_events e LEFT JOIN resolutions d ON e.request=d.request WHERE e.kind='playback'
), gaps AS (
 SELECT *,CASE WHEN time-LAG(time) OVER (PARTITION BY source_key ORDER BY time,id)<=30000 THEN 0 ELSE 1 END AS new_group FROM base
), marked AS (
 SELECT *,SUM(new_group) OVER (PARTITION BY source_key ORDER BY time,id ROWS UNBOUNDED PRECEDING) AS group_no FROM gaps
), grouped AS (
 SELECT *,MIN(id) OVER cluster AS group_id,MIN(time) OVER cluster AS first_time,
 COUNT(*) OVER cluster AS request_count,SUM(fresh) OVER cluster AS resolve_count,SUM(reused) OVER cluster AS reuse_count,
 MAX(COALESCE(json_extract(payload,'$.millis'),0)) OVER cluster AS max_ms,SUM(resolve_ms) OVER cluster AS resolution_ms,
 ROW_NUMBER() OVER (PARTITION BY source_key,group_no ORDER BY time DESC,id DESC) AS latest
 FROM marked WINDOW cluster AS (PARTITION BY source_key,group_no)
) `

type PlaybackSummary struct {
	Activity
	GroupID          int64 `json:"group_id"`
	FirstTime        int64 `json:"first_time"`
	RequestCount     int   `json:"request_count"`
	ResolveCount     int   `json:"resolve_count"`
	ReuseCount       int   `json:"reuse_count"`
	ResolutionMillis int64 `json:"resolution_millis"`
}

func (s *Server) playbackSummaries(w http.ResponseWriter, r *http.Request) {
	where, args, page, limit, err := recordFilter(r.URL.Query())
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var total, requests int
	err = s.s.db.QueryRowContext(r.Context(), groupedPlaybackCTE+"SELECT COUNT(*),COALESCE(SUM(request_count),0) FROM grouped WHERE latest=1 AND "+where, args...).Scan(&total, &requests)
	if err != nil {
		fail(w, 500, "播放汇总读取失败")
		return
	}
	pages := (total + limit - 1) / limit
	if pages == 0 {
		page = 1
	} else if page > pages {
		page = pages
	}
	queryArgs := append(append([]any{}, args...), limit, (page-1)*limit)
	rows, err := s.s.db.QueryContext(r.Context(), groupedPlaybackCTE+"SELECT id,payload,group_id,first_time,request_count,resolve_count,reuse_count,max_ms,resolution_ms FROM grouped WHERE latest=1 AND "+where+" ORDER BY time DESC,id DESC LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		fail(w, 500, "播放汇总读取失败")
		return
	}
	items := []PlaybackSummary{}
	for rows.Next() {
		var item PlaybackSummary
		var raw string
		if err = rows.Scan(&item.ID, &raw, &item.GroupID, &item.FirstTime, &item.RequestCount, &item.ResolveCount, &item.ReuseCount, &item.Millis, &item.ResolutionMillis); err != nil {
			break
		}
		id, maxMS := item.ID, item.Millis
		if err = json.Unmarshal([]byte(raw), &item.Activity); err != nil {
			break
		}
		item.ID = id
		item.Millis = maxMS
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, 500, "播放汇总内容读取失败")
		return
	}
	s.s.activityMu.Lock()
	writeError := s.s.activityError
	s.s.activityMu.Unlock()
	jsonOut(w, 200, object{"items": items, "total": total, "request_total": requests, "page": page, "pages": pages, "limit": limit, "group_gap_seconds": 30, "write_error": writeError, "retention_days": 90, "retention_per_kind": 20000})
}

func (s *Server) playbackGroupTrace(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	media := q.Get("media")
	group, err := strconv.ParseInt(q.Get("group"), 10, 64)
	if err != nil || group < 1 || media == "" || len(media) > 128 {
		fail(w, 400, "请指定有效的播放请求组")
		return
	}
	page := 1
	if q.Get("page") != "" {
		page, err = strconv.Atoi(q.Get("page"))
		if err != nil || page < 1 || page > 1000000 {
			fail(w, 400, "详情页码无效")
			return
		}
	}
	var total int
	err = s.s.db.QueryRowContext(r.Context(), groupedPlaybackCTE+"SELECT request_count FROM grouped WHERE group_id=? AND media=? AND latest=1", group, media).Scan(&total)
	if err == sql.ErrNoRows {
		fail(w, 404, "该请求组已变化或不存在，请刷新列表")
		return
	}
	if err != nil {
		fail(w, 500, "请求详情读取失败")
		return
	}
	const limit = 20
	pages := (total + limit - 1) / limit
	if page > pages {
		page = pages
	}
	rows, err := s.s.db.QueryContext(r.Context(), groupedPlaybackCTE+"SELECT id,payload FROM grouped WHERE group_id=? AND media=? ORDER BY time DESC,id DESC LIMIT ? OFFSET ?", group, media, limit, (page-1)*limit)
	if err != nil {
		fail(w, 500, "请求详情读取失败")
		return
	}
	playback := []Activity{}
	requests := []any{}
	for rows.Next() {
		var id int64
		var raw string
		var a Activity
		if err = rows.Scan(&id, &raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &a); err != nil {
			break
		}
		a.ID = id
		playback = append(playback, a)
		if a.Request != "" {
			requests = append(requests, a.Request)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, 500, "请求详情读取失败")
		return
	}
	direct := []Activity{}
	if len(requests) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(requests)), ",")
		rows, err = s.s.db.QueryContext(r.Context(), "SELECT id,payload FROM activity_events WHERE kind='direct' AND request IN ("+placeholders+") ORDER BY time DESC,id DESC", requests...)
		if err != nil {
			fail(w, 500, "解析详情读取失败")
			return
		}
		for rows.Next() {
			var id int64
			var raw string
			var a Activity
			if err = rows.Scan(&id, &raw); err != nil {
				break
			}
			if err = json.Unmarshal([]byte(raw), &a); err != nil {
				break
			}
			a.ID = id
			direct = append(direct, a)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			fail(w, 500, "解析详情读取失败")
			return
		}
	}
	jsonOut(w, 200, object{"playback": playback, "direct": direct, "total": total, "page": page, "pages": pages, "limit": limit, "group": group})
}
