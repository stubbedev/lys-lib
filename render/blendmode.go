package render

import "math"

// The CSS compositing and blending operators (mix-blend-mode's
// separable and non-separable modes): each blends one source color
// with one backdrop color the spec's way, then composites source-over
// with the blend weighted by the backdrop alpha. The blending runs on
// straight (non-premultiplied) channels per the compositing spec, all
// in integers: every step is exact, so a GPU shader computes the same
// bytes.

// BlendMode is one CSS mix-blend-mode operator. BlendNormal is the
// zero value, so an unset property composites as plain source-over.
type BlendMode uint8

// The blend modes, CSS's sixteen.
const (
	BlendNormal BlendMode = iota
	BlendMultiply
	BlendScreen
	BlendOverlay
	BlendDarken
	BlendLighten
	BlendColorDodge
	BlendColorBurn
	BlendHardLight
	BlendSoftLight
	BlendDifference
	BlendExclusion
	BlendHue
	BlendSaturation
	BlendColor
	BlendLuminosity
)

// blendName maps a mode to its CSS keyword; the empty string ends the
// table scan in BlendModeNamed.
var blendNames = [...]string{
	BlendNormal: "normal", BlendMultiply: "multiply", BlendScreen: "screen",
	BlendOverlay: "overlay", BlendDarken: "darken", BlendLighten: "lighten",
	BlendColorDodge: "color-dodge", BlendColorBurn: "color-burn",
	BlendHardLight: "hard-light", BlendSoftLight: "soft-light",
	BlendDifference: "difference", BlendExclusion: "exclusion",
	BlendHue: "hue", BlendSaturation: "saturation", BlendColor: "color",
	BlendLuminosity: "luminosity",
}

// String returns the CSS keyword of the mode.
func (m BlendMode) String() string {
	if int(m) < len(blendNames) {
		return blendNames[m]
	}
	return "normal"
}

// BlendModeNamed resolves a CSS keyword; false for anything else.
func BlendModeNamed(name string) (BlendMode, bool) {
	for i, n := range blendNames {
		if n == name {
			return BlendMode(i), true
		}
	}
	return BlendNormal, false
}

// blendK is the separable modes' fixed-point unit: a channel in
// [0, 1] is an integer in [0, blendK], 255 * 255, so a product of two
// 8-bit channels is exact.
const blendK = 255 * 255

// blendQ is the non-separable modes' unit: a channel times 16, fine
// enough for the luminance and saturation arithmetic and small enough
// that their products fit 32 bits.
const blendQ = 255 * 16

// SoftLightTable is soft-light's D(Cb) for every 8-bit backdrop
// channel, in blendK units: the one curve the integer blend cannot
// compute exactly, tabled once for both renderers.
var SoftLightTable = func() (t [256]int32) {
	for b := range t {
		x := float64(b) / 255
		d := math.Sqrt(x)
		if x <= 0.25 {
			d = ((16*x-12)*x + 4) * x
		}
		t[b] = int32(math.Round(d * blendK))
	}
	return t
}()

// blendChannel is one channel of a source and backdrop pair: the
// straight 8-bit values s and b, and the premultiplied sp and bp with
// their alphas sa and ba, which dodge and burn divide exactly.
type blendChannel struct{ s, b, sp, sa, bp, ba int32 }

// blendSeparable is a separable mode's B(Cb, Cs) for one channel, in
// blendK units.
func blendSeparable(m BlendMode, c blendChannel) int32 {
	s, b := c.s, c.b
	switch m {
	case BlendMultiply:
		return s * b
	case BlendScreen:
		return (s+b)*255 - s*b
	case BlendOverlay:
		return blendSeparable(BlendHardLight, blendChannel{s: b, b: s})
	case BlendDarken:
		return min(s, b) * 255
	case BlendLighten:
		return max(s, b) * 255
	case BlendColorDodge:
		// Cb / (1 - Cs) = bp*sa / (ba*(sa - sp)), straight from the
		// premultiplied channels: a source near white divides by a
		// sliver, which 8-bit straight values would round away.
		switch {
		case c.bp == 0 || c.ba == 0:
			return 0
		case c.sp >= c.sa:
			return blendK
		}
		d := c.ba * (c.sa - c.sp)
		return 255 * min(255, (c.bp*c.sa*255+d/2)/d)
	case BlendColorBurn:
		// 1 - (1 - Cb) / Cs = 1 - (ba - bp)*sa / (ba*sp).
		switch {
		case c.ba == 0:
			return 0
		case c.bp >= c.ba:
			return blendK
		case c.sp == 0:
			return 0
		}
		d := c.ba * c.sp
		return 255 * (255 - min(255, ((c.ba-c.bp)*c.sa*255+d/2)/d))
	case BlendHardLight:
		if 2*s <= 255 {
			return b * 2 * s
		}
		s2 := 2*s - 255
		return (b+s2)*255 - b*s2
	case BlendSoftLight:
		if 2*s <= 255 {
			return b*255 - roundDiv((255-2*s)*b*(255-b), 255)
		}
		return b*255 + roundDiv((2*s-255)*(SoftLightTable[b]-b*255), 255)
	case BlendDifference:
		if b > s {
			return (b - s) * 255
		}
		return (s - b) * 255
	case BlendExclusion:
		return (s+b)*255 - 2*s*b
	}
	return s * 255
}

