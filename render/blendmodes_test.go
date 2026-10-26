package render

import (
	"math"
	"math/rand/v2"
	"testing"
)

// The blend-mode conformance set (CSS compositing-1): blendPixel against
// a float64 reference written from the spec's own formulas, over every
// operator, translucent sources and backdrops included; the separable
// operators' defining identities; and the keyword table mix-blend-mode
// parses through.

// specBlend is the spec's B(Cb, Cs) for one mode over straight colors
// in [0, 1].
func specBlend(m BlendMode, cs, cb [3]float64) [3]float64 {
	lum := func(c [3]float64) float64 { return 0.3*c[0] + 0.59*c[1] + 0.11*c[2] }
	clip := func(c [3]float64) [3]float64 {
		l := lum(c)
		n, x := min(c[0], c[1], c[2]), max(c[0], c[1], c[2])
		for i := range c {
			if n < 0 {
				c[i] = l + (c[i]-l)*l/(l-n)
			}
		}
		for i := range c {
			if x > 1 {
				c[i] = l + (c[i]-l)*(1-l)/(x-l)
			}
		}
		return c
	}
	setLum := func(c [3]float64, l float64) [3]float64 {
		d := l - lum(c)
		return clip([3]float64{c[0] + d, c[1] + d, c[2] + d})
	}
	sat := func(c [3]float64) float64 { return max(c[0], c[1], c[2]) - min(c[0], c[1], c[2]) }
	setSat := func(c [3]float64, s float64) [3]float64 {
		o := [3]int{0, 1, 2}
		if c[o[0]] > c[o[1]] {
			o[0], o[1] = o[1], o[0]
		}
		if c[o[1]] > c[o[2]] {
			o[1], o[2] = o[2], o[1]
		}
		if c[o[0]] > c[o[1]] {
			o[0], o[1] = o[1], o[0]
		}
		var out [3]float64
		if c[o[2]] > c[o[0]] {
			out[o[1]] = (c[o[1]] - c[o[0]]) * s / (c[o[2]] - c[o[0]])
			out[o[2]] = s
		}
		return out
	}
	switch m {
	case BlendHue:
		return setLum(setSat(cs, sat(cb)), lum(cb))
	case BlendSaturation:
		return setLum(setSat(cb, sat(cs)), lum(cb))
	case BlendColor:
		return setLum(cs, lum(cb))
	case BlendLuminosity:
		return setLum(cb, lum(cs))
	}
	var out [3]float64
	for i := range out {
		s, b := cs[i], cb[i]
		var v float64
		hard := func(s, b float64) float64 {
			if s <= 0.5 {
				return b * 2 * s
			}
			return b + (2*s - 1) - b*(2*s-1)
		}
		switch m {
		case BlendMultiply:
			v = s * b
		case BlendScreen:
			v = s + b - s*b
		case BlendOverlay:
			v = hard(b, s)
		case BlendDarken:
			v = min(s, b)
		case BlendLighten:
			v = max(s, b)
		case BlendColorDodge:
			switch {
			case b == 0:
			case s == 1:
				v = 1
			default:
				v = min(1, b/(1-s))
			}
		case BlendColorBurn:
			switch {
			case b == 1:
				v = 1
			case s == 0:
			default:
				v = 1 - min(1, (1-b)/s)
			}
		case BlendHardLight:
			v = hard(s, b)
		case BlendSoftLight:
			if s <= 0.5 {
				v = b - (1-2*s)*b*(1-b)
			} else {
				d := math.Sqrt(b)
				if b <= 0.25 {
					d = ((16*b-12)*b + 4) * b
				}
				v = b + (2*s-1)*(d-b)
			}
		case BlendDifference:
			v = math.Abs(b - s)
		case BlendExclusion:
			v = s + b - 2*s*b
		default:
			v = s
		}
		out[i] = v
	}
	return out
}

