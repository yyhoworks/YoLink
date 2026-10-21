//go:build windows

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
)

func repairDirectoryID(root *os.Root) (string, error) {
	f, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer f.Close()
	var st windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &st); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d:%d", st.VolumeSerialNumber, st.FileIndexHigh, st.FileIndexLow), nil
}
