//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepairParentSymlinkSwapCannotRedirectPublication(t *testing.T) {
	f := strongScanFixture(t)
	generate(t, f, false, 0)
	s, _, _ := testToolsServer(t, f, f.c)
	_, _, key, _ := f.e.outputLocation("test/media/first.strm")
	os.Remove(filepath.Join(f.output, "test/media/first.strm"))
	p, err := s.previewChangeRepair(t.Context(), []string{key})
	if err != nil || !p.Items[0].Ready {
		t.Fatal(p, err)
	}
	original := filepath.Join(f.output, "test/media")
	moved := filepath.Join(f.output, "test/original-moved")
	other := filepath.Join(f.output, "other-unregistered")
	if err = os.Mkdir(other, 0755); err != nil {
		t.Fatal(err)
	}
	f.e.scanFault = func(point string) error {
		if point == "repair.after_intent" {
			if err = os.Rename(original, moved); err != nil {
				return err
			}
			return os.Symlink(other, original)
		}
		return nil
	}
	result, err := s.applyChangeRepair(t.Context(), p.ID, restoreRevision(p))
	if err != nil || result["conflicts"] != 1 {
		t.Fatal(result, err)
	}
	for _, directory := range []string{moved, other} {
		if _, err = os.Stat(filepath.Join(directory, "first.strm")); !os.IsNotExist(err) {
			t.Fatal("redirected repair effect", directory, err)
		}
	}
}
