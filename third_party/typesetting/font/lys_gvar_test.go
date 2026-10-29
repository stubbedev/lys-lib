package font

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"

	ot "github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype"
	"github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype/tables"
)

func TestVariedGlyphsMatchEagerGvarDecode(t *testing.T) {
	face, err := ParseTTF(bytes.NewReader(lysQuicksand(t)))
	if err != nil {
		t.Fatalf("parse Quicksand: %v", err)
	}
	if face.gvar.table.Len() != face.nGlyphs {
		t.Fatalf("gvar has %d glyphs, maxp %d", face.gvar.table.Len(), face.nGlyphs)
	}
	for _, tc := range []struct {
		wght   float32
		golden string
	}{
		{300, "164002cf865f26b6e776de8e696cc0c38770a6ab389c181b4622609a7bf06492"},
		{580, "4101892434348b0eb029f058920d60f37d3a4e94b757a910fb28dc5de53566a0"},
		{700, "ce99b83eb3e8497385ad77df01772dd4b457e2d9f81c45afe3d7d04149c39574"},
	} {
		face.SetVariations([]Variation{{Tag: ot.MustNewTag("wght"), Value: tc.wght}})
		h := sha256.New()
		for gid := range GID(face.nGlyphs) {
			outline, ok := face.GlyphDataOutline(gid)
			extents, extentsOK := face.GlyphExtents(gid)
			fmt.Fprint(h, gid, outline.Segments, ok, extents, extentsOK, face.HorizontalAdvance(gid))
		}
		if got := fmt.Sprintf("%x", h.Sum(nil)); got != tc.golden {
			t.Errorf("wght %v: outlines, extents and advances hash to %s, want %s from the eager gvar decode", tc.wght, got, tc.golden)
		}
	}
}

func TestGvarRejectsPointNumbersPastTheGlyph(t *testing.T) {
	const privatePointNumbers = 0x2000
	header, _, err := tables.ParseTupleVariationHeader([]byte{0, 6, privatePointNumbers >> 8, 0}, 1)
	if err != nil {
		t.Fatalf("parse tuple header: %v", err)
	}
	serialized := []byte{1, 0, 9, 1, 1, 1}
	tvs := []tupleVariation{{TupleVariationHeader: header}}

	err = parseGlyphVariationSerializedData(serialized, false, 5, tvs)
	if err == nil || !strings.Contains(err.Error(), "point number 9 for 5 points") {
		t.Fatalf("point 9 of a 5-point glyph: err = %v", err)
	}
	if err := parseGlyphVariationSerializedData(serialized, false, 10, tvs); err != nil {
		t.Fatalf("point 9 of a 10-point glyph: %v", err)
	}
	if !slices.Equal(tvs[0].pointNumbers, []uint16{9}) || !slices.Equal(tvs[0].deltas, []int16{1, 1}) {
		t.Fatalf("decoded points %v, deltas %v", tvs[0].pointNumbers, tvs[0].deltas)
	}
}
