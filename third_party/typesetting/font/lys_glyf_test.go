package font

import (
	"bytes"
	"testing"

	"github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype/tables"
)

func TestGlyfHeadersAndPointCountsMatchDecodedGlyphs(t *testing.T) {
	face, err := ParseTTF(bytes.NewReader(lysDejaVuSans(t)))
	if err != nil {
		t.Fatalf("parse DejaVu Sans: %v", err)
	}
	glyf := face.glyf
	if glyf.Len() != face.nGlyphs {
		t.Fatalf("glyf has %d glyphs, maxp %d", glyf.Len(), face.nGlyphs)
	}
	composites := 0
	for gid := range tables.GlyphID(glyf.Len()) {
		glyph, err := glyf.Glyph(gid)
		if err != nil {
			t.Fatalf("glyph %d: %v", gid, err)
		}
		if _, ok := glyph.Data.(tables.CompositeGlyph); ok {
			composites++
		}
		header, err := glyf.Header(gid)
		if err != nil || header != glyph.GlyphHeader {
			t.Fatalf("glyph %d header %+v, %v; decoded %+v", gid, header, err, glyph.GlyphHeader)
		}
		count, err := glyf.PointNumbersCount(gid)
		if err != nil || count != glyph.PointNumbersCount() {
			t.Fatalf("glyph %d point count %d, %v; decoded %d", gid, count, err, glyph.PointNumbersCount())
		}
	}
	if composites == 0 {
		t.Fatal("no composite glyph exercised")
	}

	gid, ok := face.NominalGlyph('A')
	if !ok {
		t.Fatal("no glyph for A")
	}
	outline, ok := face.GlyphDataOutline(gid)
	if !ok || len(outline.Segments) == 0 {
		t.Fatalf("A has no outline: %v", ok)
	}
	extents, ok := face.GlyphExtents(gid)
	if !ok || extents.Width <= 0 || extents.Height >= 0 {
		t.Fatalf("A extents %+v, %v", extents, ok)
	}
}
