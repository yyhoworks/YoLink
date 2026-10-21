//go:build linux

package main

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
)

func restoreDirectorySync(f *os.File) error { return f.Sync() }
func restoreSameDevice(root *os.Root, name string) error {
	a, err := root.Open(".")
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := root.Open(name)
	if err != nil {
		return err
	}
	defer b.Close()
	var as, bs unix.Stat_t
	if err = unix.Fstat(int(a.Fd()), &as); err != nil {
		return err
	}
	if err = unix.Fstat(int(b.Fd()), &bs); err != nil {
		return err
	}
	if as.Dev != bs.Dev {
		return errors.New("恢复路径跨文件系统")
	}
	return nil
}
func allocateRestoreReserve(f *os.File, size int64) error {
	if err := unix.Fallocate(int(f.Fd()), 0, 0, size); err != nil {
		if errors.Is(err, unix.ENOSPC) {
			return fmt.Errorf("磁盘空间不足，无法预留恢复回滚空间：%w", err)
		}
		if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOSYS) {
			return err
		}
		// The filesystem cannot preallocate (some overlay, network or
		// encrypted filesystems). Fall back to writing the reserve out in
		// full: only the mechanism changes, the space is still really held
		// before the restore starts replacing files.
		if ferr := fillRestoreReserve(f, size); ferr != nil {
			return ferr
		}
	}
	return f.Sync()
}

// fillRestoreReserve materializes a reserve file by writing zeroes, for
// filesystems without fallocate support.
func fillRestoreReserve(f *os.File, size int64) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	zeros := make([]byte, 1<<20)
	for remaining := size; remaining > 0; {
		chunk := int64(len(zeros))
		if remaining < chunk {
			chunk = remaining
		}
		if _, err := f.Write(zeros[:chunk]); err != nil {
			if errors.Is(err, unix.ENOSPC) {
				return fmt.Errorf("磁盘空间不足，无法预留恢复回滚空间：%w", err)
			}
			return fmt.Errorf("当前文件系统不支持空间预留，写入回滚预留失败：%w", err)
		}
		remaining -= chunk
	}
	return nil
}
func restorePlatformSupported() bool { return true }
