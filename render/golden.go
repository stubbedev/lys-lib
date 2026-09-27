package render

// Test-fixture infrastructure shared by the golden-image harness (see
// internal/golden and the golden_test.go files in render and widget).
// The bundled font pins text metrics and rasterization to one face, so
// goldens are byte-stable on any machine — with or without fonts
// installed — instead of tracking whatever sysfont resolves on the
// host. It is deliberately small and permissively licensed; see
// testdata/README.md and testdata/LICENSE-Cantarell.txt.

import (
	_ "embed" // the fixture font bytes below
	"image"
)

//go:embed testdata/Cantarell-Regular.ttf
var fixtureFontTTF []byte

// FixtureFontData returns the bytes of the bundled Cantarell Regular
// fixture font (SIL Open Font License 1.1; the license text ships in
// testdata/LICENSE-Cantarell.txt). It is the single face every golden
// snapshot is shaped with.
func FixtureFontData() []byte { return fixtureFontTTF }

// NewFixtureTypeface parses the bundled fixture font into a fresh
// Typeface. One per caller: a Typeface is stateful and not safe for
// concurrent use, so tests never share one.
func NewFixtureTypeface() (*Typeface, error) {
	return LoadFont(fixtureFontTTF)
}

// NRGBA converts a Canvas-format pixel buffer — ARGB8888 premultiplied
// in wl_shm byte order — into a straight-alpha image.NRGBA: exactly the
// pixel representation the PNG goldens store, so encode, decode, and
// compare all see the same bytes and the comparison is exact. stride is
// in bytes; the returned image's rows are tight.
func NRGBA(data []byte, stride, width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		out := img.Pix[y*img.Stride:]
		for x := range width {
			s := ColorFromBytes(data[y*stride+x*4:]).Straight()
			copy(out[x*4:x*4+4], s[:])
		}
	}
	return img
}
