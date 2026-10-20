//go:build !linux

package main

import (
	"errors"
	"os"
)

func restoreDirectorySync(*os.File) error {
	return errors.New("当前平台不支持恢复目录持久化")
}
func allocateRestoreReserve(*os.File, int64) error {
	return errors.New("当前平台不支持恢复空间预留")
}
func restoreSameDevice(*os.Root, string) error {
	return errors.New("当前平台尚未验证恢复激活")
}
func restorePlatformSupported() bool { return false }
