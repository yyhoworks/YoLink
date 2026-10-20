package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type DiagnosticStep struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Kind   string `json:"kind,omitempty"`
	Note   string `json:"note"`
	Action string `json:"action,omitempty"`
	Millis int64  `json:"millis"`
}

type DiagnosticReport struct {
	Version  int              `json:"version"`
	ID       string           `json:"id"`
	Created  int64            `json:"created"`
	Media    string           `json:"media"`
	Account  string           `json:"account,omitempty"`
	Revision int64            `json:"revision"`
	Steps    []DiagnosticStep `json:"steps"`
	Readable bool             `json:"readable"`
	Range    bool             `json:"range"`
	Bytes    int64            `json:"bytes"`
	HeadHTTP int              `json:"head_http,omitempty"`
	HTTP     int              `json:"http,omitempty"`
	Mode     string           `json:"mode,omitempty"`
	Stale    bool             `json:"stale"`
	Boundary string           `json:"boundary"`
}

func diagnosticAction(kind string) string {
	switch kind {
	case "credentials":
		return "更新账号凭据并测试连接"
	case "rate_limit":
		return "在账号健康详情查看建议重试时间"
	case "permission":
		return "检查网盘权益及播放通道限制"
	case "source":
		return "核对云端文件及目录规则"
	case "network":
		return "检查 NAS 网络后重新诊断"
	case "protocol":
		return "稍后重试；持续发生时查看网盘适配状态"
	default:
		return "查看账号健康与运行记录"
	}
}

func (s *Server) runDiagnostic(ctx context.Context, mid, output string, probe bool) (report DiagnosticReport) {
	report = DiagnosticReport{Version: 1, ID: randomID(), Created: time.Now().UnixMilli(),
		Boundary: "这是服务器端有限检查，不证明播放器解码、音轨、跨网连接、观看进度或拖动成功。报告不含凭据、完整路径或签名网址。",
		Steps:    []DiagnosticStep{{ID: "index", Label: "媒体索引"}, {ID: "output", Label: "本地 STRM"}, {ID: "account", Label: "网盘账号"}, {ID: "resolve", Label: "原文件地址解析"}, {ID: "probe", Label: "有限取流检查"}}}
	for i := range report.Steps {
		report.Steps[i].Status = "skipped"
		report.Steps[i].Note = "前置检查未完成"
	}
	mark := func(index int, started time.Time, status, kind, note, action string) {
		step := &report.Steps[index]
		step.Status, step.Kind, step.Note, step.Action = status, kind, note, action
		step.Millis = time.Since(started).Milliseconds()
	}
	cancelled := func(index int, started time.Time) bool {
		if ctx.Err() == nil {
			return false
		}
		kind, note := "cancelled", "诊断已取消，未继续请求网盘"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind, note = "network", "诊断已到达时间上限，请稍后重试"
		}
		mark(index, started, "cancelled", kind, note, "重新诊断")
		return true
	}
	start := time.Now()
	if cancelled(0, start) {
		return report
	}
	m, err := s.s.media(mid)
	if err != nil {
		mark(0, start, "failed", "index", "媒体索引不存在或无法读取", "在生成文件页核对或重新生成")
		return report
	}
	report.Media = m.ID
	report.Account = m.Account
	mark(0, start, "passed", "", "已读取登记索引", "")
	start = time.Now()
	if cancelled(1, start) {
		return report
	}
	if output == "" {
		err = s.s.db.QueryRow("SELECT path FROM outputs WHERE media=? AND lower(path) LIKE '%.strm' ORDER BY path LIMIT 1", mid).Scan(&output)
	} else {
		var owner string
		err = s.s.db.QueryRow("SELECT media FROM outputs WHERE path=? AND lower(path) LIKE '%.strm'", output).Scan(&owner)
		if err == nil && owner != mid {
			err = errors.New("wrong owner")
		}
	}
	if errors.Is(err, sql.ErrNoRows) && output == "" {
		mark(1, start, "warning", "unregistered", "尚无登记 STRM；仅继续检查索引对应的网盘文件", "先生成 STRM")
	} else if err != nil {
		mark(1, start, "failed", "ownership", "无法确认所选 STRM 属于此媒体", "在生成文件页重新选择文件")
		return report
	} else {
		status, _ := s.inspectOutput(ctx, output, false)
		note, action := "本地内容与登记一致", ""
		state := "passed"
		switch status {
		case "healthy":
		case "missing":
			state, note, action = "failed", "本地 STRM 缺失", "在生成文件页预览缺失文件修复"
		case "modified":
			state, note, action = "failed", "本地内容已变化；没有覆盖文件", "核对用户修改及输出归属"
		case "outdated":
			state, note, action = "warning", "登记内容与当前播放入口不一致", "前往迁移维护预览入口更新"
		default:
			state, note, action = "failed", "本地文件无法安全读取或索引异常", "检查输出挂载权限与登记记录"
		}
		mark(1, start, state, status, note, action)
		// A local failure should not conceal an independently testable cloud
		// problem. Later results never turn the failed STRM step green.
	}
	start = time.Now()
	if cancelled(2, start) {
		return report
	}
	a, err := s.s.account(m.Account)
	if err != nil || a.Disabled {
		mark(2, start, "failed", "account", "账号不可用或已停用", "在网盘管理中检查账号")
		return report
	}
	report.Revision = a.Revision
	ctx = context.WithValue(ctx, expectedAccountRevisionKey{}, expectedAccountRevision{ID: a.ID, Revision: a.Revision})
	mark(2, start, "passed", "", "账号已启用；远端凭据有效性由下一步验证", "")
	defer func() {
		latest, err := s.s.account(a.ID)
		if err != nil || latest.Revision != a.Revision || latest.Disabled {
			report.Stale = true
			report.Boundary += " 检查期间账号配置发生变化，请重新诊断。"
		}
	}()
	start = time.Now()
	if cancelled(3, start) {
		return report
	}
	if m.Legacy && m.RemoteID == "" {
		m, err = s.lookupLegacy(ctx, m)
		if err != nil {
			if !cancelled(3, start) {
				kind := classifyError(err)
				mark(3, start, "failed", kind, healthNote(kind), diagnosticAction(kind))
			}
			return report
		}
	}
	// Diagnostics use their own cancellable request, not a detached playback
	// cache flight that may remain alive after the user cancels the wizard.
	target, err := s.clouds.Resolve(ctx, m)
	if cancelled(3, start) {
		return report
	}
	if err != nil {
		kind := classifyError(err)
		mark(3, start, "failed", kind, healthNote(kind), diagnosticAction(kind))
		return report
	}
	switch target.Mode {
	case "302":
		report.Mode = "302"
	case "proxy":
		report.Mode = "proxy"
	case "proxy-required":
		report.Mode = "proxy-required"
		mark(3, start, "failed", "proxy_required", "当前通道需要中转但未获得允许", "确认流量影响后在账号设置开启中转")
		return report
	default:
		mark(3, start, "failed", "protocol", "网盘未返回有效播放通道", "检查网盘适配状态")
		return report
	}
	mark(3, start, "passed", "", "已取得临时地址；地址不会写入报告", "")
	start = time.Now()
	if !probe {
		mark(4, start, "skipped", "", "本次未请求媒体数据", "需要时启用有限取流检查")
	} else if !cancelled(4, start) {
		result := s.probeTarget(ctx, target)
		if !cancelled(4, start) {
			report.Readable, report.Range, report.Bytes, report.HeadHTTP, report.HTTP = result.Readable, result.Range, result.Bytes, result.HeadStatus, result.Status
			if !result.Readable {
				mark(4, start, "failed", "fetch", "NAS 未取得可读取的媒体数据", "检查上游状态、网盘权益和当前网络")
			} else if !result.Range {
				mark(4, start, "warning", "range", "已读取最多 1024 字节，但未确认有效 Range 响应", "用实际播放器验证拖动")
			} else {
				mark(4, start, "passed", "", "已读取最多 1024 字节并核对 Range 响应", "用实际播放器验证解码、音轨和拖动")
			}
		}
	}
	return report
}

