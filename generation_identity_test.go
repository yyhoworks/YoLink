package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerationKeepsMigratedIdentityAndFileBytes(t *testing.T) {
	f := setup(t)
	f.c.dirs = map[string][]Entry{"root": {{ID: "movie", Name: "movie.mkv", Size: 123}}}
	old := Media{ID: f.s.mediaID(f.a, "legacy:/media/movie.mkv"), Account: f.a.ID, RemoteID: "movie", Name: "movie.mkv", Path: "/media/movie.mkv", Size: 123, Legacy: true}
	f.s.saveMedia(old)
	content := []byte(mediaContent(f.s, old, f.s.setting("public_base", "")))
	if _, err := f.e.publish("test/media/movie.strm", old.ID, content); err != nil {
		t.Fatal(err)
	}
	preview := generate(t, f, true, 0)
	if preview.PreviewSame != 1 || preview.PreviewConflicts != 0 {
		t.Fatal(preview)
	}
	actual := generate(t, f, false, 0)
	if actual.CreatedFiles != 0 || actual.Skipped != 1 || actual.Errors != 0 {
		t.Fatal(actual)
	}
	file := filepath.Join(f.output, "test/media/movie.strm")
	got, _ := os.ReadFile(file)
	if string(got) != string(content) {
		t.Fatal("migrated STRM bytes changed")
	}
	var owner string
	_, _, key, _ := f.e.outputLocation("test/media/movie.strm")
	if err := f.s.db.QueryRow("SELECT media FROM outputs WHERE path=?", key).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != old.ID {
		t.Fatal("migrated output identity changed")
	}
	stored, _ := f.s.media(old.ID)
	if !stored.Legacy || stored.RemoteID != "movie" {
		t.Fatal("migration identity lost", stored)
	}
}

func TestMigratedIdentityDoesNotPermitUnrelatedOrModifiedOutput(t *testing.T) {
	for _, kind := range []string{"account", "path", "remote", "tampered"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			f.c.dirs = map[string][]Entry{"root": {{ID: "movie", Name: "movie.mkv"}}}
			old := Media{ID: "legacy-owned", Account: f.a.ID, RemoteID: "movie", Name: "movie.mkv", Path: "/media/movie.mkv", Legacy: true}
			switch kind {
			case "account":
				old.Account = "other"
			case "path":
				old.Path = "/different/movie.mkv"
			case "remote":
				old.RemoteID = "different"
			}
			f.s.saveMedia(old)
			content := []byte(mediaContent(f.s, old, f.s.setting("public_base", "")))
			if _, err := f.e.publish("test/media/movie.strm", old.ID, content); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(f.output, "test/media/movie.strm")
			if kind == "tampered" {
				content = []byte("user-modified\n")
				os.WriteFile(file, content, 0644)
			}
			j := generate(t, f, false, 0)
			if j.Errors != 1 || j.Skipped != 0 || j.CreatedFiles != 0 {
				t.Fatal("unrelated or tampered output was adopted", j)
			}
			got, _ := os.ReadFile(file)
			if string(got) != string(content) {
				t.Fatal("protected file overwritten")
			}
		})
	}
}
