package main

import (
	"encoding/json"
	"net/http"
	"time"
)

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	o, err := s.s.generationOverview()
	if err != nil {
		fail(w, 500, "运行概况读取失败")
		return
	}
	now := time.Now()
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	local := now.In(zone)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone)
	days := 7
	if r.URL.Query().Get("days") == "30" {
		days = 30
	}
	start := today.AddDate(0, 0, -days+1)
	coverage, _ := time.Parse(time.RFC3339, s.s.setting("metrics_since", ""))
	trend := []object{}
	dayMap := map[string]object{}
	for i := 0; i < days; i++ {
		label := start.AddDate(0, 0, i).Format("2006-01-02")
		p := object{"day": label, "created": int64(0), "requests": int64(0), "covered": !start.AddDate(0, 0, i+1).Before(coverage)}
		trend = append(trend, p)
		dayMap[label] = p
	}
	stats := object{"today_created": int64(0), "requests": int64(0), "resolved": int64(0), "resolve_total": int64(0), "resolve_ms": int64(0), "cache": int64(0), "upstream_ms": int64(0), "upstream_count": int64(0)}
	rows, err := s.s.db.Query("SELECT bucket,kind,event,outcome,cache,n,millis FROM metric_buckets WHERE bucket>=?", min(start.Unix()/60, now.Add(-24*time.Hour).Unix()/60))
	if err != nil {
		fail(w, 500, "运行统计读取失败")
		return
	}
	for rows.Next() {
		var bucket, n, millis int64
		var kind, event, outcome string
		var cache bool
		if rows.Scan(&bucket, &kind, &event, &outcome, &cache, &n, &millis) != nil {
			continue
		}
		p := dayMap[time.Unix(bucket*60, 0).In(zone).Format("2006-01-02")]
		if kind == "generation" && event == "strm" && outcome == "created" {
			if p != nil {
				p["created"] = p["created"].(int64) + n
			}
			if bucket >= today.Unix()/60 {
				stats["today_created"] = stats["today_created"].(int64) + n
			}
		}
		if kind == "playback" && event == "play" {
			if p != nil {
				p["requests"] = p["requests"].(int64) + n
			}
			if bucket >= now.Add(-24*time.Hour).Unix()/60 {
				stats["requests"] = stats["requests"].(int64) + n
			}
		}
		if kind == "direct" && event == "get" && bucket >= now.Add(-24*time.Hour).Unix()/60 {
			stats["resolve_total"] = stats["resolve_total"].(int64) + n
			stats["resolve_ms"] = stats["resolve_ms"].(int64) + millis
			if outcome == "resolved" || outcome == "proxy_required" {
				stats["resolved"] = stats["resolved"].(int64) + n
			}
			if cache {
				stats["cache"] = stats["cache"].(int64) + n
			} else {
				stats["upstream_ms"] = stats["upstream_ms"].(int64) + millis
				stats["upstream_count"] = stats["upstream_count"].(int64) + n
			}
		}
	}
	rows.Close()
	accounts, err := s.s.accounts()
	if err != nil {
		fail(w, 500, "网盘读取失败")
		return
	}
	reports, err := s.s.accountReports()
	if err != nil {
		fail(w, 500, "网盘状态读取失败")
		return
	}
	rules, err := s.s.rules()
	if err != nil {
		fail(w, 500, "执行计划读取失败")
		return
	}
	jobs, err := s.s.consoleJobs()
	if err != nil {
		fail(w, 500, "任务读取失败")
		return
	}
	plans := []object{}
	for _, rule := range rules {
		next := int64(0)
		if rule.Enabled {
			next = rule.LastRun + int64(rule.Interval)
		}
		plans = append(plans, object{"id": rule.ID, "name": rule.Name, "account": rule.Account, "enabled": rule.Enabled, "next": next})
	}
	recent := []object{}
	rows, err = s.s.db.Query("SELECT o.media,json_extract(m.payload,'$.name'),d.path,d.created,d.rule FROM output_details d JOIN outputs o ON o.path=d.path JOIN media m ON m.id=o.media WHERE d.created>0 ORDER BY d.created DESC LIMIT 10")
	if err == nil {
		for rows.Next() {
			var mid, name, path, rule string
			var created int64
			if rows.Scan(&mid, &name, &path, &created, &rule) == nil {
				recent = append(recent, object{"media": mid, "name": name, "path": absoluteOutputPath(s.engine, path), "created": created, "rule": rule})
			}
		}
		rows.Close()
	}
	errors := []Activity{}
	rows, err = s.s.db.Query("SELECT payload FROM activity_events WHERE kind='direct' AND outcome='failed' ORDER BY time DESC LIMIT 5")
	if err == nil {
		for rows.Next() {
			var raw string
			var v Activity
			if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &v) == nil {
				errors = append(errors, v)
			}
		}
		rows.Close()
	}
	s.s.activityMu.Lock()
	writeError := s.s.activityError
	s.s.activityMu.Unlock()
	jsonOut(w, 200, object{"time": now.UnixMilli(), "metrics_since": s.s.setting("metrics_since", ""), "overview": o, "stats": stats, "trend": trend, "accounts": accounts, "reports": reports, "plans": plans, "jobs": jobs, "recent": recent, "errors": errors, "write_error": writeError})
}
