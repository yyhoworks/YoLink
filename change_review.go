package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type changeRepairItem struct {
	ParentID  string         `json:"parent_id"`
	Key       string         `json:"path"`
	Media     Media          `json:"media"`
	Hash      string         `json:"hash"`
	Identity  scanIdentity   `json:"identity"`
	Baselines []scanBaseline `json:"baselines"`
	Ready     bool           `json:"ready"`
	Note      string         `json:"note"`
}
type changeRepairPlan struct {
	ID         string             `json:"id"`
	Created    int64              `json:"created"`
	Generation string             `json:"generation"`
	Mapping    string             `json:"mapping"`
	Items      []changeRepairItem `json:"items"`
}
type changeRepairResult struct {
	State string `json:"state"`
	Note  string `json:"note"`
}

func (s *Server) changeHistory(w http.ResponseWriter, r *http.Request) {
	rule, run := r.URL.Query().Get("rule"), r.URL.Query().Get("run")
	if len(rule) > 128 || len(run) > 128 {
		fail(w, 400, "筛选标识过长")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		fail(w, 400, "页码无效")
		return
	}
	rows, err := s.s.db.QueryContext(r.Context(), `SELECT id,rule,scope,status,created,completed,json_extract(payload,'$.full') FROM scan_runs WHERE (?='' OR rule=?) ORDER BY created DESC,rowid DESC LIMIT 20`, rule, rule)
	if err != nil {
		fail(w, 500, "扫描历史读取失败")
		return
	}
	runs := []object{}
	for rows.Next() {
		var id, ruleID, scope, status string
		var created, completed int64
		var full bool
		if err = rows.Scan(&id, &ruleID, &scope, &status, &created, &completed, &full); err != nil {
			break
		}
		runs = append(runs, object{"id": id, "rule": ruleID, "scope": scope, "status": status, "created": created, "completed": completed, "full": full})
		if run == "" && status != "active" && status != "quarantined" {
			run = id
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, 500, "扫描历史无效")
		return
	}
	var selectedStatus string
	err = s.s.db.QueryRowContext(r.Context(), "SELECT status FROM scan_runs WHERE id=? AND (?='' OR rule=?)", run, rule, rule).Scan(&selectedStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, 500, "扫描状态读取失败")
		return
	}
	items := []object{}
	total := 0
	lastFull := int64(0)
	if selectedStatus != "" && selectedStatus != "active" && selectedStatus != "quarantined" {
		const changesWhere = "run=? AND (?='complete' OR kind IN ('local_missing','local_modified','outdated'))"
		if err = s.s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM scan_changes WHERE "+changesWhere, run, selectedStatus).Scan(&total); err != nil {
			fail(w, 500, "变更数量读取失败")
			return
		}
		if total > 0 && page > (total+49)/50 {
			page = (total + 49) / 50
		}
		rows, err = s.s.db.QueryContext(r.Context(), "SELECT remote,kind,before_payload,after_payload FROM scan_changes WHERE "+changesWhere+" ORDER BY kind,remote LIMIT 50 OFFSET ?", run, selectedStatus, (page-1)*50)
		if err != nil {
			fail(w, 500, "变更读取失败")
			return
		}
		for rows.Next() {
			var remote, kind, before, after string
			if err = rows.Scan(&remote, &kind, &before, &after); err != nil {
				break
			}
			var old, current scanObservation
			_ = json.Unmarshal([]byte(before), &old)
			_ = json.Unmarshal([]byte(after), &current)
			v := object{"remote": remote, "kind": kind, "before": old.Source, "after": current.Source, "name": current.Entry.Name}
			if strings.HasPrefix(kind, "local_") || kind == "outdated" {
				var local scanLocalChange
				if json.Unmarshal([]byte(after), &local) == nil {
					v["path"] = local.Path
					v["after"] = local.Source
				}
			}
			items = append(items, v)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			fail(w, 500, "变更内容读取失败")
			return
		}
	}
	if err = s.s.db.QueryRowContext(r.Context(), "SELECT COALESCE(MAX(b.full_completed),0) FROM scan_baselines b JOIN rules r ON r.id=b.rule WHERE b.scope=json_extract(r.payload,'$.source_path') AND (?='' OR b.rule=?)", rule, rule).Scan(&lastFull); err != nil {
		fail(w, 500, "核对时间读取失败")
		return
	}
	plans := []object{}
	rows, err = s.s.db.QueryContext(r.Context(), "SELECT id,state,created FROM change_plans ORDER BY created DESC,rowid DESC LIMIT 20")
	if err != nil {
		fail(w, 500, "修复预览列表读取失败")
		return
	}
	for rows.Next() {
		var id, status string
		var created int64
		if err = rows.Scan(&id, &status, &created); err != nil {
			break
		}
		plans = append(plans, object{"id": id, "state": status, "created": created})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, 500, "修复预览列表无效")
		return
	}
	jsonOut(w, 200, object{"runs": runs, "run": run, "run_status": selectedStatus, "plans": plans, "items": items, "total": total, "page": page, "pages": (total + 49) / 50, "last_full": lastFull, "capability": "完整列目录，对比已完成观测；减少重复生成，不减少网盘列目录请求"})
}

