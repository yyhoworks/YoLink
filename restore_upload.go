package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
)

type restoreUpload struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

func (m *restoreManager) uploadedCandidate() (*restoreUpload, error) {
	name := path.Join(restoreControlName, "upload.json")
	if _, err := m.root.Lstat(name); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var upload restoreUpload
	if _, err := m.readEnvelope(name, &upload); err != nil {
		return nil, err
	}
	if !restoreIDValid(upload.ID) || len(upload.Revision) != 64 {
		return nil, errors.New("上传候选状态无效")
	}
	return &upload, nil
}

func (m *restoreManager) forgetUpload(id string) error {
	upload, err := m.uploadedCandidate()
	if err != nil {
		return err
	}
	if upload == nil {
		return nil
	}
	if upload.ID != id {
		return errors.New("上传候选已变化")
	}
	if err = m.root.Remove(path.Join(restoreControlName, "upload.json")); err != nil {
		return err
	}
	return m.syncDir(restoreControlName)
}

func (m *restoreManager) uploadSlotAvailable() error {
	upload, err := m.uploadedCandidate()
	if err != nil {
		return err
	}
	p, err := m.loadJournal()
	if err != nil {
		return err
	}
	if err = m.cleanupUnarmed(p, upload); err != nil {
		return err
	}
	if p != nil && !restoreTerminal(p.Runtime.State) {
		return errors.New("已有恢复操作等待重启或回退完成")
	}
	if upload == nil {
		return nil
	}
	if p != nil && p.Runtime.ID == upload.ID {
		return m.forgetUpload(upload.ID)
	}
	return errors.New("已有待确认的恢复候选，请先确认或移除")
}

func (m *restoreManager) discardUploaded(id string) error {
	upload, err := m.uploadedCandidate()
	if err != nil {
		return err
	}
	if upload == nil || upload.ID != id {
		return errors.New("上传候选不存在")
	}
	p, err := m.loadJournal()
	if err != nil {
		return err
	}
	if p != nil && p.Runtime.ID == id {
		return errors.New("恢复候选已启用，请取消等待中的恢复操作")
	}
	// Remove only the uploaded, locally generated session, never historical
	// baseline/control root. Failed removal keeps the pointer for retry/reporting.
	if err = m.root.RemoveAll(path.Join(restoreControlName, id)); err != nil {
		return errors.New("上传候选清理失败，请保留资料后重试")
	}
	return m.forgetUpload(id)
}

func (m *restoreManager) storeUpload(candidate restoreCandidate) error {
	if !restoreIDValid(candidate.ID) {
		return errors.New("上传候选身份无效")
	}
	return m.writeEnvelope(path.Join(restoreControlName, "upload.json"), restoreUpload{candidate.ID, candidate.Revision}, "upload")
}

func (m *restoreManager) cancelRestore(ctx context.Context, id string) error {
	p, err := m.loadJournal()
	if err != nil {
		return err
	}
	if p != nil && p.Runtime.ID == id {
		if err = m.cancelPending(id); err != nil {
			return err
		}
		if err = m.step("cancel.state_published"); err != nil {
			return err
		}
		if err = m.root.RemoveAll(path.Join(restoreControlName, id)); err != nil {
			return errors.New("已取消的恢复候选清理失败")
		}
		if err = m.forgetUpload(id); err != nil {
			return err
		}
		return m.syncDir(restoreControlName)
	}
	upload, e := m.uploadedCandidate()
	if e != nil {
		return e
	}
	if upload == nil {
		protected, e := m.protectedHistory(p)
		if e != nil {
			return e
		}
		if protected[id] || !restoreIDValid(id) {
			return errors.New("恢复会话已使用，不能清理")
		}
		var marker restoreUpload
		if _, e = m.readEnvelope(path.Join(restoreControlName, id, "unarmed.json"), &marker); e != nil || marker.ID != id {
			return errors.New("可取消的恢复候选不存在")
		}
		if e = m.root.RemoveAll(path.Join(restoreControlName, id)); e != nil {
			return e
		}
		return m.syncDir(restoreControlName)
	}
	return m.discardUploaded(id)
}

func (m *restoreManager) cleanupUnarmed(p *restoreJournal, upload *restoreUpload) error {
	protected, err := m.protectedHistory(p)
	if err != nil {
		return err
	}
	if upload != nil {
		protected[upload.ID] = true
	}
	entries, err := fs.ReadDir(m.root.FS(), restoreControlName)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 1000 {
		return errors.New("恢复控制资料过多，请检查磁盘空间")
	}
	for _, entry := range entries {
		id := entry.Name()
		if !restoreIDValid(id) || protected[id] {
			continue
		}
		marker := path.Join(restoreControlName, id, "unarmed.json")
		if _, err = m.root.Lstat(marker); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		var unarmed restoreUpload
		if _, err = m.readEnvelope(marker, &unarmed); err != nil {
			return err
		}
		if unarmed.ID != id {
			return errors.New("未启用候选身份不一致")
		}
		if err = m.root.RemoveAll(path.Join(restoreControlName, id)); err != nil {
			return errors.New("中断的恢复上传清理失败")
		}
	}
	if _, err = m.root.Lstat(restoreControlName); os.IsNotExist(err) {
		return nil
	}
	return m.syncDir(restoreControlName)
}

func (m *restoreManager) protectedHistory(p *restoreJournal) (map[string]bool, error) {
	protected := map[string]bool{}
	for depth := 0; p != nil; depth++ {
		if depth > 64 {
			return nil, errors.New("恢复保留会话过多，请先整理历史恢复资料")
		}
		if protected[p.Runtime.ID] {
			return nil, errors.New("恢复历史引用循环")
		}
		protected[p.Runtime.ID] = true
		if p.Previous == nil {
			break
		}
		var prior restoreJournal
		checksum, err := m.readEnvelope(path.Join(restoreControlName, p.Runtime.ID, "prior.json"), &prior)
		if err != nil {
			return nil, err
		}
		if checksum != p.PreviousChecksum || prior.Runtime != *p.Previous {
			return nil, errors.New("恢复历史引用无效")
		}
		if err = validateRestoreJournal(prior); err != nil {
			return nil, err
		}
		p = &prior
	}
	return protected, nil
}
