package render

import (
	"math"
	"testing"
)

// The blend-mode conformance set (CSS compositing-1): every separable
// and non-separable operator probed on known straight-channel inputs
// with an opaque backdrop (where the blend result is the operator's
// own output), the destination-alpha weighting rule, and the keyword
// table mix-blend-mode parses through.

func TestBlendModesAgainstKnownColors(t *testing.T) {
	cases := []struct {
		name   string
		cs, cb [3]float64
		want   [3]float64
	}{
		{"multiply", [3]float64{0.5, 0.5, 0.5}, [3]float64{1, 0.5, 0}, [3]float64{0.5, 0.25, 0}},
		{"screen", [3]float64{0.5, 1, 0}, [3]float64{1, 0.5, 0}, [3]float64{1, 1, 0}},
		{"darken", [3]float64{0.8, 0.2, 0.5}, [3]float64{0.3, 0.9, 0.5}, [3]float64{0.3, 0.2, 0.5}},
		{"lighten", [3]float64{0.8, 0.2, 0.5}, [3]float64{0.3, 0.9, 0.5}, [3]float64{0.8, 0.9, 0.5}},
		{"color-dodge", [3]float64{0.5, 1, 0}, [3]float64{0.5, 0.5, 0.5}, [3]float64{1, 1, 0}},
		{"color-burn", [3]float64{0.5, 0, 0.25}, [3]float64{0.5, 0.5, 1}, [3]float64{0, 0, 1}},
		{"difference", [3]float64{0.8, 0.2, 0.5}, [3]float64{0.3, 0.9, 0.5}, [3]float64{0.5, 0.7, 0}},
		{"exclusion", [3]float64{0.5, 0.5, 1}, [3]float64{0.5, 0.25, 0.5}, [3]float64{0.5, 0.5, 0.5}},
	}
	for _, c := range cases {
		m, ok := BlendModeNamed(c.name)
		if !ok {
			t.Fatalf("%s: not a keyword", c.name)
		}
		for i := range 3 {
			got := blendChannel(m, c.cs[i], c.cb[i])
			if abs64(got-c.want[i]) > 1e-9 {
				t.Errorf("%s channel %d: %v, want %v", c.name, i, got, c.want[i])
			}
		}
	}
}

func TestBlendOverlayHardLightSwap(t *testing.T) {
	// overlay(Cb, Cs) is hard-light(Cs, Cb): the spec's one identity
	// between operators, cheap to pin.
	for _, p := range [][2]float64{{0.2, 0.7}, {0.6, 0.3}, {0.5, 0.5}, {1, 0}} {
		if o := blendChannel(BlendOverlay, p[0], p[1]); o != blendChannel(BlendHardLight, p[1], p[0]) {
			t.Errorf("overlay(%v,%v)=%v, hard-light swapped=%v", p[0], p[1], o, blendChannel(BlendHardLight, p[1], p[0]))
		}
	}
}

func TestBlendSoftLightSpecExample(t *testing.T) {
	// The compositing spec's own soft-light example class: a dark
	// source darkens, a light source lightens, and 0.5 is the identity.
	for _, p := range [][2]float64{{0.25, 0.75}, {0.75, 0.25}, {0.5, 0.42}} {
		got := blendChannel(BlendSoftLight, p[0], p[1])
		if p[0] == 0.5 && got != p[1] {
			t.Errorf("soft-light 0.5 must be the identity: %v", got)
		}
		if p[0] < 0.5 && got > p[1] {
			t.Errorf("soft-light dark source lightened: %v over %v", got, p[1])
		}
		if p[0] > 0.5 && got < p[1] {
			t.Errorf("soft-light light source darkened: %v over %v", got, p[1])
		}
	}
}

