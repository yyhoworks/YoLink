package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"time"
)

// Health is an observation of one operation, never evidence that all files or
// clients work. Separate list/resolve/quota states prevent a successful quota
// request from hiding a playback failure. No upstream text or URL is persisted.
type AccountHealth struct {
	Operation   string `json:"operation"`
	Revision    int64  `json:"revision"`
	State       string `json:"state"`
	Started     int64  `json:"started"`
	Checked     int64  `json:"checked"`
	LastSuccess int64  `json:"last_success"`
	Failures    int    `json:"failures"`
	Retryable   bool   `json:"retryable"`
	NextRetry   int64  `json:"next_retry"`
	Note        string `json:"note"`
}

func healthNote(state string) string {
	switch state {
	case "healthy":
		return "最近一次操作成功；不代表全库或实际播放器已验证"
	case "credentials":
		return "凭据不可用，请更新凭据后测试连接"
	case "rate_limit":
		return "网盘限制请求，请等待建议时间后重试"
	case "network":
		return "最近连接或上游服务异常，不等同于凭据失效"
	case "permission":
		return "访问被拒绝，请检查账号权益和通道权限"
	case "source":
		return "来源未找到，请核对目录或文件"
	case "protocol":
		return "网盘返回的数据不完整或格式异常，请稍后复核"
	case "storage":
		return "本地状态保存失败，请检查数据卷"
	case "configuration":
		return "配置发生变化，请重新验证"
	case "disabled":
		return "账号已停用"
	case "unknown":
		return "尚未验证当前配置"
	case "unsupported":
		return "当前网盘通道不提供此项能力"
	default:
		return "操作未成功，请通过诊断工具核对通道与账号状态"
	}
}

