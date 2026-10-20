package main

import (
	"errors"
	"os"
)

// Hold the returned descriptor until process shutdown, including all startup
// restore/rollback work. Never unlink the lock file: another process may already
// hold its inode. Kernel locks are released even after abrupt process death.
func acquireDataLock(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("数据目录无法建立")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.New("数据目录无法打开")
	}
	defer root.Close()
	if info, e := root.Lstat(".yolink.lock"); e == nil && !info.Mode().IsRegular() || e != nil && !os.IsNotExist(e) {
		return nil, errors.New("数据目录锁文件无效")
	}
	f, err := root.OpenFile(".yolink.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("数据目录锁无法打开")
	}
	if err = lockDataFile(f); err != nil {
		f.Close()
		return nil, errors.New("数据目录已被其他进程占用或无法取得独占锁")
	}
	return f, nil
}