func noActiveGenerationTx(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM jobs WHERE status IN ('queued','running')").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errors.New("请等待生成任务结束后再预览或修复")
	}
	return nil
}

func (s *Server) prepareChangeRepairItemTx(tx *sql.Tx, key string) (item changeRepairItem, err error) {
	item.Key = key
	if len(key) > 32768 || !strings.HasSuffix(strings.ToLower(key), ".strm") {
		return item, errors.New("仅支持已登记的缺失 STRM")
	}
	var raw, ruleID string
	err = tx.QueryRow("SELECT m.payload,o.hash,COALESCE(d.rule,'') FROM outputs o JOIN media m ON m.id=o.media LEFT JOIN output_details d ON d.path=o.path WHERE o.path=?", key).Scan(&raw, &item.Hash, &ruleID)
	if err != nil || json.Unmarshal([]byte(raw), &item.Media) != nil {
		return item, errors.New("没有可验证的输出归属或媒体索引")
	}
	if ruleID == "" {
		return item, errors.New("此文件没有生成规则登记，请先按当前规则扫描")
	}
	var rule Rule
	if err = tx.QueryRow("SELECT payload FROM rules WHERE id=?", ruleID).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &rule) != nil {
		return item, errors.New("生成规则已移除")
	}
	if rule.Account != item.Media.Account {
		return item, errors.New("规则与媒体账号不一致")
	}
	relative, e := scanRelativePath(rule.SourcePath, item.Media.Path)
	if e != nil {
		return item, e
	}
	dest := path.Join(rule.Output, strings.TrimSuffix(relative, path.Ext(relative))+".strm")
	_, _, expectedKey, e := s.engine.outputLocation(dest)
	if e != nil || expectedKey != key {
		return item, errors.New("输出路径与当前规则映射不一致")
	}
	item.Identity, err = s.s.scanIdentityTx(tx, rule, rule.Folder, rule.SourcePath, s.engine.outputMappingIdentity())
	if err != nil {
		return item, err
	}
	item.Baselines, err = scanCoveredBaselines(tx, item.Identity)
	if err != nil {
		return item, err
	}
	covered := false
	var observed Entry
	for _, b := range item.Baselines {
		if b.Compare && scanSourceCovered(b.Scope, item.Media.Path) {
			var observation string
			var v scanObservation
			e = tx.QueryRow("SELECT payload FROM scan_entries WHERE run=? AND remote=? AND source=? AND done=1", b.Run, item.Media.RemoteID, item.Media.Path).Scan(&observation)
			covered = e == nil && json.Unmarshal([]byte(observation), &v) == nil && !v.Entry.Dir && v.Entry.Name == item.Media.Name
			observed = v.Entry
		}
	}
	if !covered {
		return item, errors.New("缺少相同配置的完整扫描基线，请先扫描此规则")
	}
	// Scanning filters the original final name under sanitized parent paths,
	// using the latest covered entry size rather than an older media index.
	filterPath := path.Join(path.Dir(relative), observed.Name)
	if rule.Filter.excluded(observed.Name, filterPath) || !rule.Filter.media(observed) {
		return item, errors.New("当前规则已排除此媒体，本次仅供核对")
	}
	if item.Hash != contentHash([]byte(mediaContent(s.s, item.Media, item.Identity.Base))) {
		return item, errors.New("登记内容与当前播放入口不同，本次仅供核对")
	}
	if _, err = s.engine.outputHash(key); !os.IsNotExist(err) {
		return item, errors.New("文件已存在、已修改或不可访问，已保留")
	}
	directory, e := s.engine.openRepairDirectory(key)
	if e != nil {
		return item, e
	}
	item.ParentID = directory.id
	directory.root.Close()
	item.Ready = true
	item.Note = "将按登记内容重建此缺失 STRM"
	return item, nil
}

