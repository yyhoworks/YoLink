//go:build !linux && !windows

package main

import (
	"errors"
	"os"
)

func repairDirectoryID(*os.Root) (string, error) {
	return "", errors.New("当前平台尚未验证缺失文件修复")
}
