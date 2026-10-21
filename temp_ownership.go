package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

const (
	backupWorkName        = ".backup-work"
	temporaryClaimMax     = 1024
	temporaryControlLimit = 1000
	// Legacy archives admitted at most 100k paths of at most4096 bytes each,
	// but did not count their distinct parents. Include that finite worst case
	// so interrupted older staging can still be collected. New admission limits
	// aggregate parents to backupMaxDirs; no huge allocation follows this budget.
	temporaryTreeLimit    = (backupMaxFiles+1)*(backupMaxPathBytes/2+2) + 128
	temporaryDepthLimit   = backupMaxPathBytes/2 + 8 // shortest component is "d/"; allow control/stage wrappers
	temporaryPendingLimit = backupMaxFiles*4 + 128
)

// Claims live OUTSIDE the tree they authorize. Callers hold the data-directory
// process lock, and (online) backupMu, throughout creation/use/collection. This
// module neither acquires another lock nor uses the request's canceled context.
type temporaryFiles struct {
	root  *os.Root
	fault func(string) error
}

type temporaryClaim struct {
	Format int    `json:"format"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
}

func (o temporaryFiles) step(point string) error {
	if o.fault != nil {
		return o.fault(point)
	}
	return nil
}

func temporaryNames(kind, id string) (target, claim string, err error) {
	if !restoreIDValid(id) {
		return "", "", errors.New("临时资料身份无效")
	}
	switch kind {
	case "export":
		return path.Join(backupWorkName, id), path.Join(backupWorkName, id+".json"), nil
	case "candidate":
		return path.Join(restoreControlName, id), path.Join(restoreControlName, "unarmed-"+id+".json"), nil
	}
	return "", "", errors.New("临时资料类型无效")
}

// Export support is separate from the deliberately Linux-only activation gate.
func temporaryCheckPath(root *os.Root, name string, directory bool) error {
	parts := strings.Split(name, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(prefix)
		if err != nil {
			return err
		}
		dir := i < len(parts)-1 || directory
		if info.Mode()&os.ModeSymlink != 0 || dir && !info.IsDir() || !dir && !info.Mode().IsRegular() {
			return errors.New("临时资料包含链接或非普通文件，已保留")
		}
		if err = temporarySameDevice(root, prefix); err != nil {
			return err
		}
	}
	return nil
}

func (o temporaryFiles) syncDir(name string) error {
	if err := o.step("ownership.dir.sync.before:" + name); err != nil {
		return err
	}
	if err := temporaryCheckPath(o.root, name, true); err != nil {
		return err
	}
	f, err := o.root.Open(name)
	if err != nil {
		return err
	}
	err = temporaryDirectorySync(f)
	closed := f.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	return o.step("ownership.dir.sync.after:" + name)
}

func (o temporaryFiles) ensureParent(parent string) error {
	if err := o.root.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err := temporaryCheckPath(o.root, parent, true); err != nil {
		return err
	}
	if err := o.syncDir(parent); err != nil {
		return err
	}
	return o.syncDir(".")
}

func (o temporaryFiles) readClaim(kind, id string) error {
	_, name, err := temporaryNames(kind, id)
	if err != nil {
		return err
	}
	if err = temporaryCheckPath(o.root, name, false); err != nil {
		return err
	}
	f, err := o.root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > temporaryClaimMax {
		return errors.New("临时资料所有权记录过大")
	}
	raw, err := io.ReadAll(io.LimitReader(f, temporaryClaimMax+1))
	if err != nil || len(raw) > temporaryClaimMax {
		return errors.New("临时资料所有权记录读取失败")
	}
	var envelope restoreEnvelope
	if err = restoreDecode(raw, &envelope); err != nil {
		return err
	}
	sum := sha256.Sum256(envelope.Data)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return errors.New("临时资料所有权摘要不匹配")
	}
	var claim temporaryClaim
	if err = restoreDecode(envelope.Data, &claim); err != nil {
		return err
	}
	if claim != (temporaryClaim{1, kind, id}) {
		return errors.New("临时资料所有权身份不匹配")
	}
	return nil
}

// publish never replaces an existing claim. The atomic hard-link publication
// gives exclusive-create semantics even if an unexpected file appears after
// preflight. A failed publication NEVER grants permission to create plaintext.
func (o temporaryFiles) publish(kind, id string) error {
	_, name, err := temporaryNames(kind, id)
	if err != nil {
		return err
	}
	if err = o.ensureParent(path.Dir(name)); err != nil {
		return err
	}
	data, err := json.Marshal(temporaryClaim{1, kind, id})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	raw, err := json.Marshal(restoreEnvelope{data, hex.EncodeToString(sum[:])})
	if err != nil || len(raw) > temporaryClaimMax {
		return errors.New("临时所有权记录超限")
	}
	temp := path.Join(path.Dir(name), ".ownership-"+randomID())
	if err = o.step("ownership.claim.before_write"); err != nil {
		return err
	}
	f, err := o.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = o.root.Remove(temp) }()
	_, err = f.Write(raw)
	if err == nil {
		err = o.step("ownership.claim.before_sync")
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
	if err = o.step("ownership.claim.before_publish"); err != nil {
		return err
	}
	if err = o.root.Link(temp, name); err != nil {
		return err
	}
	if err = o.step("ownership.claim.after_publish"); err != nil {
		return err
	}
	if err = o.root.Remove(temp); err != nil {
		return err
	}
	if err = o.syncDir(path.Dir(name)); err != nil {
		return err
	}
	return o.step("ownership.claim.durable")
}

func (o temporaryFiles) create(kind, id string) (string, error) {
	target, claim, err := temporaryNames(kind, id)
	if err != nil {
		return "", err
	}
	// Never retrospectively adopt a pre-existing tree, even an empty directory.
	for _, name := range []string{target, claim} {
		if _, err = o.root.Lstat(name); err == nil {
			return "", os.ErrExist
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	if err = o.publish(kind, id); err != nil {
		return "", err
	}
	if err = o.step("ownership.target.before_mkdir"); err != nil {
		return "", err
	}
	if err = o.root.Mkdir(target, 0700); err != nil {
		return "", err
	}
	if err = temporaryCheckPath(o.root, target, true); err != nil {
		return "", err
	}
	if err = o.syncDir(path.Dir(target)); err != nil {
		return "", err
	}
	return target, o.step("ownership.target.created")
}

// Incremental reads bound allocation BEFORE accepting an untrusted directory.
func (o temporaryFiles) entries(name string, limit int) ([]os.DirEntry, error) {
	if err := temporaryCheckPath(o.root, name, true); err != nil {
		return nil, err
	}
	f, err := o.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(limit + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errors.New("临时控制资料过多，请先检查磁盘")
	}
	return entries, nil
}

// Every leaf is checked, including nested device/mount boundaries. Validate the
// entire bounded tree before starting deletion; revalidate during deletion too.
type temporaryWalkBudget struct {
	remaining int
	pending   int
}

func (o temporaryFiles) walk(name string, depth int, budget *temporaryWalkBudget, remove bool) error {
	if depth > temporaryDepthLimit {
		return errors.New("临时目录嵌套过深，已保留")
	}
	budget.remaining--
	if budget.remaining < 0 {
		return errors.New("临时资料数量超限，已保留")
	}
	info, err := o.root.Lstat(name)
	if err != nil {
		return err
	}
	// Ancestors were checked before descending under the exclusive process/op
	// locks. Check each new leaf/device once instead of rewalking every ancestor
	// for every node (quadratic on deep legal paths).
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
		return errors.New("临时资料包含链接或非普通文件，已保留")
	}
	if err = temporarySameDevice(o.root, name); err != nil {
		return err
	}
	if info.IsDir() {
		f, err := o.root.Open(name)
		if err != nil {
			return err
		}
		// Bound both incremental allocation and the outstanding DFS frontier. A
		// legacy tree from N accepted files has at most N pending branches, even
		// when its total number of intermediate directories is much larger.
		var children []string
		defer func() { budget.pending -= len(children) }()
		err = func() error {
			for {
				entries, e := f.ReadDir(128)
				if e != nil && e != io.EOF {
					return e
				}
				for _, entry := range entries {
					if budget.pending >= temporaryPendingLimit {
						return errors.New("临时目录待检查项目过多，已保留")
					}
					budget.pending++
					children = append(children, entry.Name())
				}
				if e == io.EOF {
					return nil
				}
			}
		}()
		closed := f.Close() // Windows cannot remove a directory with open readers.
		if err != nil {
			return err
		}
		if closed != nil {
			return closed
		}
		// No ancestor directory handle is held during recursion. This supports
		// deep legacy candidates even under a small process file-descriptor limit.
		for _, child := range children {
			if err = o.walk(path.Join(name, child), depth+1, budget, remove); err != nil {
				return err
			}
		}
	}
	if remove {
		if err = o.step("ownership.remove.before:" + name); err != nil {
			return err
		}
		if err = o.root.Remove(name); err != nil {
			return err
		}
		return o.step("ownership.remove.after:" + name)
	}
	return nil
}

func (o temporaryFiles) removeTree(name string) error {
	if _, err := o.root.Lstat(name); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := temporaryCheckPath(o.root, name, true); err != nil {
		return err
	}
	budget := temporaryWalkBudget{remaining: temporaryTreeLimit}
	if err := o.walk(name, 0, &budget, false); err != nil {
		return err
	}
	budget = temporaryWalkBudget{remaining: temporaryTreeLimit}
	return o.walk(name, 0, &budget, true)
}

func (o temporaryFiles) cleanup(kind, id string) error {
	target, claim, err := temporaryNames(kind, id)
	if err != nil {
		return err
	}
	if err = o.readClaim(kind, id); err != nil {
		return err
	}
	if err = o.removeTree(target); err != nil {
		return err
	}
	// Keep the external claim until deletion of the target is durable. A failure
	// syncing AFTER claim unlink may leave the claim absent or resurrected after
	// a crash; both are safe because target deletion was synced first.
	if err = o.syncDir(path.Dir(target)); err != nil {
		return err
	}
	if err = o.step("ownership.claim.before_remove"); err != nil {
		return err
	}
	if err = o.root.Remove(claim); err != nil {
		return err
	}
	if err = o.step("ownership.claim.after_remove"); err != nil {
		return err
	}
	return o.syncDir(path.Dir(claim))
}

func (o temporaryFiles) cleanupExports() error {
	if _, err := o.root.Lstat(backupWorkName); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := temporaryCheckPath(o.root, backupWorkName, true); err != nil {
		return err
	}
	dir, err := o.root.Open(backupWorkName)
	if err != nil {
		return err
	}
	defer dir.Close()
	// Stream the control directory in bounded chunks rather than loading
	// it whole. Previously one malformed claim — or simply more than
	// temporaryControlLimit entries — aborted the sweep before anything
	// was collected, so valid exports piled up behind a single bad file.
	var ids []string
	unrecognized := 0
	overflow := false
	for {
		chunk, readErr := dir.ReadDir(256)
		for _, entry := range chunk {
			name := entry.Name()
			if id := strings.TrimSuffix(name, ".json"); id != name && restoreIDValid(id) {
				if len(ids) < temporaryControlLimit {
					ids = append(ids, id)
				} else {
					overflow = true
				}
				continue
			}
			if restoreIDValid(name) {
				// A bare id is a target directory (possibly an operator's
				// own, unclaimed one): recognized, never deleted here.
				continue
			}
			unrecognized++
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	// With the directory dominated by files we cannot classify, claims
	// can no longer be told apart from clutter reliably; refuse before
	// deleting anything, exactly as the previous hard limit did.
	if unrecognized >= temporaryControlLimit {
		return errors.New("临时控制资料过多，请先检查磁盘")
	}
	var failures []error
	for _, id := range ids {
		// cleanup re-validates the claim before removing anything, so a
		// malformed entry keeps its tree; record the failure and carry on
		// with the remaining exports instead of abandoning them.
		if err := o.cleanup("export", id); err != nil {
			failures = append(failures, fmt.Errorf("导出临时文件 %s 清理失败：%w", id, err))
		}
	}
	if overflow {
		failures = append(failures, errors.New("临时控制资料过多，本次仅清理了一部分，请再次运行清理"))
	}
	return errors.Join(failures...)
}

func (m *restoreManager) temporaryFiles() temporaryFiles {
	return temporaryFiles{root: m.root, fault: m.step}
}
