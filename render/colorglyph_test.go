package render

import (
	"bytes"
	"os"
	"testing"
)

// loadColor loads a color test face from testdata/color.
func loadColor(t *testing.T, name string) *Typeface {
	t.Helper()
	data, err := os.ReadFile("testdata/color/" + name)
	if err != nil {
		t.Fatal(err)
	}
	tf, err := LoadFont(data)
	if err != nil {
		t.Fatal(err)
	}
	return tf
}

// colorCount counts the distinct opaque colors s paints in f.
func colorCount(f *Typeface, s string) int {
	const w, h = 200, 80
	buf := make([]byte, Stride(w)*h)
	cv := New(buf, Stride(w), w, h)
	f.Draw(cv, f.Shape(s, 48), 8, 60, RGB(255, 255, 255))
	seen := map[[3]byte]bool{}
	for i := 0; i < len(buf); i += 4 {
		if buf[i+3] == 255 {
			seen[[3]byte{buf[i], buf[i+1], buf[i+2]}] = true
		}
	}
	return len(seen)
}

// The color sources win over the white text color: a COLRv1, an
// OT-SVG, and a COLRv0 glyph each paint several colors, and the same
// smiley paints alike through COLR and SVG.
func TestColorGlyphsPaintColor(t *testing.T) {
	for _, tc := range []struct{ font, text string }{
		{"twemoji_smiley-glyf_colr_1.ttf", "\U0001F601"},
		{"twemoji_smiley-picosvg.ttf", "\U0001F601"},
		{"BungeeColor-Regular.ttf", "B"},
	} {
		if n := colorCount(loadColor(t, tc.font), tc.text); n < 3 {
			t.Errorf("%s paints %d colors, want a color glyph", tc.font, n)
		}
	}
}

// The foreground palette entry and an SVG's currentColor follow the
// text color; everything else keeps the font's colors.
func TestColorGlyphForeground(t *testing.T) {
	if hexColor(RGB(0x12, 0x34, 0x56)) != "#123456ff" || hexColor(RGBA(0x40, 0, 0, 0x80)) != "#40000080" {
		t.Errorf("hexColor = %s %s", hexColor(RGB(0x12, 0x34, 0x56)), hexColor(RGBA(0x40, 0, 0, 0x80)))
	}
	p := &colrPainter{fg: RGB(0, 0, 255)}
	if c := p.color(0xFFFF, 1); c != (rgba{0, 0, 1, 1}) {
		t.Errorf("foreground entry = %v", c)
	}
}

// TestGoldenColorGlyphs pins the color paths: the smiley through COLRv1,
// OT-SVG and the sbix bitmap path side by side, a run of COLRv1 test
// glyphs (gradients, transforms, composites), and COLRv0 text.
func TestGoldenColorGlyphs(t *testing.T) {
	smileys := []*Typeface{loadColor(t, "twemoji_smiley-glyf_colr_1.ttf"), loadColor(t, "twemoji_smiley-picosvg.ttf"), loadColor(t, "twemoji_smiley-sbix.ttf")}
	tests := loadColor(t, "test_glyphs-glyf_colr_1.ttf")
	bungee := loadColor(t, "BungeeColor-Regular.ttf")
	goldenCanvas(t, "color-glyphs", 420, 200, func(cv *Canvas) {
		for i, f := range smileys {
			f.Draw(cv, f.Shape("\U0001F601", 40), 8+i*56, 48, RGB(255, 255, 255))
		}
		run := []rune{0xF0100, 0xF0101, 0xF0102, 0xF0103, 0xF0200, 0xF0201, 0xF0300, 0xF0400, 0xF0500, 0xF0600, 0xF0700, 0xF0800}
		tests.Draw(cv, tests.Shape(string(run), 32), 8, 110, RGB(255, 255, 255))
		bungee.Draw(cv, bungee.Shape("Bungee", 40), 8, 180, RGB(255, 255, 255))
	})
}

// A shared OT-SVG document cuts down to the glyph's element and the
// definitions; a document without the glyph's id stays whole.
func TestSVGGlyphElement(t *testing.T) {
	doc := `<svg xmlns="http://www.w3.org/2000/svg"><defs><path id="p" d="M0 0"/></defs><g id="glyph2"><use href="#p"/></g><g id="glyph3"><path d="M1 1"/></g></svg>`
	got := string(svgGlyphElement([]byte(doc), 2))
	want := `<svg xmlns="http://www.w3.org/2000/svg"><defs><path id="p" d="M0 0"/></defs><g id="glyph2"><use href="#p"/></g></svg>`
	if got != want {
		t.Errorf("cut =\n%s\nwant\n%s", got, want)
	}
	if string(svgGlyphElement([]byte(doc), 9)) != doc {
		t.Error("an absent glyph id cut the document")
	}
}

func inkBox(t *testing.T, f *Typeface, s string, px float64, penX, penY int) Rect {
	t.Helper()
	const w, h = 256, 256
	buf := make([]byte, Stride(w)*h)
	cv := New(buf, Stride(w), w, h)
	f.Draw(cv, f.Shape(s, px), penX, penY, RGB(255, 255, 255))
	x0, y0, x1, y1 := w, h, 0, 0
	for y := range h {
		for x := range w {
			if buf[(y*w+x)*4+3] != 0 {
				x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x+1), max(y1, y+1)
			}
		}
	}
	if x1 == 0 {
		t.Fatalf("%q paints nothing", s)
	}
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

func TestABitmapGlyphSitsWhereItsOutlineTwinDoes(t *testing.T) {
	const smiley, px, penX, penY = "\U0001F601", 96, 32, 160
	bitmap := inkBox(t, loadColor(t, "twemoji_smiley-sbix.ttf"), smiley, px, penX, penY)
	outline := inkBox(t, loadColor(t, "twemoji_smiley-glyf_colr_1.ttf"), smiley, px, penX, penY)
	const tolerance = 2
	if d := (bitmap.Y + bitmap.H/2) - (outline.Y + outline.H/2); d < -tolerance || d > tolerance {
		t.Errorf("the bitmap smiley %v sits %dpx from its COLR twin %v", bitmap, d, outline)
	}
	if d := (bitmap.X + bitmap.W/2) - (outline.X + outline.W/2); d < -tolerance || d > tolerance {
		t.Errorf("the bitmap smiley %v sits %dpx across from its COLR twin %v", bitmap, d, outline)
	}
}

func TestABitmapGlyphWithoutBearingsKeepsItsBottomOnTheBaseline(t *testing.T) {
	data, err := os.ReadFile("testdata/color/twemoji_smiley-sbix.ttf")
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte("png ")
	for at := 0; ; {
		i := bytes.Index(data[at:], marker)
		if i < 0 {
			break
		}
		clear(data[at+i-4 : at+i])
		at += i + len(marker)
	}
	f, err := LoadFont(data)
	if err != nil {
		t.Fatal(err)
	}
	gid, ok := f.face.NominalGlyph('\U0001F601')
	if !ok {
		t.Fatal("the sbix fixture has no smiley")
	}
	const penX, penY = 32, 160
	_, at, ok := bitmapPlacement(f, gid, 96/f.upem, penX, penY)
	if !ok {
		t.Fatal("no placement for the smiley")
	}
	if at.X != penX || at.Y+at.H != penY {
		t.Errorf("placement %v, want its bottom-left corner on the pen (%d, %d)", at, penX, penY)
	}
}
