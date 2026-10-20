package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

const restoreControlName = ".restore-control"

type restoreInventory struct {
	Files []backupFile `json:"files"`
	Dirs  []string     `json:"dirs"`
}

var restoreTopFiles = []string{"strmlink.db", "credentials.key", "playback.key", "events.key", "admin-credential.json", "initial-admin-password.txt"}

func restoreIDValid(id string) bool {
	return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == ""
}

func restoreManagedName(name string, wal bool) bool {
	return backupNameAllowed(name) || wal && (name == "strmlink.db-wal" || name == "strmlink.db-shm")
}

func restoreManagedDir(name string) bool {
	if name == "plans" || name == "strm-backups" {
		return true
	}
	return strings.HasPrefix(name, "strm-backups/") && backupNameAllowed(name+"/.directory-check.strm")
}

func validateRestoreInventory(in restoreInventory, wal bool) error {
	if len(in.Files) > backupMaxFiles || len(in.Dirs) > backupMaxFiles {
		return errors.New("恢复清单数量超限")
	}
	seen := map[string]bool{}
	var total int64
	for _, f := range in.Files {
		key := strings.ToLower(f.Name)
		if !restoreManagedName(f.Name, wal) || seen[key] || f.Size < 0 || f.Size > backupMaxFileBytes || len(f.SHA256) != 64 || strings.Trim(f.SHA256, "0123456789abcdef") != "" {
			return errors.New("恢复文件清单无效")
		}
		seen[key] = true
		total += f.Size
		if total > backupMaxTotalBytes {
			return errors.New("恢复清单总量超限")
		}
	}
	for _, dir := range in.Dirs {
		key := strings.ToLower(dir)
		if !restoreManagedDir(dir) || seen[key] {
			return errors.New("恢复目录清单无效")
		}
		seen[key] = true
	}
	for _, f := range in.Files {
		for parent := path.Dir(f.Name); parent != "."; parent = path.Dir(parent) {
			if !seen[strings.ToLower(parent)] {
				return errors.New("恢复清单缺少父目录")
			}
		}
	}
	return nil
}

// All parents and leaves must be real directories/files on the same filesystem.
// Root containment alone permits symlinks/bind mounts, so check those separately.
func restoreCheckPath(root *os.Root, name string, directory bool) error {
	parts := strings.Split(name, "/")
	for i := range parts {
		name := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		isDir := i < len(parts)-1 || directory
		if info.Mode()&os.ModeSymlink != 0 || isDir && !info.IsDir() || !isDir && !info.Mode().IsRegular() {
			return errors.New("恢复路径包含链接或非普通文件")
		}
		if err = restoreSameDevice(root, name); err != nil {
			return err
		}
	}
	return nil
}

func collectRestoreInventory(ctx context.Context, root *os.Root, wal bool) (restoreInventory, error) {
	out := restoreInventory{}
	var total int64
	addFile := func(name string) error {
		if !restoreManagedName(name, wal) {
			return errors.New("托管目录包含未支持文件")
		}
		if err := restoreCheckPath(root, name, false); err != nil {
			return err
		}
		f, err := backupFileDigest(ctx, root, name)
		if err != nil {
			return err
		}
		out.Files = append(out.Files, f)
		total += f.Size
		if len(out.Files) > backupMaxFiles || total > backupMaxTotalBytes {
			return errors.New("托管资料数量或总量超限")
		}
		return nil
	}
	names := append([]string(nil), restoreTopFiles...)
	if wal {
		names = append(names, "strmlink.db-wal", "strmlink.db-shm")
	}
	for _, name := range names {
		if _, err := root.Lstat(name); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return out, err
		}
		if err := addFile(name); err != nil {
			return out, err
		}
	}
	for _, tree := range []string{"plans", "strm-backups"} {
		if _, err := root.Lstat(tree); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return out, err
		}
		err := fs.WalkDir(root.FS(), tree, func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				if !restoreManagedDir(name) || len(out.Dirs) >= backupMaxFiles {
					return errors.New("托管目录无效或过多")
				}
				if err := restoreCheckPath(root, name, true); err != nil {
					return err
				}
				out.Dirs = append(out.Dirs, name)
				return nil
			}
			return addFile(name)
		})
		if err != nil {
			return out, err
		}
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Name < out.Files[j].Name })
	sort.Strings(out.Dirs)
	return out, validateRestoreInventory(out, wal)
}

func sameRestoreInventory(a, b restoreInventory) bool {
	if len(a.Files) != len(b.Files) || len(a.Dirs) != len(b.Dirs) {
		return false
	}
	for i := range a.Files {
		if a.Files[i] != b.Files[i] {
			return false
		}
	}
	for i := range a.Dirs {
		if a.Dirs[i] != b.Dirs[i] {
			return false
		}
	}
	return true
}

type restoreManager struct {
	root *os.Root
	// Test seam for disk errors/process interruption at actual durability steps.
	fault func(string) error
}

func openRestoreManager(data string) (*restoreManager, error) {
	root, err := os.OpenRoot(data)
	if err != nil {
		return nil, err
	}
	return &restoreManager{root: root}, nil
}

func (m *restoreManager) Close() error { return m.root.Close() }
func (m *restoreManager) step(name string) error {
	if m.fault != nil {
		return m.fault(name)
	}
	return nil
}

