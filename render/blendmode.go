package render

import "math"

// The CSS compositing and blending operators (mix-blend-mode's
// separable and non-separable modes): each blends one source color
// with one destination color the spec's way, then composites through
// Porter-Duff source-over weighted by the destination alpha. The
// blending runs on straight (non-premultiplied) channels per the
// compositing spec; callers convert at the premultiplied boundary.

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

// blendChannel is one separable operator over straight channels in
// [0, 1].
func blendChannel(m BlendMode, cs, cb float64) float64 {
	switch m {
	case BlendMultiply:
		return cs * cb
	case BlendScreen:
		return cs + cb - cs*cb
	case BlendOverlay:
		return blendChannel(BlendHardLight, cb, cs)
	case BlendDarken:
		return min(cs, cb)
	case BlendLighten:
		return max(cs, cb)
	case BlendColorDodge:
		switch {
		case cb == 0:
			return 0
		case cs == 1:
			return 1
		}
		return min(1, cb/(1-cs))
	case BlendColorBurn:
		switch {
		case cb == 1:
			return 1
		case cs == 0:
			return 0
		}
		return 1 - min(1, (1-cb)/cs)
	case BlendHardLight:
		if cs <= 0.5 {
			return blendChannel(BlendMultiply, cb, 2*cs)
		}
		return blendChannel(BlendScreen, cb, 2*cs-1)
	case BlendSoftLight:
		if cs <= 0.5 {
			return cb - (1-2*cs)*cb*(1-cb)
		}
		d := ((16*cb-12)*cb + 4) * cb
		if cb > 0.25 {
			d = math.Sqrt(cb)
		}
		return cb + (2*cs-1)*(d-cb)
	case BlendDifference:
		return math.Abs(cb - cs)
	case BlendExclusion:
		return cs + cb - 2*cs*cb
	}
	return cs
}

// lum is the WCAG luminance of a straight color.
func lum(r, g, b float64) float64 { return 0.3*r + 0.59*g + 0.11*b }

// setLum clips the color toward the target luminance, per the
// compositing spec's non-separable helper.
func setLum(r, g, b, l float64) (float64, float64, float64) {
	d := l - lum(r, g, b)
	r, g, b = r+d, g+d, b+d
	lo, hi := min(r, min(g, b)), max(r, max(g, b))
	if lo < 0 {
		r, g, b = r-lo, g-lo, b-lo
	}
	if hi > 1 {
		r, g, b = r+(1-hi), g+(1-hi), b+(1-hi)
	}
	return r, g, b
}

// sat is the color's saturation: max minus min channel.
func sat(r, g, b float64) float64 { return max(r, max(g, b)) - min(r, min(g, b)) }

// setSat reorders the channels by value and spreads them over
// [0, max-min] — the spec's Sat() helper. n is the channel indices
// sorted small, mid, large.
func setSat(r, g, b float64, s float64) (float64, float64, float64) {
	ch := [3]float64{r, g, b}
	ord := [3]int{0, 1, 2}
	for i := 1; i < 3; i++ {
		for j := i; j > 0 && ch[ord[j-1]] > ch[ord[j]]; j-- {
			ord[j-1], ord[j] = ord[j], ord[j-1]
		}
	}
	c := [3]float64{}
	c[ord[1]] = s / 2
	if ch[ord[2]] > ch[ord[0]] {
		c[ord[2]] = s
	}
	return c[0], c[1], c[2]
}

// blendNonSeparable blends the non-separable modes: hue, saturation,
// color and luminosity mix the two colors' hue/saturation/luminance
// components, each result pinned to the backdrop's luminance except
// luminosity, which takes the source's.
func blendNonSeparable(m BlendMode, cs, cb [3]float64) [3]float64 {
	switch m {
	case BlendHue:
		r, g, b := setSat(cs[0], cs[1], cs[2], sat(cb[0], cb[1], cb[2]))
		r, g, b = setLum(r, g, b, lum(cb[0], cb[1], cb[2]))
		return [3]float64{r, g, b}
	case BlendSaturation:
		r, g, b := setSat(cb[0], cb[1], cb[2], sat(cs[0], cs[1], cs[2]))
		r, g, b = setLum(r, g, b, lum(cb[0], cb[1], cb[2]))
		return [3]float64{r, g, b}
	case BlendColor:
		r, g, b := setLum(cs[0], cs[1], cs[2], lum(cb[0], cb[1], cb[2]))
		return [3]float64{r, g, b}
	case BlendLuminosity:
		r, g, b := setLum(cb[0], cb[1], cb[2], lum(cs[0], cs[1], cs[2]))
		return [3]float64{r, g, b}
	}
	return cs
}

// BlendColors composites the straight source color (channels in
// [0, 1], alpha a in [0, 1]) over the straight backdrop under the
// blend mode, per the compositing and blending spec: the blended
// color B(Cb, Cs) weighted by the backdrop alpha, then source-over.
func BlendColors(m BlendMode, sr, sg, sb, sa float64, dr, dg, db, da float64) (float64, float64, float64, float64) {
	if sa == 0 {
		return dr, dg, db, da
	}
	if m == BlendNormal {
		return sr, sg, sb, sa + da*(1-sa)
	}
	var br, bg, bb float64
	switch m {
	case BlendHue, BlendSaturation, BlendColor, BlendLuminosity:
		br, bg, bb = blendChannel3(m, sr, sg, sb, dr, dg, db)
	default:
		br = blendChannel(m, sr, dr)
		bg = blendChannel(m, sg, dg)
		bb = blendChannel(m, sb, db)
	}
	or := (1-da)*sr + da*br
	og := (1-da)*sg + da*bg
	ob := (1-da)*sb + da*bb
	return or, og, ob, sa + da*(1-sa)
}

// blendChannel3 runs one non-separable mode and spreads its result.
func blendChannel3(m BlendMode, sr, sg, sb, dr, dg, db float64) (float64, float64, float64) {
	out := blendNonSeparable(m, [3]float64{sr, sg, sb}, [3]float64{dr, dg, db})
	return out[0], out[1], out[2]
}

// blendPixel writes the premultiplied src over the premultiplied dst
// under the blend mode: un-premultiply both, blend, composite, and
// re-premultiply — the canvas's blend() with the operator applied.
func blendPixel(m BlendMode, src, dst Color) Color {
	if src.A() == 0 {
		return dst
	}
	ss, ds := src.Straight(), dst.Straight()
	r, g, b, a := BlendColors(m,
		float64(ss[0])/255, float64(ss[1])/255, float64(ss[2])/255, float64(ss[3])/255,
		float64(ds[0])/255, float64(ds[1])/255, float64(ds[2])/255, float64(ds[3])/255)
	ai := uint32(min(255, math.Round(a*255)))
	if ai == 0 {
		return 0
	}
	// Re-premultiply through the rounded alpha so the channels stay
	// consistent with every other premultiplied producer here.
	pm := float64(ai) / 255
	return Color(ai<<24 | clamp8(math.Round(r*255*pm))<<16 | clamp8(math.Round(g*255*pm))<<8 | clamp8(math.Round(b*255*pm)))
}

// clamp8 bounds a channel to [0, 255].
func clamp8(v float64) uint32 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint32(v)
}
