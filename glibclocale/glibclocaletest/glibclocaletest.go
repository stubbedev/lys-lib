// Package glibclocaletest holds glibc compiled locale data for tests, as
// glibc 2.44's localedef wrote it: the LC_IDENTIFICATION, LC_MONETARY,
// LC_NUMERIC and LC_TIME of de_DE.utf8, en_GB.utf8, en_US.utf8,
// fa_IR.utf8 and th_TH.utf8, as locale directories and as a
// locale-archive holding the same five locales.
package glibclocaletest

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

//go:embed data
var data embed.FS

// Names are the locales the data holds, sorted.
var Names = []string{"de_DE.utf8", "en_GB.utf8", "en_US.utf8", "fa_IR.utf8", "th_TH.utf8"}

// Dirs writes the locale directories into a temporary directory and
// returns it: a LOCPATH entry, or a stand-in for /usr/lib/locale.
func Dirs(t testing.TB) string {
	t.Helper()
	locales, err := fs.Sub(data, "data/locales")
	if err != nil {
		t.Fatalf("glibclocaletest: open the embedded locale directories: %v", err)
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, locales); err != nil {
		t.Fatalf("glibclocaletest: write the locale directories to %s: %v", dir, err)
	}
	return dir
}

// Archive writes the locale-archive into a temporary directory and
// returns its path.
func Archive(t testing.TB) string {
	t.Helper()
	raw, err := data.ReadFile("data/locale-archive")
	if err != nil {
		t.Fatalf("glibclocaletest: read the embedded locale-archive: %v", err)
	}
	path := filepath.Join(t.TempDir(), "locale-archive")
	if err := os.WriteFile(path, raw, 0o644); err != nil { //nolint:gosec // a test fixture
		t.Fatalf("glibclocaletest: write %s: %v", path, err)
	}
	return path
}