// lumQ is the spec's Lum of a color in blendQ units.
func lumQ(c [3]int32) int32 { return (30*c[0] + 59*c[1] + 11*c[2] + 50) / 100 }

// setLumQ is the spec's SetLum: c shifted to luminance l, then its
// ClipColor pulling an out-of-range channel back toward l.
func setLumQ(c [3]int32, l int32) [3]int32 {
	d := l - lumQ(c)
	for i := range c {
		c[i] += d
	}
	l = lumQ(c)
	lo, hi := min(c[0], c[1], c[2]), max(c[0], c[1], c[2])
	if lo < 0 && l != lo {
		for i := range c {
			c[i] = l + (c[i]-l)*l/(l-lo)
		}
	}
	if hi > blendQ && hi != l {
		for i := range c {
			c[i] = l + (c[i]-l)*(blendQ-l)/(hi-l)
		}
	}
	return c
}

// satQ is the spec's Sat: the largest channel less the smallest.
func satQ(c [3]int32) int32 { return max(c[0], c[1], c[2]) - min(c[0], c[1], c[2]) }

// setSatQ is the spec's SetSat: the channels ordered small, mid, large
// (ties kept in channel order), the small one at 0, the large at s and
// the mid in proportion between; a gray stays black.
func setSatQ(c [3]int32, s int32) [3]int32 {
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
	var out [3]int32
	if span := c[o[2]] - c[o[0]]; span > 0 {
		out[o[1]] = (c[o[1]] - c[o[0]]) * s / span
		out[o[2]] = s
	}
	return out
}

// blendNonSeparable is a non-separable mode's B(Cb, Cs) for straight
// colors in blendQ units: hue, saturation and color keep the
// backdrop's luminance, luminosity takes the source's.
func blendNonSeparable(m BlendMode, s, b [3]int32) [3]int32 {
	switch m {
	case BlendHue:
		return setLumQ(setSatQ(s, satQ(b)), lumQ(b))
	case BlendSaturation:
		return setLumQ(setSatQ(b, satQ(s)), lumQ(b))
	case BlendColor:
		return setLumQ(s, lumQ(b))
	}
	return setLumQ(b, lumQ(s))
}

// separable reports whether m blends channel by channel.
func (m BlendMode) separable() bool { return m < BlendHue }

// straightIn is a premultiplied color's straight channels scaled to
// unit (255 for 8 bits, blendQ for the non-separable modes), rounded;
// all zero for a transparent color.
func straightIn(c Color, unit int32) [3]int32 {
	a := int32(c.A())
	if a == 0 {
		return [3]int32{}
	}
	ch := [3]int32{int32(c.R()), int32(c.G()), int32(c.B())}
	for i, v := range ch {
		ch[i] = min(unit, (v*unit+a/2)/a)
	}
	return ch
}

// blendPixel composites the premultiplied src over the premultiplied
// dst under the blend mode, the compositing spec's way: the result is
// the source where the backdrop is clear, B(Cb, Cs) where both cover,
// and the backdrop where the source is clear,
//
//	co = as*(1-ab)*Cs + as*ab*B(Cb, Cs) + (1-as)*ab*Cb
//	ao = as + ab*(1-as)
//
// in integers, B rounded to 8 bits first.
func blendPixel(m BlendMode, src, dst Color) Color {
	sa, da := int32(src.A()), int32(dst.A())
	if sa == 0 {
		return dst
	}
	var bl [3]int32
	sp := [3]int32{int32(src.R()), int32(src.G()), int32(src.B())}
	dp := [3]int32{int32(dst.R()), int32(dst.G()), int32(dst.B())}
	if m.separable() {
		s, b := straightIn(src, 255), straightIn(dst, 255)
		for i := range bl {
			bl[i] = (blendSeparable(m, blendChannel{s: s[i], b: b[i], sp: sp[i], sa: sa, bp: dp[i], ba: da}) + 127) / 255
		}
	} else {
		q := blendNonSeparable(m, straightIn(src, blendQ), straightIn(dst, blendQ))
		for i := range bl {
			bl[i] = min(255, max(0, (q[i]+8)/16))
		}
	}
	ao := (sa*255 + da*(255-sa) + 127) / 255
	var out [3]uint32
	for i := range out {
		x := sp[i]*(255-da) + dp[i]*(255-sa) + (sa*da*bl[i]+127)/255
		out[i] = uint32(min((x+127)/255, ao))
	}
	return Color(uint32(ao)<<24 | out[0]<<16 | out[1]<<8 | out[2])
}
