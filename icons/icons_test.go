package icons

import (
	"bytes"
	"compress/gzip"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/render"
)

// The fixture tree, laid out per the icon theme spec. Every flag glyph
// carries a distinct fill so tests can pin which file a lookup matched.
//
//	root/
//	  plainflag.svg                        unthemed fallback
//	  testtheme/ (Inherits=parent)
//	    16x16/apps/flag.png   16 Fixed     red
//	    24x24/apps/flag.svg   24 Fixed     green
//	    32x32/apps/flag.svg   32 Fixed     blue
//	    scalable/apps/flag.svg  Scalable 12..48  yellow
//	    64x64/apps/flag.svg   64 Fixed     magenta
//	    16x16@2/apps/flag.svg 16 Scale=2   cyan
//	    24x24/apps/onlytheme.svg
//	  parent/ (Inherits=hicolor)
//	    20x20/apps/parenticon.svg  20 Fixed  #404040
//	  hicolor/
//	    16x16/apps/flag.svg  16 Threshold  white
//	    16x16/apps/hicoloronly.svg        #123456
//	    16x16/apps/plain.svg              #654321
//	    22x22/apps/threshold.svg  22 Threshold=4  #808080
//	    24x24/apps/parenticon.svg 24 Fixed  #202020
//	    symbolic/apps/face-symbolic.svg   currentColor
//	    scalable/apps/scal.svg  Scalable 16..48  #008080
//	    64x64/apps/scal.svg    64 Fixed   #800080
//	    16x16/apps/gzicon.svgz            #008000
func writeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel string, data []byte) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	theme := func(name, body string) {
		write(filepath.Join(name, "index.theme"), []byte(body))
	}
	theme("testtheme", `[Icon Theme]
Name=Test Theme
Inherits=parent
Directories=16x16/apps,24x24/apps,32x32/apps,scalable/apps,64x64/apps,16x16@2/apps

[16x16/apps]
Size=16
Type=Fixed

[24x24/apps]
Size=24
Type=Fixed

[32x32/apps]
Size=32
Type=Fixed

[scalable/apps]
Size=24
Type=Scalable
MinSize=12
MaxSize=48

[64x64/apps]
Size=64
Type=Fixed

[16x16@2/apps]
Size=16
Scale=2
`)
	theme("parent", `[Icon Theme]
Name=Parent
Inherits=hicolor
Directories=20x20/apps

[20x20/apps]
Size=20
Type=Fixed
`)
	theme("hicolor", `[Icon Theme]
Name=Hicolor
Directories=16x16/apps,22x22/apps,symbolic/apps,scalable/apps,64x64/apps,24x24/apps

[16x16/apps]
Size=16

[22x22/apps]
Size=22
Threshold=4

[symbolic/apps]
Size=16

[scalable/apps]
Size=16
Type=Scalable
MinSize=16
MaxSize=48

[64x64/apps]
Size=64
Type=Fixed

[24x24/apps]
Size=24
Type=Fixed
`)

	svg := func(fill string) []byte {
		return []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">` +
			`<rect width="16" height="16" fill="` + fill + `"/></svg>`)
	}
	write("testtheme/16x16/apps/flag.png", pngDoc(t, 0xff, 0, 0))
	write("testtheme/24x24/apps/flag.svg", svg("#00ff00"))
	write("testtheme/32x32/apps/flag.svg", svg("#0000ff"))
	write("testtheme/scalable/apps/flag.svg", svg("#ffff00"))
	write("testtheme/64x64/apps/flag.svg", svg("#ff00ff"))
	write("testtheme/16x16@2/apps/flag.svg", svg("#00ffff"))
	write("testtheme/24x24/apps/onlytheme.svg", svg("#000001"))
	write("parent/20x20/apps/parenticon.svg", svg("#404040"))
	write("hicolor/16x16/apps/flag.svg", svg("#ffffff"))
	write("hicolor/16x16/apps/hicoloronly.svg", svg("#123456"))
	write("hicolor/16x16/apps/plain.svg", svg("#654321"))
	write("hicolor/16x16/apps/threshold.svg", svg("#161616"))
	write("hicolor/22x22/apps/threshold.svg", svg("#808080"))
	write("hicolor/24x24/apps/parenticon.svg", svg("#202020"))
	write("hicolor/symbolic/apps/face-symbolic.svg", []byte(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">`+
			`<rect width="16" height="16" fill="currentColor"/></svg>`))
	write("hicolor/scalable/apps/scal.svg", svg("#008080"))
	write("hicolor/64x64/apps/scal.svg", svg("#800080"))
	write("hicolor/16x16/apps/gzicon.svgz", gzDoc(t, svg("#008000")))
	write("plainflag.svg", svg("#000002"))
	return root
}