func (m *restoreManager) syncDir(name string) error {
	if err := m.step("dir.sync.before:" + name); err != nil {
		return err
	}
	if err := restoreCheckPath(m.root, name, true); err != nil {
		return err
	}
	f, err := m.root.Open(name)
	if err != nil {
		return err
	}
	err = restoreDirectorySync(f)
	closed := f.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	return m.step("dir.sync.after:" + name)
}

func (m *restoreManager) syncAncestors(name string) error {
	for {
		if err := m.syncDir(name); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		name = path.Dir(name)
	}
}

func (m *restoreManager) mkdir(name string) error {
	if name == "." {
		return nil
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		if err := m.step("dir.mkdir.before:" + prefix); err != nil {
			return err
		}
		err := m.root.Mkdir(prefix, 0700)
		if err != nil && !os.IsExist(err) {
			return err
		}
		if err = restoreCheckPath(m.root, prefix, true); err != nil {
			return err
		}
		if err = m.step("dir.mkdir.after:" + prefix); err != nil {
			return err
		}
	}
	return nil
}

func (m *restoreManager) syncTree(ctx context.Context, prefix string) error {
	dirs := []string{}
	err := fs.WalkDir(m.root.FS(), prefix, func(name string, entry fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if e = restoreCheckPath(m.root, name, entry.IsDir()); e != nil {
			return e
		}
		if entry.IsDir() {
			dirs = append(dirs, name)
			return nil
		}
		f, e := m.root.OpenFile(name, os.O_RDWR, 0)
		if e != nil {
			return e
		}
		e = m.step("tree.file.sync.before:" + name)
		if e == nil {
			e = f.Sync()
		}
		closed := f.Close()
		if e != nil {
			return e
		}
		if closed != nil {
			return closed
		}
		return m.step("tree.file.sync.after:" + name)
	})
	if err != nil {
		return err
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if err = m.syncDir(dir); err != nil {
			return err
		}
	}
	for parent := path.Dir(prefix); ; parent = path.Dir(parent) {
		if err = m.syncDir(parent); err != nil {
			return err
		}
		if parent == "." {
			break
		}
	}
	return nil
}

func (m *restoreManager) copyFile(ctx context.Context, source, destination string, expected backupFile) (err error) {
	if err = restoreCheckPath(m.root, source, false); err != nil {
		return err
	}
	if err = m.mkdir(path.Dir(destination)); err != nil {
		return err
	}
	if err = m.step("copy.before:" + destination); err != nil {
		return err
	}
	in, err := m.root.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	// Scratch never lives in plans/ or another active managed directory. A
	// killed copy must not leave an unknown file that blocks rollback enumeration.
	tempDir := path.Join(restoreControlName, "scratch")
	if err = m.mkdir(tempDir); err != nil {
		return err
	}
	temp := path.Join(tempDir, ".restore-copy-"+randomID())
	out, err := m.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = m.root.Remove(temp) }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(backupContextReader{ctx, in}, expected.Size+1))
	if err == nil && (n != expected.Size || hex.EncodeToString(h.Sum(nil)) != expected.SHA256) {
		err = errors.New("恢复来源大小或摘要变化")
	}
	if err == nil {
		err = m.step("file.sync.before:" + destination)
	}
	if err == nil {
		err = out.Sync()
	}
	closed := out.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	if err = m.step("file.sync.after:" + destination); err != nil {
		return err
	}
	if info, e := m.root.Lstat(destination); e == nil {
		if !info.Mode().IsRegular() {
			return errors.New("恢复目标已变为非普通文件")
		}
		if e = restoreCheckPath(m.root, destination, false); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	if err = m.step("rename.before:" + destination); err != nil {
		return err
	}
	if err = m.root.Rename(temp, destination); err != nil {
		return err
	}
	if err = m.step("rename.after:" + destination); err != nil {
		return err
	}
	if err = m.syncDir(tempDir); err != nil {
		return err
	}
	return m.syncDir(path.Dir(destination))
}

func (m *restoreManager) cleanScratch() error {
	dir := path.Join(restoreControlName, "scratch")
	if _, err := m.root.Lstat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := restoreCheckPath(m.root, dir, true); err != nil {
		return err
	}
	entries, err := fs.ReadDir(m.root.FS(), dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id := strings.TrimPrefix(entry.Name(), ".restore-copy-")
		if id == entry.Name() || !restoreIDValid(id) || !entry.Type().IsRegular() {
			return errors.New("恢复暂存目录包含未知资料，已保留")
		}
		if err = m.step("scratch.remove.before"); err != nil {
			return err
		}
		if err = m.root.Remove(path.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return m.syncDir(dir)
}

func (m *restoreManager) copyInventory(ctx context.Context, from, to string, in restoreInventory) error {
	if err := m.mkdir(to); err != nil {
		return err
	}
	for _, dir := range in.Dirs {
		if err := m.mkdir(path.Join(to, dir)); err != nil {
			return err
		}
	}
	for _, file := range in.Files {
		if err := m.copyFile(ctx, path.Join(from, file.Name), path.Join(to, file.Name), file); err != nil {
			return fmt.Errorf("恢复副本创建失败: %w", err)
		}
	}
	return m.syncTree(ctx, to)
}
