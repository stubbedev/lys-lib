// Package golden compares rendered pixels against committed PNG
// snapshots ("goldens"), so render-level tests can assert byte-stable
// output on any machine. Snapshots are produced from the bundled
// fixture font (see render/golden.go), so nothing depends on the host's
// installed fonts and there is no skip path: a golden run renders the
// same pixels everywhere.
//
// Policy:
//
//   - A golden compares exact-equal by default, pixel for pixel and
//     channel for channel, in the straight-alpha NRGBA form PNG stores.
//     The deterministic font plus the fixed rasterizer make exact
//     equality the normal state, not an aspiration.
//   - Tolerance relaxes equality for deliberately fuzzy comparisons: a
//     pixel matches when every channel differs by at most
//     Tolerance.ChannelDiff, and the test still passes when at most
//     Tolerance.MaxMismatch pixels exceed that. Use it sparingly; every
//     snapshot in the tree today is exact.
//   - A reference (Reference) is an image another renderer produced,
//     the independent truth a renderer is held to. Anti-aliasing never
//     agrees byte for byte across renderers, so references compare
//     within a Tolerance, and UPDATE_GOLDEN never rewrites them.
//   - A test FAILS when its golden is missing. New widgets must land
//     with their snapshots, otherwise the golden net has a hole nobody
//     notices until it misses a real regression.
//   - Running with UPDATE_GOLDEN=1 in the environment rewrites every
//     golden the run touches instead of comparing (missing ones are
//     simply written), which is also how the missing-golden failure is
//     resolved. After a deliberate visual change, regenerate and review
//     the diff: `UPDATE_GOLDEN=1 go test ./render ./widget`, then
//     `git diff -- '*testdata/golden*'`. The regeneration round-trip
//     must leave the tree unchanged when nothing changed.
package golden

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TB is the subset of testing.TB the harness needs, so this package
// stays importable from non-test code and test doubles.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
}

// Tolerance relaxes the default exact-equality comparison. The zero
// value demands byte-identical pixels.
type Tolerance struct {
	// ChannelDiff is the largest per-channel absolute difference
	// (0-255) a pixel may show and still match.
	ChannelDiff uint8
	// MaxMismatch is how many pixels may exceed ChannelDiff before the
	// comparison fails.
	MaxMismatch int
}

// Update reports whether the run regenerates goldens instead of
// comparing: UPDATE_GOLDEN=1 in the environment.
func Update() bool { return os.Getenv("UPDATE_GOLDEN") == "1" }

// Check compares img against dir/name.png. It Fatalf's on any mismatch:
// missing golden, dimension drift, or pixels outside tol. With
// UPDATE_GOLDEN=1 it writes the golden instead and passes.
func Check(t TB, dir, name string, img *image.NRGBA, tol Tolerance) {
	t.Helper()
	path := filepath.Join(dir, name+".png")
	if Update() {
		write(t, path, img)
		return
	}
	compare(t, "golden", path, img, tol, false,
		"; a snapshot must land with the code it pins - run UPDATE_GOLDEN=1 go test ./... to generate it",
		"\nregenerate with UPDATE_GOLDEN=1 go test ./... only after confirming the new output is correct")
}

// Reference compares img against the image at path that another
// renderer produced, the independent truth img is held to. Unlike a
// golden, UPDATE_GOLDEN never rewrites it: the code under test cannot
// produce its own reference. Pixels compare premultiplied, since a
// nearly transparent pixel's straight color is noise between
// renderers.
func Reference(t TB, path string, img *image.NRGBA, tol Tolerance) {
	t.Helper()
	compare(t, "reference", path, img, tol, true, "", "")
}