func (s *Server) previewChangeRepair(ctx context.Context, paths []string) (changeRepairPlan, error) {
	p := changeRepairPlan{ID: randomID(), Created: time.Now().Unix(), Generation: s.s.startupRestore.Generation, Mapping: s.engine.outputMappingIdentity()}
	if s.s.restoreRestricted {
		return p, errors.New("恢复核对尚未完成")
	}
	if len(paths) == 0 || len(paths) > 50 {
		return p, errors.New("每次请选择 1 到 50 个已登记文件")
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	s.s.configMu.RLock()
	defer s.s.configMu.RUnlock()
	tx, err := s.s.db.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	if err = noActiveGenerationTx(tx); err != nil {
		return p, err
	}
	// A bounded, expiring set of previews prevents forgotten plans retaining an
	// unlimited number of old scan baselines. Live intents remain inspectable.
	if _, err = tx.Exec("DELETE FROM change_plans WHERE created<? AND state IN ('preview','complete','quarantined')", time.Now().Add(-24*time.Hour).Unix()); err != nil {
		return p, err
	}
	var plans int
	if err = tx.QueryRow("SELECT COUNT(*) FROM change_plans").Scan(&plans); err != nil {
		return p, err
	}
	if plans >= 100 {
		return p, errors.New("修复记录已达 100 条，请打开已有记录核验；预览在 24 小时后清理")
	}
	seen := map[string]bool{}
	remaining := (1 << 20) - 4096
	for _, key := range paths {
		if seen[key] {
			continue
		}
		seen[key] = true
		item, e := s.prepareChangeRepairItemTx(tx, key)
		if e != nil {
			item.Ready = false
			item.Note = cleanRecordText(e.Error(), 200)
		}
		itemRaw, _ := json.Marshal(item)
		remaining -= len(itemRaw) + 1
		if remaining < 0 {
			return p, errors.New("修复预览资料过大，请缩小选择或先完成根目录核对")
		}
		p.Items = append(p.Items, item)
	}
	raw, _ := json.Marshal(p)
	if len(raw) > 1<<20 {
		return p, errors.New("修复预览资料过大，请缩小选择或先完成根目录核对")
	}
	if _, err = tx.Exec("INSERT INTO change_plans VALUES(?,?,'preview',?)", p.ID, p.Created, string(raw)); err != nil {
		return p, err
	}
	for i, item := range p.Items {
		state := "preview"
		if !item.Ready {
			state = "blocked"
		}
		result, _ := json.Marshal(changeRepairResult{state, item.Note})
		if _, err = tx.Exec("INSERT INTO change_items VALUES(?,?,?,?)", p.ID, i, state, string(result)); err != nil {
			return p, err
		}
	}
	return p, tx.Commit()
}

func changePlanSummary(p changeRepairPlan, results []changeRepairResult) object {
	items := []object{}
	ready, done, conflicts := 0, 0, 0
	for i, item := range p.Items {
		state, note := "blocked", item.Note
		if item.Ready {
			state = "preview"
			ready++
		}
		if i < len(results) {
			state, note = results[i].State, results[i].Note
		}
		if state == "done" || state == "recovered" {
			done++
		}
		if state == "conflict" {
			conflicts++
		}
		items = append(items, object{"path": item.Key, "ready": item.Ready, "state": state, "note": note})
	}
	return object{"id": p.ID, "revision": restoreRevision(p), "created": p.Created, "items": items, "ready": ready, "done": done, "conflicts": conflicts, "total": len(items)}
}

func loadChangePlanTx(tx *sql.Tx, id string) (changeRepairPlan, string, []changeRepairResult, error) {
	var p changeRepairPlan
	var raw, state string
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return p, state, nil, errors.New("修复预览标识无效")
	}
	if err := tx.QueryRow("SELECT state,payload FROM change_plans WHERE id=?", id).Scan(&state, &raw); err != nil {
		return p, state, nil, errors.New("修复预览不存在")
	}
	if restoreDecode([]byte(raw), &p) != nil || p.ID != id || len(p.Items) == 0 || len(p.Items) > 50 {
		return p, state, nil, errors.New("修复预览资料无效")
	}
	rows, err := tx.Query("SELECT position,state,payload FROM change_items WHERE plan=? ORDER BY position", id)
	if err != nil {
		return p, state, nil, err
	}
	defer rows.Close()
	results := []changeRepairResult{}
	for rows.Next() {
		var n int
		var result changeRepairResult
		var itemState string
		if err = rows.Scan(&n, &itemState, &raw); err != nil {
			return p, state, nil, err
		}
		if n != len(results) || restoreDecode([]byte(raw), &result) != nil || result.State != itemState && state != "quarantined" {
			return p, state, nil, errors.New("修复进度不一致")
		}
		if state == "quarantined" {
			result = changeRepairResult{"quarantined", "恢复后已隔离，旧预览不能执行"}
		}
		results = append(results, result)
	}
	if err = rows.Err(); err != nil {
		return p, state, nil, err
	}
	if len(results) != len(p.Items) {
		return p, state, nil, errors.New("修复进度缺失")
	}
	return p, state, results, nil
}