func TestBlendNonSeparableKeepBackdropLuminance(t *testing.T) {
	// hue, saturation and color pin the result to the backdrop's
	// luminance; luminosity takes the source's.
	cb := [3]float64{0.8, 0.4, 0.2}
	cs := [3]float64{0.1, 0.6, 0.9}
	for _, m := range []BlendMode{BlendHue, BlendSaturation, BlendColor} {
		out := blendNonSeparable(m, cs, cb)
		if abs64(lum(out[0], out[1], out[2])-lum(cb[0], cb[1], cb[2])) > 1e-9 {
			t.Errorf("%s moved the backdrop luminance: %v", m, out)
		}
	}
	out := blendNonSeparable(BlendLuminosity, cs, cb)
	if abs64(lum(out[0], out[1], out[2])-lum(cs[0], cs[1], cs[2])) > 1e-9 {
		t.Errorf("luminosity dropped the source luminance: %v", out)
	}
}

func TestBlendColorsDestinationAlphaWeighting(t *testing.T) {
	// With a transparent backdrop the blend result must be the source
	// itself (B never shows through nothing), and with a fully
	// transparent source the destination survives untouched.
	r, g, b, a := BlendColors(BlendMultiply, 0.8, 0.6, 0.4, 1, 0.5, 0.5, 0.5, 0)
	if r != 0.8 || g != 0.6 || b != 0.4 || a != 1 {
		t.Errorf("blend over transparent backdrop: %v %v %v %v", r, g, b, a)
	}
	r, g, b, a = BlendColors(BlendMultiply, 0.8, 0.6, 0.4, 0, 0.5, 0.5, 0.5, 1)
	if r != 0.5 || g != 0.5 || b != 0.5 || a != 1 {
		t.Errorf("transparent source changed the destination: %v %v %v %v", r, g, b, a)
	}
}

func TestBlendPixelPremultipliedComposite(t *testing.T) {
	// An opaque source under multiply over an opaque backdrop is the
	// multiplied color written opaque; a half-alpha source keeps the
	// destination's other half.
	dst := RGB(200, 100, 50)
	got := blendPixel(BlendMultiply, RGB(128, 128, 128), dst)
	wantR := uint32(200) * 128 / 255
	if got.A() != 0xff || abs64(float64(got.R())-float64(wantR)) > 1 {
		t.Errorf("multiply pixel = %08x, want ~%08x", uint32(got), 0xff000000|wantR<<16)
	}
	got = blendPixel(BlendMultiply, RGBA(255, 255, 255, 128), 0)
	if got.A() != 128 || got.R() != 128 || got.G() != 128 || got.B() != 128 {
		t.Errorf("half-alpha blend over transparent = %08x, want premultiplied 128s", uint32(got))
	}
}

func TestBlendModeKeywordsRoundTrip(t *testing.T) {
	for m := BlendNormal; m <= BlendLuminosity; m++ {
		n, ok := BlendModeNamed(m.String())
		if !ok || n != m {
			t.Errorf("%q round trip: %v %v", m.String(), n, ok)
		}
	}
	if _, ok := BlendModeNamed("plaid"); ok {
		t.Error("an unknown keyword resolved")
	}
}

func TestDrawLayerBlendsUnderMode(t *testing.T) {
	// A red half of the layer multiplied over a white canvas stays red;
	// multiplied over a black canvas it goes black — the two probes
	// that catch a mode applied as plain source-over.
	l := blendTestLayer(t, RGB(255, 0, 0))
	for _, tc := range []struct {
		bg   Color
		want uint8
	}{
		{RGB(255, 255, 255), 255},
		{RGB(0, 0, 0), 0},
	} {
		cv, _ := newTestCanvas(4, 4)
		cv.Clear(cv.Rect(), tc.bg)
		cv.DrawLayer(l, Rect{W: 4, H: 4}, BlendMultiply)
		if got := cv.get(2, 2).R(); got != tc.want {
			t.Errorf("multiply over %08x: red %d, want %d", uint32(tc.bg), got, tc.want)
		}
	}
}

// blendTestLayer fills a fresh layer with col over its whole canvas.
func blendTestLayer(t *testing.T, col Color) *Layer {
	t.Helper()
	cv, _ := newTestCanvas(4, 4)
	cv.Clear(cv.Rect(), col)
	l := &Layer{}
	return cv.Snapshot(l, cv.Rect())
}

func abs64(v float64) float64 { return math.Abs(v) }
