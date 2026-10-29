package tables

import (
	"reflect"
	"testing"
)

var (
	lysSimpleGlyph    = []byte{0x0, 0x2, 0x0, 0xc, 0x0, 0x0, 0x4, 0x94, 0x5, 0x96, 0x0, 0x6, 0x0, 0xa, 0x0, 0x0, 0x41, 0x33, 0x1, 0x23, 0x1, 0x1, 0x23, 0x13, 0x21, 0x15, 0x21, 0x1, 0xf5, 0xb6, 0x1, 0xe9, 0xbd, 0xfe, 0x78, 0xfe, 0x78, 0xbb, 0xed, 0x2, 0xae, 0xfd, 0x52, 0x5, 0x96, 0xfa, 0x6a, 0x4, 0xa9, 0xfb, 0x57, 0x2, 0x2, 0xa2}
	lysCompositeGlyph = []byte{
		0xff, 0xff, 0x0, 0x1, 0x0, 0x2, 0x0, 0x3, 0x0, 0x4,
		0x0, 0x20, 0x0, 0x3, 0x0, 0x0,
		0x0, 0x0, 0x0, 0x2, 0x10, 0x20,
	}
)

func lysGlyf(t *testing.T) Glyf {
	t.Helper()
	data := append(append([]byte{}, lysSimpleGlyph...), lysCompositeGlyph...)
	simpleEnd := uint32(len(lysSimpleGlyph))
	g, err := ParseGlyf(data, []uint32{0, 0, simpleEnd, uint32(len(data))})
	if err != nil {
		t.Fatalf("ParseGlyf: %v", err)
	}
	return g
}

func TestGlyfDecodesGlyphsOnDemand(t *testing.T) {
	g := lysGlyf(t)
	if g.Len() != 3 {
		t.Fatalf("Len = %d, want 3", g.Len())
	}

	empty, err := g.Glyph(0)
	if err != nil || !reflect.DeepEqual(empty, Glyph{}) {
		t.Fatalf("Glyph(0) = %+v, %v; want the zero glyph", empty, err)
	}

	simple, err := g.Glyph(1)
	if err != nil {
		t.Fatalf("Glyph(1): %v", err)
	}
	if want := (GlyphHeader{numberOfContours: 2, XMin: 12, YMin: 0, XMax: 1172, YMax: 1430}); simple.GlyphHeader != want {
		t.Fatalf("Glyph(1) header = %+v, want %+v", simple.GlyphHeader, want)
	}
	data, ok := simple.Data.(SimpleGlyph)
	if !ok || !reflect.DeepEqual(data.EndPtsOfContours, []uint16{6, 10}) || len(data.Points) != 11 {
		t.Fatalf("Glyph(1) data = %+v", simple.Data)
	}

	composite, err := g.Glyph(2)
	if err != nil {
		t.Fatalf("Glyph(2): %v", err)
	}
	parts, ok := composite.Data.(CompositeGlyph)
	if !ok || len(parts.Glyphs) != 2 || parts.Glyphs[0].GlyphIndex != 3 || parts.Glyphs[1].GlyphIndex != 2 {
		t.Fatalf("Glyph(2) data = %+v", composite.Data)
	}
}

func TestGlyfHeaderMatchesTheDecodedGlyph(t *testing.T) {
	g := lysGlyf(t)
	for gid := range GlyphID(g.Len()) {
		glyph, err := g.Glyph(gid)
		if err != nil {
			t.Fatalf("Glyph(%d): %v", gid, err)
		}
		header, err := g.Header(gid)
		if err != nil || header != glyph.GlyphHeader {
			t.Errorf("Header(%d) = %+v, %v; want %+v", gid, header, err, glyph.GlyphHeader)
		}
	}
}

func TestGlyfRejectsBadOffsetsPerGlyph(t *testing.T) {
	g, err := ParseGlyf(lysSimpleGlyph, []uint32{0, uint32(len(lysSimpleGlyph)), 4, 1 << 20})
	if err != nil {
		t.Fatalf("ParseGlyf: %v", err)
	}
	if _, err := g.Glyph(0); err != nil {
		t.Errorf("Glyph(0): %v", err)
	}
	for _, gid := range []GlyphID{1, 2, 3} {
		if _, err := g.Glyph(gid); err == nil {
			t.Errorf("Glyph(%d) decoded despite its offsets", gid)
		}
		if _, err := g.Header(gid); err == nil {
			t.Errorf("Header(%d) decoded despite its offsets", gid)
		}
	}
	if _, err := ParseGlyf(nil, nil); err == nil {
		t.Error("ParseGlyf accepted an empty loca")
	}
	if n := (Glyf{}).Len(); n != 0 {
		t.Errorf("absent table Len = %d", n)
	}
}