func (s *Server) validateChangeItemTx(tx *sql.Tx, p changeRepairPlan, item changeRepairItem) error {
	if s.s.restoreRestricted || p.Generation != s.s.startupRestore.Generation || p.Mapping != s.engine.outputMappingIdentity() || time.Now().Unix()-p.Created > 86400 {
		return errors.New("修复预览已过期或恢复代次、映射已变化，请重新预览")
	}
	identity, err := s.s.scanIdentityTx(tx, item.Identity.Rule, item.Identity.Folder, item.Identity.Source, p.Mapping)
	if err != nil {
		return err
	}
	if restoreRevision(identity) != restoreRevision(item.Identity) {
		return errors.New("账号、规则或播放入口已变化，请重新预览")
	}
	baselines, err := scanCoveredBaselines(tx, identity)
	if err != nil {
		return err
	}
	if restoreRevision(baselines) != restoreRevision(item.Baselines) {
		return errors.New("扫描基线已变化，请重新预览")
	}
	owner, hash, err := outputOwnerTx(tx, item.Key)
	if err != nil {
		return err
	}
	if owner != item.Media.ID || hash != item.Hash {
		return errors.New("输出归属或登记哈希已变化")
	}
	var raw string
	var m Media
	if err = tx.QueryRow("SELECT payload FROM media WHERE id=?", item.Media.ID).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &m) != nil || restoreRevision(m) != restoreRevision(item.Media) {
		return errors.New("媒体索引已变化")
	}
	return noActiveGenerationTx(tx)
}

func saveChangeResultTx(tx *sql.Tx, id string, n int, result changeRepairResult) error {
	raw, _ := json.Marshal(result)
	_, err := tx.Exec("UPDATE change_items SET state=?,payload=? WHERE plan=? AND position=?", result.State, string(raw), id, n)
	return err
}