func (s *Store) observeAccount(a Account, operation string, started, checked time.Time, operationErr error) error {
	if operation != "list" && operation != "resolve" && operation != "quota" {
		return errors.New("无效的健康检测类型")
	}
	if errors.Is(operationErr, context.Canceled) {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revision int64
	var disabled bool
	if err = tx.QueryRow("SELECT COALESCE(json_extract(payload,'$.revision'),0),COALESCE(json_extract(payload,'$.disabled'),0) FROM accounts WHERE id=?", a.ID).Scan(&revision, &disabled); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if disabled || revision != a.Revision {
		return nil
	}
	old := AccountHealth{State: "unknown", Revision: revision}
	var raw string
	err = tx.QueryRow("SELECT payload FROM account_health WHERE account=? AND operation=? AND revision=?", a.ID, operation, revision).Scan(&raw)
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &old); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// A slow old request must not overwrite the result of a newer request.
	if old.Started > started.UnixNano() {
		return nil
	}
	next := old
	next.Operation, next.Revision = operation, revision
	next.Started, next.Checked = started.UnixNano(), checked.UnixMilli()
	next.State, next.Retryable, next.NextRetry = "healthy", false, 0
	if operationErr == nil {
		next.LastSuccess, next.Failures = checked.UnixMilli(), 0
	} else {
		next.State = classifyError(operationErr)
		switch next.State {
		case "credentials", "rate_limit", "network", "permission", "source", "protocol", "storage", "configuration", "unsupported":
		default:
			next.State = "other"
		}
		next.Failures = min(old.Failures+1, 1000000)
		if next.State == "unsupported" {
			next.Failures = 0
		}
		var cloud *CloudError
		if errors.As(operationErr, &cloud) {
			next.Retryable = cloud.Retryable
		}
		if next.State == "network" || next.State == "rate_limit" {
			next.Retryable = true
		}
		if next.Retryable {
			delay := min(30*time.Second*time.Duration(1<<min(next.Failures-1, 6)), 30*time.Minute)
			if cloud != nil {
				delay = max(delay, min(cloud.RetryAfter, 24*time.Hour))
			}
			next.NextRetry = checked.Add(delay).UnixMilli()
		}
	}
	next.Note = healthNote(next.State)
	payload, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO account_health VALUES(?,?,?,?) ON CONFLICT(account,operation) DO UPDATE SET revision=excluded.revision,payload=excluded.payload", a.ID, operation, revision, string(payload)); err != nil {
		return err
	}
	// One unread notification per state transition, not one per failed file.
	// Repeated failures update health counters without resetting read notices.
	if next.State != old.State && next.State != "unsupported" && !(old.State == "unknown" && next.State == "healthy") {
		title, level := "网盘操作状态变化", "warning"
		if next.State == "healthy" {
			title, level = "网盘操作已恢复", "info"
		}
		body := cleanRecordText(a.Name, 100) + " · " + operation + "：" + next.Note
		if _, err = tx.Exec("INSERT INTO notifications(id,created,title,body,level,read,dedupe) VALUES(?,?,?,?,?,0,?) ON CONFLICT(dedupe) DO UPDATE SET created=excluded.created,title=excluded.title,body=excluded.body,level=excluded.level,read=0", randomID(), checked.UnixMilli(), title, body, level, "health:"+a.ID+":"+operation); err != nil {
			return err
		}
		if _, err = tx.Exec("DELETE FROM notifications WHERE id IN (SELECT id FROM notifications ORDER BY created DESC,rowid DESC LIMIT -1 OFFSET 500)"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) accountHealth(a Account) ([]AccountHealth, error) {
	out := []AccountHealth{}
	rows, err := s.db.Query("SELECT operation,payload FROM account_health WHERE account=? AND revision=?", a.ID, a.Revision)
	if err != nil {
		return nil, err
	}
	states := map[string]AccountHealth{}
	for rows.Next() {
		var op, raw string
		if err = rows.Scan(&op, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var h AccountHealth
		if err = json.Unmarshal([]byte(raw), &h); err != nil {
			rows.Close()
			return nil, err
		}
		states[op] = h
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, op := range []string{"list", "resolve", "quota"} {
		h, ok := states[op]
		if !ok {
			h = AccountHealth{Operation: op, Revision: a.Revision, State: "unknown", Note: healthNote("unknown")}
		}
		if a.Disabled {
			h.State, h.Note, h.Retryable = "disabled", healthNote("disabled"), false
			h.NextRetry = 0
		}
		out = append(out, h)
	}
	return out, nil
}

// beginCloudObservation captures the configuration BEFORE calling the provider;
// observeAccount atomically discards stale results after edits/removal.
func (p *Providers) beginCloudObservation(ctx context.Context, id, operation string) func(error) {
	a, err := p.s.account(id)
	if expected, ok := ctx.Value(expectedAccountRevisionKey{}).(expectedAccountRevision); ok && (expected.ID != a.ID || expected.Revision != a.Revision) {
		return func(error) {}
	}
	started := time.Now()
	return func(result error) {
		if err == nil {
			if saveErr := p.s.observeAccount(a, operation, started, time.Now(), result); saveErr != nil {
				// Observability failure must not corrupt a successful media response.
				p.s.activityMu.Lock()
				p.s.activityError = "网盘健康状态保存失败，请检查数据卷"
				p.s.activityMu.Unlock()
			}
		}
	}
}

func recoveryRuleMatches(old *Rule, current Rule) bool {
	if old == nil {
		return false
	}
	a := *old
	a.LastRun, current.LastRun = 0, 0
	return reflect.DeepEqual(a, current)
}

func (s *Store) recoveryReadiness(a Account, now time.Time) error {
	if a.Disabled {
		return errors.New("账号已停用")
	}
	health, err := s.accountHealth(a)
	if err != nil {
		return errors.New("健康状态读取失败")
	}
	for _, h := range health {
		if h.Operation == "list" {
			if h.State != "healthy" || h.Checked <= 0 || now.UnixMilli()-h.Checked > int64((5*time.Minute)/time.Millisecond) || h.Checked > now.UnixMilli() {
				return errors.New("请先测试当前账号连接，五分钟内成功后再恢复任务")
			}
		}
		if h.State == "credentials" || h.State == "rate_limit" && h.NextRetry > now.UnixMilli() {
			return errors.New("账号仍有凭据或限流异常，请先处理对应操作")
		}
	}
	return nil
}

func (s *Store) accountRecoveryJobs(account string, before int64) ([]Job, int64, error) {
	rows, err := s.db.Query(`SELECT j.rowid,j.payload FROM jobs j LEFT JOIN rules r ON r.id=json_extract(j.payload,'$.rule')
WHERE j.status IN ('failed','partial','interrupted') AND COALESCE(json_extract(j.payload,'$.retried_by'),'')=''
AND COALESCE(json_extract(j.payload,'$.snapshot.account'),json_extract(r.payload,'$.account'))=?
AND (?=0 OR j.rowid<?) ORDER BY j.rowid DESC LIMIT 101`, account, before, before)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	jobs := []Job{}
	var last int64
	for rows.Next() {
		if len(jobs) == 100 {
			return jobs, last, nil
		}
		var raw string
		if err = rows.Scan(&last, &raw); err != nil {
			return nil, 0, err
		}
		var j Job
		if err = json.Unmarshal([]byte(raw), &j); err != nil {
			return nil, 0, err
		}
		jobs = append(jobs, j)
	}
	return jobs, 0, rows.Err()
}

func (s *Server) healthAccount(w http.ResponseWriter, r *http.Request) {
	var before int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		var err error
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 0 {
			fail(w, 400, "分页游标无效")
			return
		}
	}
	a, err := s.s.account(r.PathValue("id"))
	if err != nil {
		fail(w, 404, "账号不存在")
		return
	}
	health, err := s.s.accountHealth(a)
	if err != nil {
		fail(w, 500, "健康状态读取失败")
		return
	}
	jobs, next, err := s.s.accountRecoveryJobs(a.ID, before)
	if err != nil {
		fail(w, 500, "受影响任务读取失败")
		return
	}
	affected := []object{}
	for _, j := range jobs {
		reason := ""
		current, er := s.s.rule(j.Rule)
		if er != nil || !recoveryRuleMatches(j.Snapshot, current) {
			reason = "规则已改变或移除，请按当前规则重新生成"
		}
		if j.Snapshot == nil {
			reason = "旧任务缺少规则快照，不能安全恢复，请重新生成"
		}
		affected = append(affected, object{"id": j.ID, "rule": j.Rule, "source_path": j.SourcePath, "status": j.Status, "error_kind": j.ErrorKind, "eligible": reason == "", "reason": reason})
	}
	ready := s.s.recoveryReadiness(a, time.Now())
	reason := ""
	if ready != nil {
		reason = ready.Error()
	}
	jsonOut(w, 200, object{"account": a.ID, "revision": a.Revision, "operations": health, "affected": affected, "next_before": next, "has_more": next > 0, "recovery_ready": ready == nil, "recovery_reason": reason})
}

func (s *Server) recoverAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision int64    `json:"revision"`
		Jobs     []string `json:"jobs"`
		Confirm  bool     `json:"confirm"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, "恢复参数无效")
		return
	}
	if !in.Confirm || len(in.Jobs) == 0 || len(in.Jobs) > 50 {
		fail(w, 400, "请确认并选择 1–50 个待恢复任务")
		return
	}
	if p, ok := s.clouds.(*Providers); ok {
		gate := p.lock(r.PathValue("id"))
		if err := gate.acquire(r.Context(), true); err != nil {
			fail(w, 408, "恢复请求已取消")
			return
		}
		defer gate.Unlock()
	}
	a, err := s.s.account(r.PathValue("id"))
	if err != nil {
		fail(w, 404, "账号不存在")
		return
	}
	if a.Revision != in.Revision {
		fail(w, 409, "账号配置已改变，请刷新后重试")
		return
	}
	if err = s.s.recoveryReadiness(a, time.Now()); err != nil {
		fail(w, 409, err.Error())
		return
	}
	started, deferred := []Job{}, []object{}
	seen := map[string]bool{}
	for _, id := range in.Jobs {
		if seen[id] {
			continue
		}
		seen[id] = true
		old, er := s.s.job(id)
		if er != nil || old.Snapshot == nil || old.Snapshot.Account != a.ID {
			deferred = append(deferred, object{"id": id, "reason": "任务不属于此账号或缺少规则快照"})
			continue
		}
		next, er := s.engine.enqueueJob(old.Rule, old.Folder, old.SourcePath, old.DryRun, old.Limit, "health-recovery", old.ID)
		if er != nil {
			deferred = append(deferred, object{"id": id, "reason": cleanRecordText(er.Error(), 300)})
			continue
		}
		started = append(started, next)
	}
	jsonOut(w, 202, object{"started": started, "deferred": deferred})
}
