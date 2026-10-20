//go:build linux

package main

import "os"

func temporaryDirectorySync(f *os.File) error              { return f.Sync() }
func temporarySameDevice(root *os.Root, name string) error { return restoreSameDevice(root, name) }
