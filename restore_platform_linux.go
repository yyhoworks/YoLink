//go:build linux

package main

import (
	"errors"
	"golang.org/x/sys/unix"
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
		return err
	}
	return f.Sync()
}
func restorePlatformSupported() bool { return true }
