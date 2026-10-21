package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Check using the runtime user's actual access, including NAS ACLs. Opening
// existing files never truncates them; the directory probe is removed again.
func checkStartupPermissions(data, output string) error {
	for _, target := range []struct {
		path string
		mode os.FileMode
	}{{data, 0700}, {output, 0755}} {
		dir := target.path
		if err := os.MkdirAll(dir, target.mode); err != nil {
			return startupPermissionError(dir, err)
		}
		f, err := os.CreateTemp(dir, ".yolink-permission-*")
		if err != nil {
			return startupPermissionError(dir, err)
		}
		closeErr := f.Close()
		removeErr := os.Remove(f.Name())
		if closeErr != nil {
			return startupPermissionError(dir, closeErr)
		}
		if removeErr != nil {
			return startupPermissionError(dir, removeErr)
		}
	}
	for _, name := range []string{".yolink.lock", "strmlink.db", "strmlink.db-wal", "strmlink.db-shm", "credentials.key", "playback.key", "events.key", "admin-credential.json", "initial-admin-password.txt"} {
		file := filepath.Join(data, name)
		info, err := os.Lstat(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return startupPermissionError(file, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("数据文件 %s 不是普通文件，请检查挂载", file)
		}
		flag := os.O_RDONLY
		if name == ".yolink.lock" || name == "strmlink.db" || name == "strmlink.db-wal" || name == "strmlink.db-shm" {
			flag = os.O_RDWR
		}
		f, err := os.OpenFile(file, flag, 0)
		if err != nil {
			return startupPermissionError(file, err)
		}
		if err := f.Close(); err != nil {
			return startupPermissionError(file, err)
		}
	}
	return nil
}

func startupPermissionError(name string, err error) error {
	return fmt.Errorf("运行用户无法读写 %s，请检查宿主机挂载目录及已有数据文件的属主/ACL（镜像默认 UID:GID 10001:10001）；安装和升级步骤见 QUICKSTART.md：%w", name, err)
}
