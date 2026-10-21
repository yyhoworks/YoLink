package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"time"
)

const restoreJournalMax = backupMaxManifest*2 + 1<<20

type restoreRuntime struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	Generation string `json:"generation"`
}

func (r restoreRuntime) held() bool {
	return r.ID != "" && r.State != "released_new" && r.State != "released_old"
}
func restoreTerminal(state string) bool {
	return state == "installed" || state == "rolled_back" || state == "released_new" || state == "released_old"
}

type restoreJournal struct {
	Format           int              `json:"format"`
	Schema           int              `json:"schema"`
	Runtime          restoreRuntime   `json:"runtime"`
	Created          int64            `json:"created"`
	New              restoreInventory `json:"new"`
	Old              restoreInventory `json:"old"`
	Post             restoreInventory `json:"post"`
	Previous         *restoreRuntime  `json:"previous,omitempty"`
	PreviousChecksum string           `json:"previous_checksum,omitempty"`
}

type restoreEnvelope struct {
	Data     json.RawMessage `json:"data"`
	Checksum string          `json:"checksum"`
}

func restoreRevision(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func restoreDecode(raw []byte, target any) error {
	if err := validateBackupJSON(raw); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("恢复日志格式无效")
	}
	return nil
}

func (m *restoreManager) readEnvelope(name string, target any) (string, error) {
	return m.readEnvelopeLimit(name, target, restoreJournalMax)
}

func (m *restoreManager) readEnvelopeLimit(name string, target any, limit int64) (string, error) {
	if err := restoreCheckPath(m.root, name, false); err != nil {
		return "", err
	}
	f, err := m.root.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > limit {
		return "", errors.New("恢复日志过大或无法读取")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return "", errors.New("恢复日志读取中断")
	}
	var envelope restoreEnvelope
	if err = restoreDecode(raw, &envelope); err != nil {
		return "", err
	}
	sum := sha256.Sum256(envelope.Data)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return "", errors.New("恢复日志摘要不匹配")
	}
	return envelope.Checksum, restoreDecode(envelope.Data, target)
}

func (m *restoreManager) writeEnvelope(name string, value any, label string) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	raw, err := json.Marshal(restoreEnvelope{data, hex.EncodeToString(sum[:])})
	if err != nil || int64(len(raw)) > restoreJournalMax {
		return errors.New("恢复日志超限")
	}
	if err = m.mkdir(path.Dir(name)); err != nil {
		return err
	}
	temp := path.Join(path.Dir(name), ".journal-"+randomID())
	if err = m.step("journal." + label + ".before_write"); err != nil {
		return err
	}
	f, err := m.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = m.root.Remove(temp) }()
	_, err = f.Write(raw)
	if err == nil {
		err = m.step("journal." + label + ".before_sync")
	}
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	if err = m.step("journal." + label + ".before_rename"); err != nil {
		return err
	}
	if _, e := m.root.Lstat(name); e == nil {
		if e = restoreCheckPath(m.root, name, false); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	if err = m.root.Rename(temp, name); err != nil {
		return err
	}
	if err = m.step("journal." + label + ".after_rename"); err != nil {
		return err
	}
	if err = m.syncAncestors(path.Dir(name)); err != nil {
		return err
	}
	return m.step("journal." + label + ".durable")
}

func validateRestoreJournal(p restoreJournal) error {
	if p.Format != 1 || p.Schema < 1 || p.Schema > currentSchemaVersion || !restoreIDValid(p.Runtime.ID) || !restoreIDValid(p.Runtime.Generation) || p.Created <= 0 {
		return errors.New("恢复日志身份或版本无效")
	}
	switch p.Runtime.State {
	case "pending", "prepared", "installing", "installed", "rollback_pending", "rollback_prepared", "rolling_back", "rolled_back", "released_new", "released_old":
	default:
		return errors.New("恢复日志状态不受支持")
	}
	if err := validateRestoreInventory(p.New, false); err != nil {
		return err
	}
	if err := validateRestoreInventory(p.Old, true); err != nil {
		return err
	}
	if err := validateRestoreInventory(p.Post, true); err != nil {
		return err
	}
	manifest := backupManifest{Format: 1, Version: appVersion, Schema: p.Schema, Created: p.Created, Files: p.New.Files}
	if err := validateBackupManifest(manifest); err != nil {
		return err
	}
	if p.Previous != nil {
		if !restoreIDValid(p.Previous.ID) || !restoreIDValid(p.Previous.Generation) || !restoreTerminal(p.Previous.State) || len(p.PreviousChecksum) != 64 {
			return errors.New("上次恢复状态无效")
		}
	} else if p.PreviousChecksum != "" {
		return errors.New("上次恢复摘要无效")
	}
	return nil
}

