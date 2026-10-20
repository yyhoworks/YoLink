//go:build !linux && !windows

package main

import (
	"errors"
	"os"
)

func lockDataFile(*os.File) error {
	return errors.New("当前平台尚不支持数据目录独占锁")
}
