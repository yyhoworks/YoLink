//go:build linux

package main

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockDataFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
