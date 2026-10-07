package render

import (
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
