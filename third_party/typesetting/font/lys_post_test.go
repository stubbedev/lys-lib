package font

import (
	"bytes"
	"testing"

	"github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype/tables"
)

func TestPostNamesDecodeOnLookup(t *testing.T) {
	names := newPostNames20(tables.PostNames20{
		GlyphNameIndexes: []uint16{0, 258, 259, 260, 40000, 36},
		Strings:          []byte{3, 'f', 'o', 'o', 3, 'b', 'a', 'r', 5, 'x'},
	})
	for gid, want := range []string{".notdef", "foo", "bar", "", "", "A", ""} {
		if got := names.glyphName(GID(gid)); got != want {
			t.Errorf("glyph %d named %q, want %q", gid, got, want)
		}
	}
}

func TestPostNamesOfARealFont(t *testing.T) {
	face, err := ParseTTF(bytes.NewReader(lysDejaVuSans(t)))
	if err != nil {
		t.Fatalf("parse DejaVu Sans: %v", err)
	}
	for r, want := range map[rune]string{'A': "A", 'Ā': "Amacron", 'Ж': "uni0416"} {
		gid, ok := face.NominalGlyph(r)
		if !ok {
			t.Fatalf("no glyph for %q", r)
		}
		if got := face.GlyphName(gid); got != want {
			t.Errorf("%q named %q, want %q", r, got, want)
		}
	}
}
