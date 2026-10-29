package tables

import (
	"bytes"
	"slices"
	"testing"
)

var lysGvarVariedGlyph = []byte{
	0x00, 0x01, 0x00, 0x0a,
	0x00, 0x02, 0x80, 0x00, 0x40, 0x00,
	0x81, 0x81,
}

func lysGvarTable(offsets ...byte) []byte {
	header := []byte{
		0x00, 0x01, 0x00, 0x00,
		0x00, 0x01, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x02, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x1a,
	}
	return slices.Concat(header, offsets, lysGvarVariedGlyph)
}

func TestGvarDecodesGlyphVariationsOnDemand(t *testing.T) {
	gvar, _, err := ParseGvar(lysGvarTable(0, 0, 0, 0, 0, 6))
	if err != nil {
		t.Fatalf("ParseGvar: %v", err)
	}
	if gvar.Len() != 2 {
		t.Fatalf("Len = %d, want 2", gvar.Len())
	}

	unvaried, err := gvar.GlyphVariationData(0)
	if err != nil || len(unvaried.TupleVariationHeaders) != 0 || unvaried.SerializedData != nil {
		t.Fatalf("GlyphVariationData(0) = %+v, %v; want no variations", unvaried, err)
	}

	varied, err := gvar.GlyphVariationData(1)
	if err != nil {
		t.Fatalf("GlyphVariationData(1): %v", err)
	}
	if len(varied.TupleVariationHeaders) != 1 || !slices.Equal(varied.TupleVariationHeaders[0].PeakTuple.Values, []Coord{0x4000}) {
		t.Fatalf("GlyphVariationData(1) headers = %+v", varied.TupleVariationHeaders)
	}
	if !bytes.Equal(varied.SerializedData, []byte{0x81, 0x81}) {
		t.Fatalf("GlyphVariationData(1) serialized data = %x", varied.SerializedData)
	}

	if _, err := gvar.GlyphVariationData(2); err == nil {
		t.Fatal("GlyphVariationData(2) of a 2-glyph table: no error")
	}
}

func TestGvarRejectsBadOffsetsAtLoad(t *testing.T) {
	for _, tc := range []struct {
		name  string
		table []byte
	}{
		{"decreasing", lysGvarTable(0, 0, 0, 6, 0, 3)},
		{"past end", lysGvarTable(0, 0, 0, 0, 0, 7)},
	} {
		if _, _, err := ParseGvar(tc.table); err == nil {
			t.Errorf("%s offsets: ParseGvar gave no error", tc.name)
		}
	}
}
