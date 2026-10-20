package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"time"
)

type backupRequest struct {
	AdminPassword  string `json:"admin_password"`
	BackupPassword string `json:"backup_password"`
	ID             string `json:"id,omitempty"`
	Revision       string `json:"revision,omitempty"`
	Generation     string `json:"generation,omitempty"`
	Confirm        string `json:"confirm,omitempty"`
}

func (s *Server) backupReauthenticate(w http.ResponseWriter, r *http.Request, password string) bool {
	if !s.allowAuthAttempt(r) {
		fail(w, 429, "尝试次数过多，请一分钟后重试")
		return false
	}
	s.authMu.RLock()
	ok := s.passwordMatches(password)
	s.authMu.RUnlock()
	if !ok {
		fail(w, 403, "当前管理员密码不正确")
		return false
	}
	return true
}

func (s *Server) startBackupOperation(w http.ResponseWriter) bool {
	if !s.backupMu.TryLock() {
		fail(w, 409, "已有备份或恢复操作正在处理，请稍后重试")
		return false
	}
	return true
}

func (s *Server) exportBackup(w http.ResponseWriter, r *http.Request) {
	var in backupRequest
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(in.BackupPassword) < 12 || len(in.BackupPassword) > 1024 {
		fail(w, 400, "备份口令需为 12–1024 字节")
		return
	}
	if !s.backupReauthenticate(w, r, in.AdminPassword) || !s.startBackupOperation(w) {
		return
	}
	defer s.backupMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	snapshot, err := s.backupSnapshot(ctx, s.s.dir, in.AdminPassword)
	if snapshot != "" {
		defer func() {
			if e := os.RemoveAll(snapshot); e != nil {
				s.s.notify("备份临时资料待清理", "备份快照清理失败，请检查数据目录的 .backup-snapshot-* 私有目录。", "error", "backup-cleanup")
			}
		}()
	}
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	archive, err := os.CreateTemp(s.s.dir, ".backup-encrypted-*.age")
	if err != nil {
		fail(w, 500, "无法创建加密备份，请检查剩余磁盘空间")
		return
	}
	defer func() {
		archive.Close()
		if e := os.Remove(archive.Name()); e != nil && !os.IsNotExist(e) {
			s.s.notify("加密备份临时文件待清理", "请检查数据目录的 .backup-encrypted-* 文件。", "error", "backup-cleanup")
		}
	}()
	if _, err = writeBackupArchive(ctx, archive, snapshot, in.BackupPassword, currentSchemaVersion); err == nil {
		err = archive.Sync()
	}
	if err != nil {
		fail(w, 500, "加密备份生成失败，请检查磁盘空间后重试")
		return
	}
	info, err := archive.Stat()
	if err != nil {
		fail(w, 500, "加密备份无法读取")
		return
	}
	if _, err = archive.Seek(0, io.SeekStart); err != nil {
		fail(w, 500, "加密备份无法读取")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="yolink-backup-%s.age"`, time.Now().UTC().Format("20060102T150405Z")))
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, backupContextReader{ctx, archive})
}

func (s *Server) importBackup(w http.ResponseWriter, r *http.Request) {
	if !restorePlatformSupported() {
		fail(w, 400, "恢复激活需在 Linux 部署中进行，当前平台可导出备份")
		return
	}
	if !s.startBackupOperation(w) {
		return
	}
	defer s.backupMu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, backupMaxTotalBytes+backupMaxManifest+int64(backupMaxFiles)*4096+1<<20)
	defer r.Body.Close()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(10 * time.Minute))
	defer controller.SetReadDeadline(time.Time{})
	multipart, err := r.MultipartReader()
	if err != nil {
		fail(w, 400, "请选择加密备份文件")
		return
	}
	options, err := multipart.NextPart()
	if err != nil || options.FormName() != "options" {
		fail(w, 400, "恢复请求缺少验证信息")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(options, 8193))
	options.Close()
	var in backupRequest
	if err != nil || len(raw) > 8192 || restoreDecode(raw, &in) != nil {
		fail(w, 400, "恢复验证信息无效")
		return
	}
	if !s.backupReauthenticate(w, r, in.AdminPassword) {
		return
	}
	file, err := multipart.NextPart()
	if err != nil || file.FormName() != "archive" {
		fail(w, 400, "恢复请求缺少备份文件")
		return
	}
	defer file.Close()
	m, err := openRestoreManager(s.s.dir)
	if err != nil {
		fail(w, 500, "恢复控制目录无法打开")
		return
	}
	defer m.Close()
	if err = m.uploadSlotAvailable(); err != nil {
		fail(w, 409, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	candidate, err := m.prepareCandidate(ctx, file, in.BackupPassword)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if _, err = multipart.NextPart(); err != io.EOF {
		// Session is fresh and unarmed; same cleanup ownership as preparation.
		_ = m.root.RemoveAll(path.Join(restoreControlName, candidate.ID))
		fail(w, 400, "恢复请求包含多余内容，请重新上传")
		return
	}
	if err = m.storeUpload(candidate); err != nil {
		fail(w, 500, "恢复候选状态未保存，暂未启用")
		return
	}
	var payload int64
	for _, f := range candidate.Inventory.Files {
		payload += f.Size
	}
	jsonOut(w, 200, object{"id": candidate.ID, "revision": candidate.Revision, "created": candidate.Created, "validation": candidate.Validation, "payload_bytes": payload, "restart_required": true, "automatic_work_paused": true})
}

func (s *Server) restoreStatus(w http.ResponseWriter, r *http.Request) {
	m, err := openRestoreManager(s.s.dir)
	if err != nil {
		fail(w, 500, "恢复状态无法读取")
		return
	}
	defer m.Close()
	p, err := m.loadJournal()
	if err != nil {
		fail(w, 500, "恢复日志损坏，请保留资料并使用管理员回退流程")
		return
	}
	status := object{"supported": restorePlatformSupported(), "restricted": s.s.restoreRestricted, "runtime": s.s.startupRestore, "pending": nil, "limits": object{"files": backupMaxFiles, "file_bytes": backupMaxFileBytes, "payload_bytes": backupMaxTotalBytes, "passphrase_min_bytes": 12}}
	if p != nil {
		status["pending"] = object{"id": p.Runtime.ID, "state": p.Runtime.State, "generation": p.Runtime.Generation, "created": p.Created}
	}
	upload, e := m.uploadedCandidate()
	if e != nil {
		fail(w, 500, "恢复候选状态无法读取")
		return
	}
	status["upload"] = upload
	jsonOut(w, 200, status)
}

func (s *Server) restoreAction(w http.ResponseWriter, r *http.Request) {
	var in backupRequest
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !s.backupReauthenticate(w, r, in.AdminPassword) || !s.startBackupOperation(w) {
		return
	}
	defer s.backupMu.Unlock()
	m, err := openRestoreManager(s.s.dir)
	if err != nil {
		fail(w, 500, "恢复控制目录无法打开")
		return
	}
	defer m.Close()
	switch r.PathValue("action") {
	case "confirm":
		if in.Confirm != "RESTORE" {
			fail(w, 400, "请输入 RESTORE 确认替换应用数据")
			return
		}
		upload, e := m.uploadedCandidate()
		if e != nil || upload == nil || upload.ID != in.ID || upload.Revision != in.Revision {
			fail(w, 409, "待确认的恢复候选已变化，请刷新状态")
			return
		}
		err = m.armPreparedCandidate(r.Context(), in.ID, in.Revision)
		if err == nil {
			err = m.forgetUpload(in.ID)
		}
	case "cancel":
		err = m.cancelRestore(r.Context(), in.ID)
	case "rollback":
		if in.Confirm != "ROLLBACK" {
			fail(w, 400, "请输入 ROLLBACK 确认回退应用数据")
			return
		}
		err = m.requestRollback(in.ID)
	case "review":
		if in.Confirm != "REVIEWED" {
			fail(w, 400, "请输入 REVIEWED 确认已核对恢复资料")
			return
		}
		err = s.finishRestoreReview(r.Context(), in.ID, in.Generation, in.AdminPassword)
	case "restart":
		if in.Confirm != "RESTART" {
			fail(w, 400, "请输入 RESTART 确认重启")
			return
		}
		if s.restart == nil {
			err = errors.New("当前运行方式不支持管理页重启，请手动重启容器")
		}
	default:
		fail(w, 404, "恢复操作不存在")
		return
	}
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	jsonOut(w, 200, object{"ok": true, "restart_required": r.PathValue("action") != "cancel"})
	if r.PathValue("action") == "restart" {
		s.restart()
	}
}

// Administrative shell escape hatch: runs before SQLite/auth/engine starts.
// It does not need a usable new database or administrator login.
func runRestoreRollbackCLI(ctx context.Context, m *restoreManager) error {
	p, err := m.loadJournal()
	if err != nil {
		return err
	}
	if p == nil {
		return errors.New("没有可回退的恢复会话")
	}
	if p.Runtime.State == "pending" || p.Runtime.State == "prepared" {
		return m.cancelRestore(ctx, p.Runtime.ID)
	}
	if p.Runtime.State == "rolled_back" || p.Runtime.State == "released_old" {
		return nil
	}
	if p.Runtime.State == "installed" || p.Runtime.State == "released_new" {
		if err = m.requestRollback(p.Runtime.ID); err != nil {
			return err
		}
	}
	_, err = m.recoverStartup(ctx)
	return err
}
