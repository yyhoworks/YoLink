package main

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
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
	if _, err := m.readEnvelopeLimit(name, &upload, temporaryClaimMax); err != nil {
		return nil, err
	}
	if !restoreIDValid(upload.ID) || len(upload.Revision) != 64 || strings.Trim(upload.Revision, "0123456789abcdef") != "" {
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
	if err = m.cleanupUnarmed(); err != nil {
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
	_, upload, protected, err := m.candidateProtection()
	if err != nil {
		return err
	}
	if upload == nil || upload.ID != id {
		return errors.New("上传候选不存在")
	}
	// The whole prior chain wins over an inconsistent/stale upload pointer.
	if protected[id] {
		return errors.New("恢复候选已启用，请取消等待中的恢复操作")
	}
	if err = m.ensureCandidateOwnership(id, nil); err != nil {
		return err
	}
	if err = m.forgetUpload(id); err != nil {
		return err
	}
	return m.cleanupCandidate(id)
}

func (m *restoreManager) storeUpload(candidate restoreCandidate) error {
	if !restoreIDValid(candidate.ID) {
		return errors.New("上传候选身份无效")
	}
	return m.writeEnvelope(path.Join(restoreControlName, "upload.json"), restoreUpload{candidate.ID, candidate.Revision}, "upload")
}

func (m *restoreManager) cancelRestore(ctx context.Context, id string) error {
	if !restoreIDValid(id) {
		return errors.New("恢复候选身份无效")
	}
	p, upload, history, err := m.candidateProtection()
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
		return m.cleanupCandidate(id)
	}
	if history[id] {
		return errors.New("恢复会话已使用，不能清理")
	}
	if upload == nil {
		return m.cleanupCandidate(id)
	}
	return m.discardUploaded(id)
}

func (m *restoreManager) candidateProtection() (*restoreJournal, *restoreUpload, map[string]bool, error) {
	p, err := m.loadJournal()
	if err != nil {
		return nil, nil, nil, err
	}
	protected, err := m.protectedHistory(p)
	if err != nil {
		return nil, nil, nil, err
	}
	upload, err := m.uploadedCandidate()
	return p, upload, protected, err
}

// Only a validated internal marker (legacy) or the exact pending journal may
// authorize migrating an old candidate to external ownership. Fresh candidates
// use temporaryFiles.create and can never adopt a pre-existing directory.
func (m *restoreManager) ensureCandidateOwnership(id string, pending *restoreJournal) error {
	o := m.temporaryFiles()
	_, claim, err := temporaryNames("candidate", id)
	if err != nil {
		return err
	}
	if _, err = m.root.Lstat(claim); err == nil {
		if err = o.readClaim("candidate", id); err != nil {
			return err
		}
		return o.syncDir(restoreControlName)
	} else if !os.IsNotExist(err) {
		return err
	}
	if pending == nil || pending.Runtime.ID != id || (pending.Runtime.State != "pending" && pending.Runtime.State != "prepared") {
		var marker restoreUpload
		if _, err = m.readEnvelopeLimit(path.Join(restoreControlName, id, "unarmed.json"), &marker, temporaryClaimMax); err != nil {
			return err
		}
		if marker.ID != id || marker.Revision != "" {
			return errors.New("未启用候选身份不一致")
		}
	}
	return o.publish("candidate", id)
}

func (m *restoreManager) cleanupCandidate(id string) error {
	_, upload, protected, err := m.candidateProtection()
	if err != nil {
		return err
	}
	if protected[id] || upload != nil && upload.ID == id {
		return errors.New("恢复候选仍受保护，已保留")
	}
	if err = m.ensureCandidateOwnership(id, nil); err != nil {
		return err
	}
	return m.temporaryFiles().cleanup("candidate", id)
}

func (m *restoreManager) cleanupUnarmed() error {
	_, upload, protected, err := m.candidateProtection()
	if err != nil {
		return err
	}
	if upload != nil {
		protected[upload.ID] = true
	}
	if _, err = m.root.Lstat(restoreControlName); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	o := m.temporaryFiles()
	entries, err := o.entries(restoreControlName, temporaryControlLimit)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, entry := range entries {
		id := entry.Name()
		if strings.HasPrefix(id, "unarmed-") && strings.HasSuffix(id, ".json") {
			id = strings.TrimSuffix(strings.TrimPrefix(id, "unarmed-"), ".json")
			if !restoreIDValid(id) {
				return errors.New("未启用候选所有权名称无效")
			}
			if err = o.readClaim("candidate", id); err != nil {
				return err
			}
			ids[id] = true
			continue
		}
		if !restoreIDValid(id) || protected[id] {
			continue
		}
		marker := path.Join(restoreControlName, id, "unarmed.json")
		if _, err = m.root.Lstat(marker); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err = m.ensureCandidateOwnership(id, nil); err != nil {
			return err
		}
		ids[id] = true
	}
	for id := range ids {
		if protected[id] {
			continue
		}
		if err = m.cleanupCandidate(id); err != nil {
			return err
		}
	}
	return nil
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