func (s *Server) applyOneChange(ctx context.Context, p changeRepairPlan, n int, before changeRepairResult) (changeRepairResult, error) {
	item := p.Items[n]
	conflict := func(err error) (changeRepairResult, error) {
		return changeRepairResult{"conflict", cleanRecordText(err.Error(), 200)}, nil
	}
	if !item.Ready || before.State == "done" || before.State == "recovered" || before.State == "blocked" || before.State == "conflict" {
		return before, nil
	}
	if before.State != "preview" && before.State != "intent" {
		return conflict(errors.New("修复条目状态已隔离或无效"))
	}
	tx, err := s.s.db.BeginTx(ctx, nil)
	if err != nil {
		return before, err
	}
	defer tx.Rollback()
	if err = s.validateChangeItemTx(tx, p, item); err != nil {
		return conflict(err)
	}
	directory, err := s.engine.openRepairDirectory(item.Key)
	if err != nil {
		return conflict(err)
	}
	defer directory.root.Close()
	if directory.id != item.ParentID {
		return conflict(errors.New("输出父目录已变化，请重新预览"))
	}
	actual, readErr := s.engine.outputHash(item.Key)
	recovered := before.State == "intent" && readErr == nil && actual == item.Hash
	if !recovered && !os.IsNotExist(readErr) {
		return conflict(errors.New("文件已出现、被修改或无法访问，已保留"))
	}
	if err = saveChangeResultTx(tx, p.ID, n, changeRepairResult{"intent", "已登记修复意图，尚未确认完成"}); err != nil {
		return before, err
	}
	if _, err = tx.Exec("UPDATE change_plans SET state='applying' WHERE id=?", p.ID); err != nil {
		return before, err
	}
	if err = tx.Commit(); err != nil {
		return before, err
	}
	if err = s.engine.hitScanFault("repair.after_intent"); err != nil {
		return before, err
	}
	if !recovered {
		content := []byte(mediaContent(s.s, item.Media, item.Identity.Base))
		if contentHash(content) != item.Hash {
			return conflict(errors.New("重建内容与预览不一致"))
		}
		created, e := s.engine.publishMissingRepair(item.Key, item.ParentID, directory, content)
		if e != nil {
			if created {
				return before, e
			}
			return conflict(e)
		}
		if !created {
			return conflict(errors.New("发布前文件已由其他进程创建，已保留"))
		}
	}
	if err = s.engine.hitScanFault("repair.after_file"); err != nil {
		return before, err
	}
	tx, err = s.s.db.BeginTx(ctx, nil)
	if err != nil {
		return before, err
	}
	defer tx.Rollback()
	if err = s.validateChangeItemTx(tx, p, item); err != nil {
		return conflict(err)
	}
	if actual, err = s.engine.outputHash(item.Key); err != nil || actual != item.Hash {
		return conflict(errors.New("文件在登记前已变化，已保留现场"))
	}
	if _, err = tx.Exec("UPDATE outputs SET hash=? WHERE path=? AND media=? AND hash=?", item.Hash, item.Key, item.Media.ID, item.Hash); err != nil {
		return before, err
	}
	if _, err = tx.Exec("UPDATE output_details SET checked=?,status='healthy',note='已按预览核验并恢复缺失文件' WHERE path=?", time.Now().UnixMilli(), item.Key); err != nil {
		return before, err
	}
	if err = s.engine.hitScanFault("repair.after_registration"); err != nil {
		return before, err
	}
	result := changeRepairResult{"done", "已恢复缺失 STRM，登记与内容核验一致"}
	if recovered {
		result = changeRepairResult{"recovered", "中断后重新核验归属与内容，已完成登记"}
	}
	if err = saveChangeResultTx(tx, p.ID, n, result); err != nil {
		return before, err
	}
	if err = s.engine.hitScanFault("repair.before_commit"); err != nil {
		return before, err
	}
	return result, tx.Commit()
}

func (s *Server) applyChangeRepair(ctx context.Context, id, revision string) (object, error) {
	if s.s.restoreRestricted {
		return nil, errors.New("恢复核对尚未完成")
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	s.s.configMu.RLock()
	defer s.s.configMu.RUnlock()
	tx, err := s.s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	p, state, results, err := loadChangePlanTx(tx, id)
	tx.Rollback()
	if err != nil {
		return nil, err
	}
	if revision != restoreRevision(p) || state == "quarantined" {
		return nil, errors.New("修复预览版本已变化或已隔离")
	}
	for i := range p.Items {
		if err = ctx.Err(); err != nil {
			return changePlanSummary(p, results), err
		}
		result, e := s.applyOneChange(ctx, p, i, results[i])
		if e != nil {
			return changePlanSummary(p, results), e
		}
		results[i] = result
		if result.State == "conflict" {
			tx, e = s.s.db.BeginTx(ctx, nil)
			if e != nil {
				return nil, e
			}
			if e = saveChangeResultTx(tx, id, i, result); e != nil {
				tx.Rollback()
				return nil, e
			}
			if e = tx.Commit(); e != nil {
				return nil, e
			}
		}
	}
	if _, err = s.s.db.ExecContext(ctx, "UPDATE change_plans SET state='complete' WHERE id=?", id); err != nil {
		return nil, err
	}
	return changePlanSummary(p, results), nil
}

func (s *Server) previewChangeRepairHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paths []string `json:"paths"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	p, err := s.previewChangeRepair(r.Context(), in.Paths)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	jsonOut(w, 200, changePlanSummary(p, nil))
}
func (s *Server) applyChangeRepairHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision string `json:"revision"`
		Confirm  string `json:"confirm"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if in.Confirm != "修复缺失" {
		fail(w, 400, "请确认修复此预览中的缺失文件")
		return
	}
	result, err := s.applyChangeRepair(r.Context(), r.PathValue("id"), in.Revision)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	jsonOut(w, 200, result)
}
func (s *Server) getChangeRepairHTTP(w http.ResponseWriter, r *http.Request) {
	tx, err := s.s.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 500, "修复状态读取失败")
		return
	}
	defer tx.Rollback()
	p, state, results, err := loadChangePlanTx(tx, r.PathValue("id"))
	if err != nil {
		fail(w, 404, err.Error())
		return
	}
	summary := changePlanSummary(p, results)
	summary["state"] = state
	jsonOut(w, 200, summary)
}
