package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type repairDirectory struct {
	root *os.Root
	id   string
	name string
}

// Pin each directory object and compare it with the no-follow observation.
// A parent swapped for a symlink cannot redirect publication to another tree.
func (e *Engine) openRepairDirectory(key string) (*repairDirectory, error) {
	root, rel, _, err := e.outputLocation(key)
	if err != nil {
		return nil, err
	}
	parent, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	closeError := func(err error) (*repairDirectory, error) { parent.Close(); return nil, err }
	for _, mount := range e.outputMounts {
		if mount.root != root {
			continue
		}
		physical, er := os.Lstat(filepath.FromSlash(mount.Path))
		opened, e2 := parent.Stat(".")
		if er != nil || e2 != nil || physical.Mode()&os.ModeSymlink != 0 || !os.SameFile(physical, opened) {
			return closeError(errors.New("输出挂载目录已变化"))
		}
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/") {
		if part == "." {
			continue
		}
		before, er := parent.Lstat(part)
		if er != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			return closeError(errors.New("输出父目录缺失、不可访问或包含链接"))
		}
		if er = temporarySameDevice(parent, part); er != nil {
			return closeError(errors.New("输出路径跨卷或包含重解析点"))
		}
		child, er := parent.OpenRoot(part)
		if er != nil {
			return closeError(er)
		}
		after, er := child.Stat(".")
		if er != nil || !os.SameFile(before, after) {
			child.Close()
			return closeError(errors.New("输出父目录在核对期间已变化"))
		}
		parent.Close()
		parent = child
	}
	id, err := repairDirectoryID(parent)
	if err != nil {
		return closeError(err)
	}
	return &repairDirectory{parent, id, filepath.Base(rel)}, nil
}

func (e *Engine) publishMissingRepair(key, parentID string, held *repairDirectory, content []byte) (bool, error) {
	check, err := e.openRepairDirectory(key)
	if err != nil {
		return false, err
	}
	check.root.Close()
	if check.id != parentID || held.id != parentID {
		return false, errors.New("输出父目录已变化，请重新预览")
	}
	if _, err = held.root.Lstat(held.name); !os.IsNotExist(err) {
		return false, errors.New("文件已出现或不可访问，已保留")
	}
	temp := ".strmlink-" + randomID() + ".tmp"
	f, err := held.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return false, err
	}
	defer held.root.Remove(temp)
	_, err = f.Write(content)
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil {
		return false, err
	}
	if closed != nil {
		return false, closed
	}
	if err = held.root.Link(temp, held.name); err != nil {
		return false, errors.New("文件已出现或卷不支持独占发布，已保留")
	}
	dir, err := held.root.Open(".")
	if err != nil {
		return true, err
	}
	defer dir.Close()
	if err = temporaryDirectorySync(dir); err != nil {
		return true, err
	}
	return true, nil
}
