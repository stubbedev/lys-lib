package fontscan

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

func TestScanReportsWhetherTheIndexChanged(t *testing.T) {
	dir := t.TempDir()
	font := filepath.Join(dir, "a.ttf")
	if err := os.WriteFile(font, []byte("not a font"), 0o600); err != nil {
		t.Fatal(err)
	}
	scan := func(prev systemFontsIndex) (systemFontsIndex, bool) {
		t.Helper()
		index, unchanged, err := scanFontFootprints(discardLogger{}, prev, dir)
		if err != nil {
			t.Fatal(err)
		}
		return index, unchanged
	}
	first, unchanged := scan(nil)
	if unchanged || len(first) != 1 {
		t.Fatalf("first scan: unchanged %v, %d files", unchanged, len(first))
	}
	if _, unchanged := scan(first); !unchanged {
		t.Error("a scan that found every file up to date reported a change")
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(font, later, later); err != nil {
		t.Fatal(err)
	}
	touched, unchanged := scan(first)
	if unchanged {
		t.Error("a modified file reported no change")
	}
	if err := os.Remove(font); err != nil {
		t.Fatal(err)
	}
	if _, unchanged := scan(touched); unchanged {
		t.Error("a removed file reported no change")
	}
}

func TestTheIndexFileIsReplacedWhole(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.cache")
	want := systemFontsIndex{{path: "/a.ttf"}}
	if err := want.serializeToFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := deserializeIndexFile(path)
	if err != nil || len(got) != 1 || got[0].path != "/a.ttf" {
		t.Fatalf("read back %v, %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Errorf("directory holds %v, %v; want only the index", entries, err)
	}
}
