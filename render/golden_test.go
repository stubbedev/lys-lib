package render

// Golden-image tests for the canvas primitives and the text pipeline,
// compared byte-exactly against committed PNGs (see internal/golden for
// the policy and testdata/README.md for the fixture font). These pin
// the anti-aliasing coverage math, the gradient interpolation, and the
// shape/raster path at both device scales — the same pixels the widget
// snapshots are built from.

import (
	"testing"

	"github.com/stubbedev/gelm/internal/golden"
)

// goldenCanvas paints into a fresh WxH canvas cleared to the dark
// theme's background and checks the pixels against
// testdata/golden/name.png.
func goldenCanvas(t *testing.T, name string, w, h int, paint func(cv *Canvas)) {
	t.Helper()
	stride := Stride(w)
	buf := make([]byte, stride*h)
	cv := New(buf, stride, w, h)
	cv.Clear(cv.Rect(), RGB(0x1E, 0x1E, 0x2E))
	paint(cv)
	golden.Check(t, "testdata/golden", name, NRGBA(buf, stride, w, h), golden.Tolerance{})
}

func TestGoldenRoundedRect(t *testing.T) {
	goldenCanvas(t, "rounded-rect", 160, 120, func(cv *Canvas) {
		cv.RoundedRect(Rect{X: 10, Y: 10, W: 60, H: 40}, 8, RGB(0x18, 0x18, 0x24))
		cv.RoundedRect(Rect{X: 84, Y: 10, W: 60, H: 40}, 20, RGB(0x89, 0xB4, 0xFA))
		cv.RoundedRect(Rect{X: 10, Y: 64, W: 60, H: 40}, 0, RGB(0xA6, 0xAD, 0xC3))
		// Translucent fill: the coverage and the fill's own alpha must
		// both scale the premultiplied channels.
		cv.RoundedRect(Rect{X: 84, Y: 64, W: 60, H: 40}, 12, RGBA(0xCD, 0xD6, 0xF4, 128))
	})
}

func TestGoldenLine(t *testing.T) {
	goldenCanvas(t, "line", 160, 120, func(cv *Canvas) {
		cv.Line(8, 108, 150, 18, 2, RGB(0x89, 0xB4, 0xFA))
		cv.Line(10, 20, 148, 20, 3, RGB(0xA6, 0xAD, 0xC3))
		cv.Line(20, 40, 21, 100, 1, RGB(0xCD, 0xD6, 0xF4))
	})
}

func TestGoldenGradientAlpha(t *testing.T) {
	goldenCanvas(t, "gradient-alpha", 160, 90, func(cv *Canvas) {
		cv.LinearGradient(Rect{X: 8, Y: 8, W: 144, H: 30}, RGB(0x1E, 0x66, 0xF5), RGB(0x89, 0xB4, 0xFA), true)
		cv.LinearGradient(Rect{X: 8, Y: 48, W: 144, H: 30}, RGB(0xCD, 0xD6, 0xF4), RGB(0x11, 0x11, 0x1B), false)
		prev := cv.PushAlpha(0.5)
		cv.FillRect(Rect{X: 30, Y: 20, W: 100, H: 40}, RGB(0xFF, 0xFF, 0xFF))
		cv.PopAlpha(prev)
	})
}

func TestGoldenText(t *testing.T) {
	tf := goldenTypeface(t)
	goldenCanvas(t, "text", 220, 96, func(cv *Canvas) {
		tf.Draw(cv, tf.Shape("Deterministic goldens — 0123", 16), 8, 24, RGB(0xCD, 0xD6, 0xF4))
		tf.Draw(cv, tf.Shape("… \"quoted\" (v1.2) hjpq", 16), 8, 48, RGB(0xA6, 0xAD, 0xC3))
		tf.DrawAligned(cv, "cold cache, warm cache", Rect{X: 8, Y: 60, W: 204, H: 24}, 13, RGB(0x89, 0xB4, 0xFA), AlignCenter)
	})
}

func TestGoldenTextScale2(t *testing.T) {
	tf := goldenTypeface(t)
	// Device scale 2: the same logical primitives rasterized at 2x — the
	// path fractional-scale windows exercise.
	w, h := 110, 48
	stride := Stride(w * 2)
	buf := make([]byte, stride*h*2)
	cv := NewScaled(buf, stride, w*2, h*2, 2, 1)
	cv.Clear(cv.Rect(), RGB(0x1E, 0x1E, 0x2E))
	cv.RoundedRect(Rect{X: 8, Y: 8, W: 60, H: 24}, 8, RGB(0x89, 0xB4, 0xFA))
	cv.Line(76, 30, 102, 12, 2, RGB(0xA6, 0xAD, 0xC3))
	tf.Draw(cv, tf.Shape("2x sharp", 13), 14, 24, RGB(0x11, 0x11, 0x1B))
	golden.Check(t, "testdata/golden", "text-scale2", NRGBA(buf, stride, w*2, h*2), golden.Tolerance{})
}

// goldenTypeface loads the bundled fixture face; goldens never consult
// the host's fonts.
func goldenTypeface(tb testing.TB) *Typeface {
	tb.Helper()
	tf, err := NewFixtureTypeface()
	if err != nil {
		tb.Fatalf("load fixture font: %v", err)
	}
	return tf
}