func (m *restoreManager) loadJournal() (*restoreJournal, error) {
	if _, err := m.root.Lstat(restoreControlName); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := restoreCheckPath(m.root, restoreControlName, true); err != nil {
		return nil, err
	}
	name := path.Join(restoreControlName, "active.json")
	if _, err := m.root.Lstat(name); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var p restoreJournal
	if _, err := m.readEnvelope(name, &p); err != nil {
		return nil, err
	}
	if err := validateRestoreJournal(p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (m *restoreManager) saveJournal(p *restoreJournal, state string) error {
	p.Runtime.State = state
	if err := validateRestoreJournal(*p); err != nil {
		return err
	}
	return m.writeEnvelope(path.Join(restoreControlName, "active.json"), p, state)
}

func (m *restoreManager) inventoryAt(ctx context.Context, prefix string, wal bool) (restoreInventory, error) {
	if err := restoreCheckPath(m.root, prefix, true); err != nil {
		return restoreInventory{}, err
	}
	r, err := m.root.OpenRoot(prefix)
	if err != nil {
		return restoreInventory{}, err
	}
	defer r.Close()
	return collectRestoreInventory(ctx, r, wal)
}

func (m *restoreManager) verifyInventory(ctx context.Context, prefix string, want restoreInventory, wal bool) error {
	got, err := m.inventoryAt(ctx, prefix, wal)
	if err != nil {
		return err
	}
	if !sameRestoreInventory(got, want) {
		return errors.New("恢复资料与持久化清单不一致")
	}
	return nil
}

func (m *restoreManager) reserve(p *restoreJournal) error {
	name := path.Join(restoreControlName, p.Runtime.ID, "rollback.reserve")
	bytes, err := restoreReserveBytes(p)
	if err != nil {
		return err
	}
	if err := m.step("reserve.before_allocate"); err != nil {
		return err
	}
	if info, e := m.root.Lstat(name); e == nil && !info.Mode().IsRegular() {
		return errors.New("恢复空间预留文件无效")
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	f, err := m.root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = allocateRestoreReserve(f, bytes)
	closed := f.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	if err = m.syncDir(path.Dir(name)); err != nil {
		return err
	}
	return m.step("reserve.allocated")
}

func restoreReserveBytes(p *restoreJournal) (int64, error) {
	// Include New/Old/Post metadata, not just Old: a small prior database can
	// restore a very large inventory whose transition journal exceeds 8MiB.
	// Two envelope-sized allowances cover publication while active.json remains
	// plus transition growth, in addition to physical old payload/FS metadata.
	raw, err := json.Marshal(p)
	if err != nil || int64(len(raw)) > restoreJournalMax-256 {
		return 0, errors.New("恢复日志过大，无法预留回退空间")
	}
	bytes := int64(8<<20) + 2*(int64(len(raw))+256)
	for _, f := range p.Old.Files {
		bytes += f.Size
	}
	bytes += int64(len(p.Old.Files)+len(p.Old.Dirs)) * 16384
	return bytes, nil
}

func (m *restoreManager) releaseReserve(p *restoreJournal) error {
	name := path.Join(restoreControlName, p.Runtime.ID, "rollback.reserve")
	if err := m.step("reserve.before_release"); err != nil {
		return err
	}
	if err := m.root.Remove(name); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := m.syncDir(path.Dir(name)); err != nil {
		return err
	}
	return m.step("reserve.released")
}

// The immutable baseline is copied, never consumed. Only names in the recorded
// union may be removed. Unknown live managed files are preserved and fail final
// verification rather than being recursively deleted.
func (m *restoreManager) replaceSet(ctx context.Context, source string, want restoreInventory, remove restoreInventory) error {
	keep := map[string]bool{}
	for _, dir := range want.Dirs {
		keep[dir] = true
		if err := m.mkdir(dir); err != nil {
			return err
		}
	}
	for _, f := range want.Files {
		keep[f.Name] = true
		if err := m.copyFile(ctx, path.Join(source, f.Name), f.Name, f); err != nil {
			return err
		}
	}
	for _, f := range remove.Files {
		if keep[f.Name] {
			continue
		}
		if _, err := m.root.Lstat(f.Name); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := restoreCheckPath(m.root, f.Name, false); err != nil {
			return err
		}
		if err := m.step("remove.before:" + f.Name); err != nil {
			return err
		}
		if err := m.root.Remove(f.Name); err != nil {
			return err
		}
		if err := m.step("remove.after:" + f.Name); err != nil {
			return err
		}
		if err := m.syncDir(path.Dir(f.Name)); err != nil {
			return err
		}
	}
	dirs := append([]string(nil), remove.Dirs...)
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if keep[dir] {
			continue
		}
		if _, err := m.root.Lstat(dir); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := restoreCheckPath(m.root, dir, true); err != nil {
			return err
		}
		if err := m.step("remove.dir.before:" + dir); err != nil {
			return err
		}
		if err := m.root.Remove(dir); err != nil {
			return err
		}
		if err := m.step("remove.dir.after:" + dir); err != nil {
			return err
		}
		if err := m.syncDir(path.Dir(dir)); err != nil {
			return err
		}
	}
	// Each nested/empty directory entry must be durable in its parent before a
	// terminal journal hands the active set to mutable runtime. Leaf file Sync
	// and data-root Sync alone do not persist intermediate directory ancestry.
	affected := unionRestoreInventory(want, remove)
	parents := map[string]bool{".": true}
	for _, dir := range affected.Dirs {
		for dir != "." {
			parents[dir] = true
			dir = path.Dir(dir)
		}
	}
	for _, file := range affected.Files {
		for dir := path.Dir(file.Name); dir != "."; dir = path.Dir(dir) {
			parents[dir] = true
		}
	}
	ordered := make([]string, 0, len(parents))
	for dir := range parents {
		ordered = append(ordered, dir)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, dir := range ordered {
		if _, err := m.root.Lstat(dir); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := m.syncDir(dir); err != nil {
			return err
		}
	}
	return m.verifyInventory(ctx, ".", want, true)
}

func unionRestoreInventory(a, b restoreInventory) restoreInventory {
	files := map[string]backupFile{}
	dirs := map[string]bool{}
	for _, f := range a.Files {
		files[f.Name] = f
	}
	for _, f := range b.Files {
		files[f.Name] = f
	}
	for _, d := range a.Dirs {
		dirs[d] = true
	}
	for _, d := range b.Dirs {
		dirs[d] = true
	}
	out := restoreInventory{}
	for _, f := range files {
		out.Files = append(out.Files, f)
	}
	for d := range dirs {
		out.Dirs = append(out.Dirs, d)
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Name < out.Files[j].Name })
	sort.Strings(out.Dirs)
	return out
}

func (m *restoreManager) recoverStartup(ctx context.Context) (restoreRuntime, error) {
	// main holds the exclusive data lock here, before opening SQLite/auth or
	// workers. Also collect interrupted uploads when there is NO active journal.
	if err := m.temporaryFiles().cleanupExports(); err != nil {
		return restoreRuntime{}, err
	}
	if err := m.cleanupUnarmed(); err != nil {
		return restoreRuntime{}, err
	}
	p, err := m.loadJournal()
	if err != nil || p == nil {
		return restoreRuntime{}, err
	}
	if !restorePlatformSupported() {
		return restoreRuntime{}, errors.New("当前平台不支持恢复激活")
	}
	if err = m.cleanScratch(); err != nil {
		return restoreRuntime{}, err
	}
	base := path.Join(restoreControlName, p.Runtime.ID)
	state := p.Runtime.State
	if state == "pending" {
		if err = m.verifyInventory(ctx, path.Join(base, "candidate"), p.New, false); err != nil {
			return restoreRuntime{}, err
		}
		p.Old, err = collectRestoreInventory(ctx, m.root, true)
		if err != nil {
			return restoreRuntime{}, err
		}
		if err = m.copyInventory(ctx, ".", path.Join(base, "rollback"), p.Old); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.verifyInventory(ctx, path.Join(base, "rollback"), p.Old, true); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.saveJournal(p, "prepared"); err != nil {
			return restoreRuntime{}, err
		}
		state = "prepared"
	}
	if state == "prepared" {
		if err = m.verifyInventory(ctx, path.Join(base, "candidate"), p.New, false); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.verifyInventory(ctx, path.Join(base, "rollback"), p.Old, true); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.verifyInventory(ctx, ".", p.Old, true); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.reserve(p); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.saveJournal(p, "installing"); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.replaceSet(ctx, path.Join(base, "candidate"), p.New, p.Old); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.saveJournal(p, "installed"); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.releaseReserve(p); err != nil {
			return restoreRuntime{}, err
		}
		return p.Runtime, nil
	}
	if state == "rollback_pending" {
		p.Post, err = collectRestoreInventory(ctx, m.root, true)
		if err != nil {
			return restoreRuntime{}, err
		}
		if err = m.copyInventory(ctx, ".", path.Join(base, "post"), p.Post); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.verifyInventory(ctx, path.Join(base, "post"), p.Post, true); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.saveJournal(p, "rollback_prepared"); err != nil {
			return restoreRuntime{}, err
		}
		state = "rollback_prepared"
	}
	if state == "rollback_prepared" {
		if err = m.verifyInventory(ctx, path.Join(base, "post"), p.Post, true); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.verifyInventory(ctx, ".", p.Post, true); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.reserve(p); err != nil {
			return restoreRuntime{}, err
		}
	}
	if state == "installing" || state == "rollback_prepared" {
		if err = m.verifyInventory(ctx, path.Join(base, "rollback"), p.Old, true); err != nil {
			return restoreRuntime{}, err
		}
		// Release BEFORE allocating the rolling_back journal. ENOSPC may have
		// prevented installed publication; requiring a fresh temp journal first
		// would make our reserved space unreachable. Installing is already a
		// durable instruction to roll back, so this release is retry-safe.
		if err = m.releaseReserve(p); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.saveJournal(p, "rolling_back"); err != nil {
			return restoreRuntime{}, err
		}
		state = "rolling_back"
	}
	if state == "rolling_back" {
		if err = m.verifyInventory(ctx, path.Join(base, "rollback"), p.Old, true); err != nil {
			return restoreRuntime{}, err
		}
		// A prior interrupted attempt may have already freed/consumed the reserve.
		// Never allocate again once rolling_back is durable.
		if err = m.releaseReserve(p); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.replaceSet(ctx, path.Join(base, "rollback"), p.Old, unionRestoreInventory(p.New, p.Post)); err != nil {
			return restoreRuntime{}, err
		}
		if err = m.saveJournal(p, "rolled_back"); err != nil {
			return restoreRuntime{}, err
		}
		return p.Runtime, nil
	}
	if !restoreTerminal(state) {
		return restoreRuntime{}, errors.New("恢复日志无法确定下一步")
	}
	// Terminal states are handed to mutable runtime. Historical installation
	// hashes must never be compared to a legitimately modified active DB/auth set.
	if state == "installed" {
		if err = m.releaseReserve(p); err != nil {
			return restoreRuntime{}, err
		}
	}
	return p.Runtime, nil
}

func (m *restoreManager) requestRollback(id string) error {
	p, err := m.loadJournal()
	if err != nil {
		return err
	}
	if p == nil || p.Runtime.ID != id || (p.Runtime.State != "installed" && p.Runtime.State != "released_new") {
		return errors.New("当前没有可回退的恢复会话")
	}
	p.Runtime.Generation = randomID()
	return m.saveJournal(p, "rollback_pending")
}

func (m *restoreManager) releaseReview(id, generation string) error {
	p, err := m.loadJournal()
	if err != nil {
		return err
	}
	if p == nil || p.Runtime.ID != id || p.Runtime.Generation != generation {
		return errors.New("恢复会话已变化")
	}
	switch p.Runtime.State {
	case "installed":
		return m.saveJournal(p, "released_new")
	case "rolled_back":
		return m.saveJournal(p, "released_old")
	default:
		return errors.New("恢复会话尚不能完成核对")
	}
}

// Arm/cancel never alter live managed files. Save the complete prior terminal
// journal before publishing a replacement so hold/release and generation survive.
func (m *restoreManager) armCandidate(ctx context.Context, id, revision string, in restoreInventory) error {
	if !restoreIDValid(id) || restoreRevision(in) != revision {
		return errors.New("恢复候选版本已变化")
	}
	if !restorePlatformSupported() {
		return errors.New("当前平台不支持恢复激活")
	}
	if err := validateRestoreInventory(in, false); err != nil {
		return err
	}
	if err := m.verifyInventory(ctx, path.Join(restoreControlName, id, "candidate"), in, false); err != nil {
		return err
	}
	if err := m.syncTree(ctx, path.Join(restoreControlName, id)); err != nil {
		return err
	}
	prior, err := m.loadJournal()
	if err != nil {
		return err
	}
	used, err := m.protectedHistory(prior)
	if err != nil {
		return err
	}
	if used[id] {
		return errors.New("该恢复会话已使用，请重新上传")
	}
	// Admission must leave room for the new active node. Otherwise a valid
	// confirmation can create a history that startup and cancellation refuse.
	if len(used) >= restoreHistoryLimit {
		return errors.New("恢复保留会话已达上限，未启用新候选；请先保留并整理历史恢复资料")
	}
	p := restoreJournal{Format: 1, Schema: currentSchemaVersion, Runtime: restoreRuntime{id, "pending", randomID()}, Created: time.Now().Unix(), New: in}
	if prior != nil {
		if !restoreTerminal(prior.Runtime.State) || prior.Runtime.ID == id {
			return errors.New("已有恢复操作等待完成")
		}
		p.Previous = &prior.Runtime
		p.PreviousChecksum = restoreRevision(prior)
		if err = m.writeEnvelope(path.Join(restoreControlName, id, "prior.json"), prior, "prior"); err != nil {
			return err
		}
		if err = m.syncTree(ctx, path.Join(restoreControlName, id)); err != nil {
			return err
		}
	}
	return m.saveJournal(&p, "pending")
}

func (m *restoreManager) cancelPending(id string) error {
	p, upload, _, err := m.candidateProtection()
	if err != nil {
		return err
	}
	if p == nil || p.Runtime.ID != id || (p.Runtime.State != "pending" && p.Runtime.State != "prepared") {
		return errors.New("恢复已进入启动处理，无法取消")
	}
	if p.Runtime.State == "prepared" {
		if err = m.verifyInventory(context.Background(), ".", p.Old, true); err != nil {
			return err
		}
		if err = m.releaseReserve(p); err != nil {
			return err
		}
	}
	if err = m.ensureCandidateOwnership(id, p); err != nil {
		return err
	}
	// Remove the upload protection BEFORE cancellation publication. Until the
	// active journal is reverted it still protects the candidate after any crash.
	if upload != nil && upload.ID == id {
		if err = m.forgetUpload(id); err != nil {
			return err
		}
	}
	if err = m.step("cancel.upload_removed"); err != nil {
		return err
	}
	// Persist cleanup eligibility before reverting the active pointer. If the
	// process dies after cancellation publication, history-aware orphan cleanup
	// can still identify this exact never-installed session.
	if err = m.writeEnvelope(path.Join(restoreControlName, id, "unarmed.json"), restoreUpload{ID: id}, "cancel-cleanup"); err != nil {
		return err
	}
	if p.Previous != nil {
		var prior restoreJournal
		checksum, e := m.readEnvelope(path.Join(restoreControlName, id, "prior.json"), &prior)
		if e != nil {
			return e
		}
		if checksum != p.PreviousChecksum || prior.Runtime != *p.Previous {
			return errors.New("上次恢复资料不匹配")
		}
		if e = validateRestoreJournal(prior); e != nil {
			return e
		}
		return m.saveJournal(&prior, prior.Runtime.State)
	}
	if err = m.root.Remove(path.Join(restoreControlName, "active.json")); err != nil {
		return err
	}
	return m.syncDir(restoreControlName)
}