func (s *Server) diagnoseMedia(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Output string `json:"output"`
		Probe  bool   `json:"probe"`
	}
	if err := decode(w, r, &in); err != nil || len(in.Output) > 4096 {
		fail(w, 400, "诊断参数无效；请选择已登记媒体")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	report := s.runDiagnostic(ctx, r.PathValue("id"), in.Output, in.Probe)
	raw, err := json.Marshal(report)
	if err != nil {
		fail(w, 500, "诊断报告生成失败")
		return
	}
	tx, err := s.s.db.Begin()
	if err != nil {
		fail(w, 500, "诊断报告保存失败")
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO diagnostic_reports VALUES(?,?,?)", report.ID, report.Created, string(raw)); err == nil {
		_, err = tx.Exec("DELETE FROM diagnostic_reports WHERE created<? OR id IN (SELECT id FROM diagnostic_reports ORDER BY created DESC,rowid DESC LIMIT -1 OFFSET 200)", time.Now().Add(-7*24*time.Hour).UnixMilli())
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 500, "诊断报告保存失败")
		return
	}
	jsonOut(w, 200, report)
}

func (s *Server) exportDiagnostic(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if len(id) != 32 {
		fail(w, 404, "报告不存在或已过期")
		return
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			fail(w, 404, "报告不存在或已过期")
			return
		}
	}
	var raw string
	if err := s.s.db.QueryRow("SELECT payload FROM diagnostic_reports WHERE id=? AND created>=?", id, time.Now().Add(-7*24*time.Hour).UnixMilli()).Scan(&raw); err != nil {
		fail(w, 404, "报告不存在或已过期")
		return
	}
	var report DiagnosticReport
	if json.Unmarshal([]byte(raw), &report) != nil {
		fail(w, 500, "报告无法读取")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="yolink-diagnostic-`+id+`.json"`)
	jsonOut(w, 200, report)
}
