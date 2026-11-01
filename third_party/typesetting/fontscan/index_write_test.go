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

func TestAnEmptyFontFileDoesNotStopAScan(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "empty.ttf"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	index, _, err := scanFontFootprints(discardLogger{}, nil, dir)
	if err != nil || len(index) != 1 || len(index[0].footprints) != 0 {
		t.Errorf("index %v, %v; want the file listed with no footprints", index, err)
	}
}

func TestAMappedFontScansAndLoadsAsItsBytes(t *testing.T) {
	path := filepath.Join("..", "..", "..", "render", "testdata", "Cantarell-Regular.ttf")
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	font := filepath.Join(dir, "c.ttf")
	if err := os.WriteFile(font, data, 0o600); err != nil {
		t.Fatal(err)
	}
	index, _, err := scanFontFootprints(discardLogger{}, nil, dir)
	if err != nil || len(index) != 1 || len(index[0].footprints) != 1 {
		t.Fatalf("index %v, %v", index, err)
	}
	fp := index[0].footprints[0]
	face, err := fp.loadFromDisk()
	if err != nil {
		t.Fatal(err)
	}
	gid, ok := face.NominalGlyph('a')
	if !ok {
		t.Fatal("the loaded face has no glyph for a")
	}
	if adv := face.HorizontalAdvance(gid); adv <= 0 {
		t.Errorf("a glyph read through the mapping has advance %v", adv)
	}
}

// A directory linked into a font directory, as a Nix profile links each
// package's fonts, is scanned as a directory and does not stop the scan;
// a link back to a parent ends rather than walking forever.
func TestALinkedDirectoryIsWalked(t *testing.T) {
	dir, linked := t.TempDir(), t.TempDir()
	font := filepath.Join(linked, "a.ttf")
	if err := os.WriteFile(font, []byte("not a font"), 0o600); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"package": linked, "loop": dir} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	index, _, err := scanFontFootprints(discardLogger{}, nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(index) != 1 {
		t.Fatalf("%d files indexed through the links, want the 1 font: %+v", len(index), index)
	}
}
