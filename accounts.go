package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type QuotaUsage struct {
	Total   int64 `json:"total"`
	Used    int64 `json:"used"`
	Checked int64 `json:"checked"`
}
type AccountReport struct {
	Revision   int64       `json:"revision"`
	Checked    int64       `json:"checked"`
	Reachable  bool        `json:"reachable"`
	Entries    int         `json:"entries"`
	Error      string      `json:"error,omitempty"`
	Quota      *QuotaUsage `json:"quota,omitempty"`
	QuotaError string      `json:"quota_error,omitempty"`
}
type quotaCloud interface {
	Quota(context.Context, string) (QuotaUsage, error)
}

func (s *Store) accountReports() (map[string]AccountReport, error) {
	rows, e := s.db.Query("SELECT r.id,r.payload FROM account_reports r JOIN accounts a ON a.id=r.id WHERE COALESCE(json_extract(r.payload,'$.revision'),0)=COALESCE(json_extract(a.payload,'$.revision'),0)")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]AccountReport{}
	for rows.Next() {
		var id, raw string
		var report AccountReport
		if e = rows.Scan(&id, &raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &report); e != nil {
			return nil, e
		}
		out[id] = report
	}
	return out, rows.Err()
}

var errAccountReportStale = errors.New("检测期间账号配置已改变，请重新检测")

