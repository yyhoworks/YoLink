//go:build !linux && !windows

package main

import (
	"errors"
	"os"
)

func temporaryDirectorySync(*os.File) error {
	return errors.New("当前平台尚未验证临时资料清理")
}
func temporarySameDevice(*os.Root, string) error {
	return errors.New("当前平台尚未验证临时资料清理")
}
