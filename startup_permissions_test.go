package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupPermissionsPreserveExistingData(t *testing.T) {
	data, output := filepath.Join(t.TempDir(), "data"), filepath.Join(t.TempDir(), "output")
	mustSucceed(t, checkStartupPermissions(data, output))
	want := []byte("fixture database bytes")
	mustSucceed(t, os.WriteFile(filepath.Join(data, "strmlink.db"), want, 0600))
	mustSucceed(t, checkStartupPermissions(data, output))
	got, err := os.ReadFile(filepath.Join(data, "strmlink.db"))
	if err != nil || string(got) != string(want) {
		t.Fatal("permission check changed existing data", err)
	}
	for _, dir := range []string{data, output} {
		entries, err := os.ReadDir(dir)
		mustSucceed(t, err)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".yolink-permission-") {
				t.Fatal("permission probe was not removed")
			}
		}
	}
}

func TestStartupPermissionsRejectInvalidMountAndDataFile(t *testing.T) {
	data, output := t.TempDir(), t.TempDir()
	mustSucceed(t, os.Mkdir(filepath.Join(data, "strmlink.db"), 0700))
	if err := checkStartupPermissions(data, output); err == nil {
		t.Fatal("directory accepted as database file")
	}
	file := filepath.Join(t.TempDir(), "output")
	mustSucceed(t, os.WriteFile(file, nil, 0600))
	if err := checkStartupPermissions(t.TempDir(), file); err == nil || !strings.Contains(err.Error(), "QUICKSTART.md") {
		t.Fatal("invalid mount did not give actionable error", err)
	}
}
