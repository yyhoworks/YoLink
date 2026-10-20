//go:build windows

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Windows export claims cover process termination, not power-loss durability.
// File content is flushed before publication; directory fsync is not promised.
func temporaryDirectorySync(*os.File) error { return nil }
func temporarySameDevice(root *os.Root, name string) error {
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
	var as, bs windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(windows.Handle(a.Fd()), &as); err != nil {
		return err
	}
	if err = windows.GetFileInformationByHandle(windows.Handle(b.Fd()), &bs); err != nil {
		return err
	}
	if as.VolumeSerialNumber != bs.VolumeSerialNumber || bs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("临时资料路径跨卷或包含重解析点")
	}
	return nil
}