// compare is Check's and Reference's comparison; kind names the
// expected image in failures, premultiplied compares premultiplied
// pixels, and the hints follow the missing and mismatched reports.
func compare(t TB, kind, path string, img *image.NRGBA, tol Tolerance, premultiplied bool, missingHint, mismatchHint string) {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the caller's testdata file
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s %s missing%s", kind, path, missingHint)
	}
	if err != nil {
		t.Fatalf("read %s %s: %v", kind, path, err)
	}
	dec, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode %s %s: %v", kind, path, err)
	}
	want := asNRGBA(dec)
	if want.Rect != img.Rect {
		t.Fatalf("%s %s is %dx%d, shot is %dx%d; the layout under it changed - regenerate and review",
			kind, path, want.Rect.Dx(), want.Rect.Dy(), img.Rect.Dx(), img.Rect.Dy())
	}
	wantPix, gotPix := want.Pix, img.Pix
	if premultiplied {
		wantPix, gotPix = premultiply(want), premultiply(img)
	}
	if bytes.Equal(wantPix, gotPix) {
		return
	}
	mismatch, samples := mismatches(wantPix, gotPix, want.Rect.Dx(), tol)
	if mismatch <= tol.MaxMismatch {
		return
	}
	t.Fatalf("%s %s mismatched: %d of %d pixels differ (tolerance %d/channel, %d allowed)%s%s",
		kind, path, mismatch, len(want.Pix)/4, tol.ChannelDiff, tol.MaxMismatch, strings.Join(samples, ""), mismatchHint)
}

// write encodes img to path, the UPDATE_GOLDEN=1 half of Check.
func write(t TB, path string, img *image.NRGBA) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create golden dir: %v", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode golden %s: %v", path, err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write golden %s: %v", path, err)
	}
	t.Logf("golden updated: %s", path)
}

// asNRGBA normalizes a decoded golden to straight-alpha NRGBA pixels.
// Opaque shots (everything painted over an opaque background) are
// stored by the encoder as truecolor-without-alpha and decode as
// *image.RGBA; at full opacity the premultiplied bytes equal the
// straight ones, so rows copy verbatim. The generic path keeps any
// other layout exact.
func asNRGBA(img image.Image) *image.NRGBA {
	if m, ok := img.(*image.NRGBA); ok {
		return m
	}
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	if m, ok := img.(*image.RGBA); ok && m.Opaque() {
		copy(out.Pix, m.Pix)
		return out
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c, _ := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			o := out.PixOffset(x-b.Min.X, y-b.Min.Y)
			out.Pix[o+0], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = c.R, c.G, c.B, c.A
		}
	}
	return out
}

// premultiply is img's pixels with each color channel scaled by its
// alpha, rounded.
func premultiply(img *image.NRGBA) []byte {
	out := make([]byte, len(img.Pix))
	for i := 0; i < len(img.Pix); i += 4 {
		a := uint32(img.Pix[i+3])
		for c := range 3 {
			out[i+c] = uint8((uint32(img.Pix[i+c])*a + 127) / 255)
		}
		out[i+3] = img.Pix[i+3]
	}
	return out
}

// maxReported caps the per-pixel samples a failure prints.
const maxReported = 5

// mismatches counts the pixels outside tol and samples the first
// few, enough to see what moved without opening an image editor.
func mismatches(want, got []byte, w int, tol Tolerance) (mismatch int, samples []string) {
	for i := 0; i < len(want); i += 4 {
		if pixelMatches(want[i:i+4], got[i:i+4], tol) {
			continue
		}
		if mismatch < maxReported {
			x, y := (i/4)%w, (i/4)/w
			samples = append(samples, fmt.Sprintf("\n  (%d,%d) want R%d G%d B%d A%d, got R%d G%d B%d A%d",
				x, y,
				want[i], want[i+1], want[i+2], want[i+3],
				got[i], got[i+1], got[i+2], got[i+3]))
		}
		mismatch++
	}
	return mismatch, samples
}

// pixelMatches reports whether one pixel pair is within tol.
func pixelMatches(want, got []byte, tol Tolerance) bool {
	if tol.ChannelDiff == 0 {
		return want[0] == got[0] && want[1] == got[1] && want[2] == got[2] && want[3] == got[3]
	}
	for i := range 4 {
		d := int(want[i]) - int(got[i])
		if d < 0 {
			d = -d
		}
		if d > int(tol.ChannelDiff) {
			return false
		}
	}
	return true
}