func (s *Store) updateAccountReport(id string, fn func(*AccountReport), expectedRevision ...int64) (AccountReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	var report AccountReport
	tx, e := s.db.Begin()
	if e != nil {
		return report, e
	}
	defer tx.Rollback()
	var revision int64
	if e = tx.QueryRow("SELECT COALESCE(json_extract(payload,'$.revision'),0) FROM accounts WHERE id=?", id).Scan(&revision); e != nil {
		return report, errors.New("账号不存在")
	}
	if len(expectedRevision) > 0 && expectedRevision[0] != revision {
		return report, errAccountReportStale
	}
	if e := tx.QueryRow("SELECT payload FROM account_reports WHERE id=?", id).Scan(&raw); e == nil {
		if e = json.Unmarshal([]byte(raw), &report); e != nil {
			return report, e
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return report, e
	}
	if report.Revision != revision {
		report = AccountReport{}
	}
	report.Revision = revision
	fn(&report)
	b, e := json.Marshal(report)
	if e != nil {
		return report, e
	}
	if _, e = tx.Exec("INSERT INTO account_reports VALUES(?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", id, string(b)); e != nil {
		return report, e
	}
	return report, tx.Commit()
}
func (s *Server) accountForRequest(w http.ResponseWriter, r *http.Request) (Account, bool) {
	a, e := s.s.account(r.PathValue("id"))
	if e != nil {
		fail(w, 404, "网盘账号不存在")
		return a, false
	}
	if a.Disabled {
		fail(w, 409, "此网盘已停用，请先启用")
		return a, false
	}
	return a, true
}
func (s *Server) checkAccount(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountForRequest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	files, checkErr := s.clouds.List(ctx, a.ID, a.Root)
	report, e := s.s.updateAccountReport(a.ID, func(v *AccountReport) {
		v.Checked = time.Now().Unix()
		v.Reachable = checkErr == nil
		v.Entries = len(files)
		v.Error = ""
		if checkErr != nil {
			v.Error = checkErr.Error()
		}
	}, a.Revision)
	if e != nil {
		if errors.Is(e, errAccountReportStale) {
			fail(w, 409, e.Error())
			return
		}
		fail(w, 500, "连接结果保存失败")
		return
	}
	jsonOut(w, 200, report)
}
func (s *Server) accountQuota(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountForRequest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var usage QuotaUsage
	var quotaErr error
	if provider, ok := s.clouds.(quotaCloud); ok {
		usage, quotaErr = provider.Quota(ctx, a.ID)
	} else {
		quotaErr = errors.New("此通道暂未提供容量信息")
	}
	if quotaErr == nil && (usage.Total <= 0 || usage.Used < 0) {
		quotaErr = errors.New("网盘未返回有效容量信息")
	}
	report, e := s.s.updateAccountReport(a.ID, func(v *AccountReport) {
		v.QuotaError = ""
		if quotaErr != nil {
			v.QuotaError = quotaErr.Error()
		} else {
			usage.Checked = time.Now().Unix()
			v.Quota = &usage
		}
	}, a.Revision)
	if e != nil {
		if errors.Is(e, errAccountReportStale) {
			fail(w, 409, e.Error())
			return
		}
		fail(w, 500, "容量结果保存失败")
		return
	}
	jsonOut(w, 200, report)
}
func (s *Server) accountMutation(w http.ResponseWriter, r *http.Request, remove, enabled bool) {
	id := r.PathValue("id")
	if pr, ok := s.clouds.(*Providers); ok {
		mu := pr.lock(id)
		mu.Lock()
		defer mu.Unlock()
	}
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	s.s.mu.Lock()
	defer s.s.mu.Unlock()
	if _, e := s.s.account(id); e != nil {
		fail(w, 404, "网盘账号不存在")
		return
	}
	var active int
	if e := s.s.db.QueryRow(`SELECT COUNT(*) FROM jobs j JOIN rules r ON json_extract(j.payload,'$.rule')=r.id WHERE json_extract(r.payload,'$.account')=? AND j.status IN ('running','queued')`, id).Scan(&active); e != nil {
		fail(w, 500, "任务状态读取失败")
		return
	}
	if active > 0 {
		fail(w, 409, "此网盘还有生成任务，请先取消或等待完成")
		return
	}
	if remove {
		var count int
		if e := s.s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM rules WHERE json_extract(payload,'$.account')=?)+(SELECT COUNT(*) FROM media WHERE account=?)`, id, id).Scan(&count); e != nil {
			fail(w, 500, "引用状态读取失败")
			return
		}
		if count > 0 {
			fail(w, 409, "此账号仍有关联规则或播放索引，请使用停用以保留已有 STRM")
			return
		}
		tx, e := s.s.db.Begin()
		if e != nil {
			fail(w, 500, "移除账号失败")
			return
		}
		defer tx.Rollback()
		if _, e = tx.Exec("DELETE FROM account_reports WHERE id=?", id); e == nil {
			_, e = tx.Exec("DELETE FROM accounts WHERE id=?", id)
		}
		if e == nil {
			e = tx.Commit()
		}
		if e != nil {
			fail(w, 500, "移除账号失败")
			return
		}
	} else {
		if _, e := s.s.db.Exec("UPDATE accounts SET payload=json_set(payload,'$.disabled',json(?),'$.revision',COALESCE(json_extract(payload,'$.revision'),0)+1) WHERE id=?", map[bool]string{true: "false", false: "true"}[enabled], id); e != nil {
			fail(w, 500, "网盘状态保存失败")
			return
		}
	}
	jsonOut(w, 200, object{"ok": true})
}
func (s *Server) enableAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if e := decode(w, r, &body); e != nil || body.Enabled == nil {
		fail(w, 400, "请指定是否启用")
		return
	}
	s.accountMutation(w, r, false, *body.Enabled)
}
func (s *Server) removeAccount(w http.ResponseWriter, r *http.Request) {
	s.accountMutation(w, r, true, false)
}

func (p *Providers) Quota(ctx context.Context, id string) (usage QuotaUsage, err error) {
	err = p.with(ctx, id, func(a *Account) error {
		var result object
		var e error
		switch a.Provider {
		case "115":
			usage, e = p.quota115(ctx, a)
		case "139", "139old":
			result, e = p.mobile(ctx, a, "https://user-njs.yun.139.com/user/disk/quota/detail", object{})
			if e == nil {
				data := obj(result["data"])
				total, free := num(data["diskSize"]), num(data["freeDiskSize"])
				_, hasFree := data["freeDiskSize"]
				if total > 0 && hasFree && free >= 0 && free <= total && total < 1<<42 {
					usage.Total = total * 1024 * 1024
					usage.Used = (total - free) * 1024 * 1024
				}
			}
		case "quark":
			result, e = p.quark(ctx, a, "GET", "/member", nil, map[string]string{"fetch_subscribe": "false", "_ch": "home", "fetch_identity": "false"})
			if e == nil {
				data := obj(result["data"])
				usage.Total = num(data["total_capacity"])
				usage.Used = num(data["use_capacity"])
				if _, ok := data["use_capacity"]; !ok {
					usage.Total = 0
				}
			}
		case "ali":
			result, e = p.ali(ctx, a, "/adrive/v1.0/user/getSpaceInfo", object{})
			if e == nil {
				data := obj(result["personal_space_info"])
				usage.Total = num(data["total_size"])
				usage.Used = num(data["used_size"])
				if _, ok := data["used_size"]; !ok {
					usage.Total = 0
				}
			}
		default:
			return errors.New("此通道暂未提供容量查询，仍可浏览和生成 STRM")
		}
		if e != nil {
			return e
		}
		if usage.Total <= 0 || usage.Used < 0 {
			return errors.New("当前授权未返回容量信息，仍可测试连接和浏览文件")
		}
		return nil
	})
	return
}