// newTestCache returns a cache over the fixture tree; the cache is
// discarded with the test, so nothing leaks into other tests or the
// process-wide Default.
func newTestCache(t *testing.T, theme string) *Cache {
	t.Helper()
	return newTestCacheAt(t, theme, writeTree(t))
}

func newTestCacheAt(t *testing.T, theme, root string) *Cache {
	t.Helper()
	c := New(theme)
	c.SetSearchPaths([]string{root})
	return c
}

func pngDoc(t *testing.T, r, g, b uint8) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := range 16 {
		for x := range 16 {
			img.Set(x, y, color.NRGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gzDoc(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// mustLookup resolves through c and returns the matched path.
func mustLookup(t *testing.T, c *Cache, name string, size int, frac uint32) string {
	t.Helper()
	path, err := c.Lookup(name, size, frac)
	if err != nil {
		t.Fatalf("Lookup(%q, %d, %d): %v", name, size, frac, err)
	}
	return path
}

func wantDir(t *testing.T, path, want string) {
	t.Helper()
	if !strings.Contains(filepath.ToSlash(path), want+"/") {
		t.Errorf("resolved %s, want it under %s", path, want)
	}
}

// centerColor reads the center pixel of an icon as a color.
func centerColor(ic *render.Icon) render.Color {
	w, h := ic.Size()
	r, g, b, a := ic.At(w/2, h/2).RGBA()
	return render.Color(a>>8<<24 | r>>8<<16 | g>>8<<8 | b>>8)
}

func TestLookupSizes(t *testing.T) {
	c := newTestCache(t, "testtheme")

	t.Run("exact size", func(t *testing.T) {
		wantDir(t, mustLookup(t, c, "flag", 24, 120), "testtheme/24x24/apps")
	})

	t.Run("scalable MinSize and MaxSize bound the exact match", func(t *testing.T) {
		// The scalable rule accepts everything from 12 to 48, including
		// sizes no Fixed rule holds.
		wantDir(t, mustLookup(t, c, "flag", 12, 120), "testtheme/scalable/apps")
		wantDir(t, mustLookup(t, c, "flag", 26, 120), "testtheme/scalable/apps")
		wantDir(t, mustLookup(t, c, "flag", 48, 120), "testtheme/scalable/apps")
	})

	t.Run("nearest size wins when no rule accepts", func(t *testing.T) {
		// 58: scalable clamps to 10, the 32 fixed dir to 26, the 64
		// fixed dir to 6 - nearest wins.
		wantDir(t, mustLookup(t, c, "flag", 58, 120), "testtheme/64x64/apps")
		// 50: scalable clamps to 2, nearer than the 64 fixed dir's 14.
		wantDir(t, mustLookup(t, c, "flag", 50, 120), "testtheme/scalable/apps")
	})

	t.Run("threshold boundaries are inclusive on both ends", func(t *testing.T) {
		hc := newTestCache(t, "hicolor")
		// 16x16 is Threshold by default: 14..18.
		wantDir(t, mustLookup(t, hc, "threshold", 17, 120), "hicolor/16x16/apps")
		// 18 accepts both; the earlier directory in the index wins.
		wantDir(t, mustLookup(t, hc, "threshold", 18, 120), "hicolor/16x16/apps")
		// 19 is only inside 22 ± 4.
		wantDir(t, mustLookup(t, hc, "threshold", 19, 120), "hicolor/22x22/apps")
		wantDir(t, mustLookup(t, hc, "threshold", 26, 120), "hicolor/22x22/apps")
		// 27 matches nothing; the nearest pass still serves the icon.
		wantDir(t, mustLookup(t, hc, "threshold", 27, 120), "hicolor/22x22/apps")
	})
}

func TestLookupChainOrder(t *testing.T) {
	c := newTestCache(t, "testtheme")

	t.Run("inherited theme serves its own icons", func(t *testing.T) {
		wantDir(t, mustLookup(t, c, "parenticon", 20, 120), "parent/20x20/apps")
	})

	t.Run("an inherited theme beats an exact hicolor match", func(t *testing.T) {
		// parent has parenticon at 20 (distance 1), hicolor at 24
		// (distance 0): the chain order must win over hicolor's
		// perfect size.
		wantDir(t, mustLookup(t, c, "parenticon", 21, 120), "parent/20x20/apps")
	})

	t.Run("hicolor fallback", func(t *testing.T) {
		wantDir(t, mustLookup(t, c, "hicoloronly", 16, 120), "hicolor/16x16/apps")
	})

	t.Run("missing icon errors with ErrNotFound", func(t *testing.T) {
		_, err := c.Lookup("no-such-icon", 16, 120)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("unthemed fallback finds the bare file", func(t *testing.T) {
		path := mustLookup(t, c, "plainflag", 16, 120)
		if !strings.HasSuffix(filepath.ToSlash(path), "plainflag.svg") {
			t.Errorf("resolved %s, want plainflag.svg", path)
		}
	})
}

func TestLookupScales(t *testing.T) {
	c := newTestCache(t, "testtheme")

	t.Run("a 2x lookup matches Scale=2 directories", func(t *testing.T) {
		wantDir(t, mustLookup(t, c, "flag", 16, 240), "testtheme/16x16@2/apps")
	})

	t.Run("scaled directories never satisfy a 1x lookup", func(t *testing.T) {
		wantDir(t, mustLookup(t, c, "flag", 16, 120), "testtheme/16x16/apps")
	})

	t.Run("themes without scaled directories fall back to 1x", func(t *testing.T) {
		// parent only ships scale-1 directories; the 2x request still
		// resolves there and rasterizes into the larger device box.
		wantDir(t, mustLookup(t, c, "parenticon", 20, 240), "parent/20x20/apps")
	})
}

func TestLookupSymbolic(t *testing.T) {
	hc := newTestCache(t, "hicolor")

	t.Run("a -symbolic name matches its -symbolic file", func(t *testing.T) {
		wantDir(t, mustLookup(t, hc, "face-symbolic", 16, 120), "hicolor/symbolic/apps")
	})

	t.Run("a -symbolic name falls back to the unsuffixed file", func(t *testing.T) {
		wantDir(t, mustLookup(t, hc, "plain-symbolic", 16, 120), "hicolor/16x16/apps")
	})
}

func TestCacheDecodesPerKey(t *testing.T) {
	c := newTestCache(t, "testtheme")

	t.Run("same key returns the shared raster", func(t *testing.T) {
		first, err := c.Icon("flag", 24, 120)
		if err != nil {
			t.Fatal(err)
		}
		again, err := c.Icon("flag", 24, 120)
		if err != nil {
			t.Fatal(err)
		}
		if first != again {
			t.Error("second Icon call for the same key decoded again")
		}
	})

	t.Run("the raster is the device box for the key's scale", func(t *testing.T) {
		one, err := c.Icon("flag", 16, 120)
		if err != nil {
			t.Fatal(err)
		}
		if w, h := one.Size(); w != 16 || h != 16 {
			t.Errorf("1x size = %dx%d, want 16x16", w, h)
		}
		two, err := c.Icon("flag", 16, 240)
		if err != nil {
			t.Fatal(err)
		}
		if two == one {
			t.Fatal("2x returned the 1x raster")
		}
		if w, h := two.Size(); w != 32 || h != 32 {
			t.Errorf("2x size = %dx%d, want 32x32", w, h)
		}
	})

	t.Run("decoded pixels come from the matched file", func(t *testing.T) {
		ic, err := c.Icon("flag", 24, 120)
		if err != nil {
			t.Fatal(err)
		}
		if got := centerColor(ic); got != render.RGB(0, 0xff, 0) {
			t.Errorf("24px flag center = %v, want green (the 24x24 file)", got)
		}
	})
}

func TestCacheInvalidation(t *testing.T) {
	root := writeTree(t)
	c := newTestCacheAt(t, "testtheme", root)
	before, err := c.Icon("flag", 24, 120)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("InvalidateTheme re-decodes", func(t *testing.T) {
		c.InvalidateTheme()
		after, err := c.Icon("flag", 24, 120)
		if err != nil {
			t.Fatal(err)
		}
		if after == before {
			t.Error("raster survived InvalidateTheme")
		}
	})

	t.Run("SetTheme bumps the generation and switches files", func(t *testing.T) {
		gen := c.Generation()
		c.SetTheme("hicolor")
		if c.Generation() != gen+1 {
			t.Errorf("generation = %d, want %d", c.Generation(), gen+1)
		}
		if c.Theme() != "hicolor" {
			t.Errorf("theme = %q, want hicolor", c.Theme())
		}
		ic, err := c.Icon("flag", 16, 120)
		if err != nil {
			t.Fatal(err)
		}
		if got := centerColor(ic); got != render.RGB(0xff, 0xff, 0xff) {
			t.Errorf("flag center = %v, want white (the hicolor file)", got)
		}
	})

	t.Run("sticking errors clear on invalidation", func(t *testing.T) {
		if _, err := c.Icon("no-such-icon", 16, 120); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		fresh := filepath.Join(root, "hicolor", "16x16", "apps", "no-such-icon.svg")
		svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">` +
			`<rect width="16" height="16" fill="#000010"/></svg>`)
		if err := os.WriteFile(fresh, svg, 0o600); err != nil {
			t.Fatal(err)
		}
		// The negative entry is still cached...
		if _, err := c.Icon("no-such-icon", 16, 120); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want the cached ErrNotFound", err)
		}
		// ...until a theme invalidation drops it.
		c.InvalidateTheme()
		ic, err := c.Icon("no-such-icon", 16, 120)
		if err != nil {
			t.Fatalf("err = %v, want the icon after invalidation", err)
		}
		if got := centerColor(ic); got != render.RGB(0, 0, 0x10) {
			t.Errorf("center = %v, want the new file's blue", got)
		}
	})
}

func TestCacheEviction(t *testing.T) {
	oldCap := maxEntries
	maxEntries = 2
	defer func() { maxEntries = oldCap }()

	// All three names resolve through one chain, giving three cache
	// entries against a capacity of two.
	c := newTestCache(t, "testtheme")
	first, err := c.Icon("onlytheme", 24, 120)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Icon("hicoloronly", 16, 120); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Icon("threshold", 22, 120); err != nil {
		t.Fatal(err)
	}
	again, err := c.Icon("onlytheme", 24, 120)
	if err != nil {
		t.Fatal(err)
	}
	if again == first {
		t.Error("evicted entry was served again without a re-decode")
	}
}

func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	oldCap := maxEntries
	maxEntries = 2
	defer func() { maxEntries = oldCap }()

	c := newTestCache(t, "testtheme")
	used, err := c.Icon("onlytheme", 24, 120)
	if err != nil {
		t.Fatal(err)
	}
	idle, err := c.Icon("hicoloronly", 16, 120)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Icon("onlytheme", 24, 120); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Icon("threshold", 22, 120); err != nil {
		t.Fatal(err)
	}
	if again, _ := c.Icon("onlytheme", 24, 120); again != used {
		t.Error("the recently used entry was evicted, though it was inserted first")
	}
	if again, _ := c.Icon("hicoloronly", 16, 120); again == idle {
		t.Error("the least recently used entry survived past the cap")
	}
}

func TestSymbolicRecoloring(t *testing.T) {
	hc := newTestCache(t, "hicolor")
	accent := render.RGB(0x89, 0xB4, 0xFA)

	t.Run("a symbolic source recolors to the tint", func(t *testing.T) {
		ic, err := hc.SymbolicIcon("face-symbolic", 16, 120, accent)
		if err != nil {
			t.Fatal(err)
		}
		if got := centerColor(ic); got != accent {
			t.Errorf("center = %v, want the accent", got)
		}
	})

	t.Run("currentColor decodes white without a tint", func(t *testing.T) {
		ic, err := hc.Icon("face-symbolic", 16, 120)
		if err != nil {
			t.Fatal(err)
		}
		if got := centerColor(ic); got != render.RGB(0xff, 0xff, 0xff) {
			t.Errorf("center = %v, want white", got)
		}
	})

	t.Run("a non-symbolic source keeps its colors", func(t *testing.T) {
		ic, err := hc.SymbolicIcon("threshold", 22, 120, accent)
		if err != nil {
			t.Fatal(err)
		}
		if got := centerColor(ic); got != render.RGB(0x80, 0x80, 0x80) {
			t.Errorf("center = %v, want the file's gray", got)
		}
	})

	t.Run("Tinted recolors whatever matched", func(t *testing.T) {
		ic, err := hc.Tinted("threshold", 22, 120, accent)
		if err != nil {
			t.Fatal(err)
		}
		if got := centerColor(ic); got != accent {
			t.Errorf("center = %v, want the accent", got)
		}
	})

	t.Run("switching tints re-derives from the cached decode", func(t *testing.T) {
		other := render.RGB(0xf5, 0xc2, 0x67)
		first, err := hc.SymbolicIcon("face-symbolic", 16, 120, accent)
		if err != nil {
			t.Fatal(err)
		}
		second, err := hc.SymbolicIcon("face-symbolic", 16, 120, other)
		if err != nil {
			t.Fatal(err)
		}
		if first == second {
			t.Fatal("different tints shared one raster")
		}
		if got := centerColor(second); got != other {
			t.Errorf("center = %v, want the second tint", got)
		}
		back, err := hc.SymbolicIcon("face-symbolic", 16, 120, accent)
		if err != nil {
			t.Fatal(err)
		}
		if got := centerColor(back); got != accent {
			t.Errorf("center = %v, want the first tint again", got)
		}
	})
}

func TestDecodeImage(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8">` +
		`<rect width="8" height="8" fill="currentColor"/></svg>`)

	t.Run("svgz is ungzipped by extension", func(t *testing.T) {
		ic, err := DecodeImage(gzDoc(t, svg), "icon.svgz", 8, 8)
		if err != nil {
			t.Fatal(err)
		}
		if w, h := ic.Size(); w != 8 || h != 8 {
			t.Errorf("size = %dx%d, want 8x8", w, h)
		}
		if got := centerColor(ic); got != render.RGB(0xff, 0xff, 0xff) {
			t.Errorf("center = %v, want white (currentColor)", got)
		}
	})

	t.Run("corrupt svgz errors", func(t *testing.T) {
		if _, err := DecodeImage([]byte("not gzip"), "icon.svgz", 8, 8); err == nil {
			t.Error("garbage svgz must error")
		}
	})

	t.Run("unknown extension sniffs png magic then falls back to svg", func(t *testing.T) {
		ic, err := DecodeImage(pngDoc(t, 0xff, 0, 0), "icon.bin", 8, 8)
		if err != nil {
			t.Fatal(err)
		}
		if got := centerColor(ic); got != render.RGB(0xff, 0, 0) {
			t.Errorf("center = %v, want the png's red", got)
		}
		if _, err := DecodeImage(svg, "icon.bin", 8, 8); err != nil {
			t.Errorf("svg without extension: %v", err)
		}
	})

	t.Run("currentColor rewrites keep opacity parseable", func(t *testing.T) {
		grad := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8">` +
			`<rect width="8" height="8" fill="currentColor" fill-opacity="0.5"/></svg>`)
		ic, err := DecodeImage(grad, "icon.svg", 8, 8)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, a := ic.At(4, 4).RGBA(); a == 0 {
			t.Error("opacity lost under the currentColor rewrite")
		}
	})
}

func TestAPlainNameFindsAThemesSymbolicIcon(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "icons", "Only", "symbolic", "status")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	index := "[Icon Theme]\nName=Only\nDirectories=symbolic/status\n\n[symbolic/status]\nSize=16\nMinSize=8\nMaxSize=512\nType=Scalable\n"
	if err := os.WriteFile(filepath.Join(root, "icons", "Only", "index.theme"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	both := []string{"only-symbolic.svg", "both.svg", "both-symbolic.svg"}
	for _, name := range both {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("<svg/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := New("Only")
	c.SetSearchPaths([]string{filepath.Join(root, "icons")})
	got, err := c.Lookup("only", 16, 120)
	if err != nil || filepath.Base(got) != "only-symbolic.svg" {
		t.Errorf("a plain name with only a symbolic icon = %q, %v", got, err)
	}
	if got, err := c.Lookup("both", 16, 120); err != nil || filepath.Base(got) != "both.svg" {
		t.Errorf("a plain name with both = %q, %v, want the plain icon", got, err)
	}
	if _, err := c.Lookup("neither", 16, 120); !errors.Is(err, ErrNotFound) {
		t.Errorf("a name with neither: %v", err)
	}
}

func TestIsSymbolic(t *testing.T) {
	plain := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"></svg>`)
	sym := []byte(`<svg fill="currentColor" xmlns="http://www.w3.org/2000/svg"></svg>`)

	t.Run("file stem", func(t *testing.T) {
		if !IsSymbolic("face-symbolic.svg", plain) {
			t.Error("-symbolic stem not detected")
		}
		if IsSymbolic("face.svg", plain) {
			t.Error("plain stem flagged symbolic")
		}
	})

	t.Run("svg content", func(t *testing.T) {
		if !IsSymbolic("face.svg", sym) {
			t.Error("currentColor not detected")
		}
		if IsSymbolic("face.svg", plain) {
			t.Error("plain svg flagged symbolic")
		}
	})
}

func TestDeviceBoxAndDirScale(t *testing.T) {
	for _, tc := range []struct {
		size     int
		frac     uint32
		wantBox  int
		wantRule int
	}{
		{size: 16, frac: 120, wantBox: 16, wantRule: 1},
		{size: 16, frac: 240, wantBox: 32, wantRule: 2},
		{size: 16, frac: 150, wantBox: 20, wantRule: 1}, // 1.25x rounds up
		{size: 16, frac: 0, wantBox: 16, wantRule: 1},   // 0 means unset: 1x
		{size: 16, frac: 60, wantBox: 8, wantRule: 1},   // half scale
	} {
		if got := DeviceBox(tc.size, tc.frac); got != tc.wantBox {
			t.Errorf("DeviceBox(%d, %d) = %d, want %d", tc.size, tc.frac, got, tc.wantBox)
		}
		if got := dirScale(tc.frac); got != tc.wantRule {
			t.Errorf("dirScale(%d) = %d, want %d", tc.frac, got, tc.wantRule)
		}
	}
}

func TestCacheResetListeners(t *testing.T) {
	root := writeTree(t)
	c := newTestCacheAt(t, "testtheme", root)
	resets := 0
	c.OnReset(func() { resets++ })
	c.OnReset(nil)

	steps := []struct {
		name string
		do   func()
		want int
	}{
		{"SetTheme to the current theme is a no-op", func() { c.SetTheme("testtheme") }, 0},
		{"SetTheme to another theme resets", func() { c.SetTheme("hicolor") }, 1},
		{"equal search paths are a no-op", func() { c.SetSearchPaths([]string{root}) }, 1},
		{"new search paths reset", func() { c.SetSearchPaths([]string{root, root + "/hicolor"}) }, 2},
		{"InvalidateTheme resets", c.InvalidateTheme, 3},
		{"an empty followed theme is a no-op", func() { c.ApplyIconTheme("") }, 3},
		{"a followed theme switch resets", func() { c.ApplyIconTheme("testtheme") }, 4},
	}
	for _, s := range steps {
		gen, before := c.Generation(), resets
		s.do()
		if resets != s.want {
			t.Errorf("%s: %d resets, want %d", s.name, resets, s.want)
		}
		if moved, reset := c.Generation() != gen, resets != before; moved != reset {
			t.Errorf("%s: generation moved = %v, listeners ran = %v", s.name, moved, reset)
		}
	}
}

func TestCacheSearchPaths(t *testing.T) {
	c := New("hicolor")
	if got, want := c.SearchPaths(), defaultSearchPaths(); !slices.Equal(got, want) {
		t.Errorf("unset paths = %v, want the XDG default %v", got, want)
	}

	root := writeTree(t)
	paths := []string{root}
	c.SetSearchPaths(paths)
	paths[0] = "/elsewhere"
	if got := c.SearchPaths(); !slices.Equal(got, []string{root}) {
		t.Errorf("paths = %v, want [%s]: the caller's slice was not copied", got, root)
	}
	if _, err := c.Lookup("hicoloronly", 16, 120); err != nil {
		t.Errorf("lookup under the set path: %v", err)
	}

	resets := 0
	c.OnReset(func() { resets++ })
	c.SetSearchPaths([]string{})
	if resets != 1 {
		t.Errorf("empty paths after [%s]: %d resets, want 1", root, resets)
	}
	if _, err := c.Lookup("hicoloronly", 16, 120); !errors.Is(err, ErrNotFound) {
		t.Errorf("lookup with no search paths: err = %v, want ErrNotFound", err)
	}
	c.SetSearchPaths(nil)
	if resets != 2 {
		t.Errorf("nil after empty paths: %d resets, want 2 (nil is the default, not empty)", resets)
	}
	if got, want := c.SearchPaths(), defaultSearchPaths(); !slices.Equal(got, want) {
		t.Errorf("nil paths = %v, want the XDG default %v", got, want)
	}
}

// Lookups answer from each directory's listing, read once: a linked
// icon resolves through its link and a dangling link does not, and an
// icon installed after a lookup shows once the theme is invalidated,
// as an installed icon theme announces itself.
func TestLookupIndexesDirectories(t *testing.T) {
	root := writeTree(t)
	c := newTestCacheAt(t, "testtheme", root)
	apps := filepath.Join(root, "hicolor", "16x16", "apps")
	if err := os.Symlink("plain.svg", filepath.Join(apps, "linked.svg")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing.svg", filepath.Join(apps, "dangling.svg")); err != nil {
		t.Fatal(err)
	}
	if path, err := c.Lookup("linked", 16, 120); err != nil || filepath.Base(path) != "linked.svg" {
		t.Errorf("linked icon: %q, %v", path, err)
	}
	if _, err := c.Lookup("dangling", 16, 120); err == nil {
		t.Error("a dangling link resolved")
	}
	if _, err := c.Lookup("late", 16, 120); err == nil {
		t.Fatal("an icon resolved before it was installed")
	}
	plain, err := os.ReadFile(filepath.Join(apps, "plain.svg"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apps, "late.svg"), plain, 0o600); err != nil {
		t.Fatal(err)
	}
	c.InvalidateTheme()
	if path, err := c.Lookup("late", 16, 120); err != nil || filepath.Base(path) != "late.svg" {
		t.Errorf("installed icon after InvalidateTheme: %q, %v", path, err)
	}
}
