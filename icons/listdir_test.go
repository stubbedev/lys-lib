package icons

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestListDirMatchesTheStandardLibrary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.png"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file.png", filepath.Join(dir, "link.png")); err != nil {
		t.Fatal(err)
	}
	for i := range 3000 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("many-%04d-with-a-long-name-to-fill-a-read.svg", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := listDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(got, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	want, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("listed %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name() != want[i].Name() || got[i].Type() != want[i].Type() || got[i].IsDir() != want[i].IsDir() {
			t.Errorf("entry %d = %s %v, want %s %v", i, got[i].Name(), got[i].Type(), want[i].Name(), want[i].Type())
		}
	}
}

func TestListDirErrorsReadLikeTheStandardLibrarys(t *testing.T) {
	dir := t.TempDir()
	if _, err := listDir(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing directory: %v", err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listDir(file); !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("a file: %v", err)
	}
}

func TestADirentOfUnknownTypeIsStatted(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("nowhere", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	rec := make([]byte, 32)
	rec[direntReclenAt] = 32
	copy(rec[direntNameAt:], "link")
	got, err := appendDirents(nil, dir, rec)
	if err != nil || len(got) != 1 || got[0].Type() != fs.ModeSymlink {
		t.Errorf("entries %v, %v; want the symlink an lstat finds", got, err)
	}
}

func TestATruncatedDirentIsAnError(t *testing.T) {
	if _, err := appendDirents(nil, "/d", make([]byte, 8)); err == nil {
		t.Error("a short record was accepted")
	}
	rec := make([]byte, 24)
	rec[direntReclenAt] = 200
	if _, err := appendDirents(nil, "/d", rec); err == nil {
		t.Error("a record longer than the read was accepted")
	}
}