// specComposite is the spec's premultiplied result of src over dst
// under m, in 8-bit channels before rounding.
func specComposite(m BlendMode, src, dst Color) (co [3]float64, ao float64) {
	as, ab := float64(src.A())/255, float64(dst.A())/255
	straight := func(c Color) [3]float64 {
		a := float64(c.A())
		if a == 0 {
			return [3]float64{}
		}
		return [3]float64{float64(c.R()) / a, float64(c.G()) / a, float64(c.B()) / a}
	}
	cs, cb := straight(src), straight(dst)
	b := specBlend(m, cs, cb)
	for i := range co {
		co[i] = 255 * (as*(1-ab)*cs[i] + as*ab*b[i] + (1-as)*ab*cb[i])
	}
	return co, 255 * (as + ab*(1-as))
}

// Every operator matches the spec within two levels, opaque and
// translucent, separable and not (#334).
func TestBlendPixelMatchesTheSpec(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	color := func() Color {
		a := uint32(r.IntN(256))
		if r.IntN(4) == 0 {
			a = 255
		}
		ch := func() uint32 { return uint32(r.IntN(int(a) + 1)) }
		return Color(a<<24 | ch()<<16 | ch()<<8 | ch())
	}
	for m := BlendNormal; m <= BlendLuminosity; m++ {
		worst := 0.0
		for range 4000 {
			src, dst := color(), color()
			got := blendPixel(m, src, dst)
			co, ao := specComposite(m, src, dst)
			if src.A() == 0 {
				co, ao = [3]float64{float64(dst.R()), float64(dst.G()), float64(dst.B())}, float64(dst.A())
			}
			worst = max(worst, math.Abs(float64(got.A())-ao))
			for i, g := range [3]uint8{got.R(), got.G(), got.B()} {
				d := math.Abs(float64(g) - co[i])
				worst = max(worst, d)
				if d > 2 {
					t.Fatalf("%s: %08x over %08x = %08x, channel %d %.2f off the spec's %.2f", m, uint32(src), uint32(dst), uint32(got), i, d, co[i])
				}
			}
		}
		t.Logf("%s: worst %.2f", m, worst)
	}
}

// Opaque colors under each separable operator give the spec's exact
// 8-bit results, dodge and burn dividing the operand the spec divides.
func TestBlendSeparableKnownColors(t *testing.T) {
	for _, c := range []struct {
		m        BlendMode
		src, dst Color
		r, g, b  uint8
	}{
		{BlendMultiply, RGB(128, 128, 128), RGB(255, 128, 0), 128, 64, 0},
		{BlendScreen, RGB(128, 255, 0), RGB(255, 128, 0), 255, 255, 0},
		{BlendDarken, RGB(204, 51, 128), RGB(77, 230, 128), 77, 51, 128},
		{BlendLighten, RGB(204, 51, 128), RGB(77, 230, 128), 204, 230, 128},
		{BlendDifference, RGB(204, 51, 128), RGB(77, 230, 128), 127, 179, 0},
		{BlendColorDodge, RGB(51, 191, 0), RGB(102, 26, 128), 128, 104, 128},
		{BlendColorBurn, RGB(204, 102, 0), RGB(153, 230, 255), 127, 192, 255},
	} {
		got := blendPixel(c.m, c.src, c.dst)
		if got.A() != 255 || got.R() != c.r || got.G() != c.g || got.B() != c.b {
			t.Errorf("%s: %08x over %08x = %08x, want %02x%02x%02x", c.m, uint32(c.src), uint32(c.dst), uint32(got), c.r, c.g, c.b)
		}
	}
}

// overlay(Cb, Cs) is hard-light(Cs, Cb), the spec's one identity
// between operators; soft-light at mid-gray is the identity.
func TestBlendSeparableIdentities(t *testing.T) {
	for s := int32(0); s < 256; s += 17 {
		for b := int32(0); b < 256; b += 15 {
			if o, h := blendSeparable(BlendOverlay, blendChannel{s: s, b: b}), blendSeparable(BlendHardLight, blendChannel{s: b, b: s}); o != h {
				t.Fatalf("overlay(%d, %d) = %d, hard-light swapped %d", s, b, o, h)
			}
		}
		if got := (blendSeparable(BlendSoftLight, blendChannel{s: 127, b: s}) + 127) / 255; got < s-1 || got > s+1 {
			t.Errorf("soft-light at mid-gray over %d gives %d", s, got)
		}
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
