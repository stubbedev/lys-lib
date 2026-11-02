package gpu

import (
	_ "embed" // the colour transform's GLSL below

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// ColorGLSL is the GLSL of ColorTransform.Apply, colorApply, valid as GLSL
// 4.50 and as GLSL ES 1.00, for renderers outside this package to share. CP
// must name its nine vec4s, ColorTransform.Uniforms.
//
//go:embed shaders/color.glsl
var ColorGLSL string

// ColorUniforms is how many vec4s ColorGLSL reads through CP.
const ColorUniforms = 9

// ColorKind is a family of transfer functions.
type ColorKind uint8

// The families. ColorHLG only decodes: nothing is encoded to HLG here.
const (
	ColorLinear ColorKind = iota
	ColorCurve
	ColorPQ
	ColorHLG
)

// ColorFunc is one transfer function. A ColorCurve is ICC's parametric form,
// mirrored through the origin: (A*x + B)^G + E where x >= D, and C*x + F
// below it.
type ColorFunc struct {
	Kind                ColorKind
	G, A, B, C, D, E, F float32
}

// ColorTransform takes a premultiplied colour from one encoding to another:
// un-premultiply, Decode (then, for HLG, the OOTF: scene light scaled by
// its Luma luminance to the power HLGGamma - 1), Matrix, compress the
// largest channel above Knee when Compress is positive, multiply by Scale,
// Encode, and premultiply again. It is a value, so equal transforms compare
// equal.
type ColorTransform struct {
	Decode   ColorFunc
	Luma     [3]float32
	HLGGamma float32
	Matrix   [9]float32
	Knee     float32
	Compress float32
	Scale    float32
	Encode   ColorFunc
}

// Uniforms packs t for ColorGLSL.
func (t *ColorTransform) Uniforms() [ColorUniforms][4]float32 {
	d, e, m := t.Decode, t.Encode, t.Matrix
	return [ColorUniforms][4]float32{
		{d.G, d.A, d.B, d.C},
		{d.D, d.E, d.F, float32(d.Kind)},
		{t.Luma[0], t.Luma[1], t.Luma[2], t.HLGGamma - 1},
		{m[0], m[1], m[2]},
		{m[3], m[4], m[5]},
		{m[6], m[7], m[8]},
		{t.Knee, t.Compress, 0, t.Scale},
		{e.G, e.A, e.B, e.C},
		{e.D, e.E, e.F, float32(e.Kind)},
	}
}

func pow32(x, y float32) float32 {
	return float32(gomath.Pow(float64(x), float64(y)))
}

func (f ColorFunc) curve(x float32) float32 {
	m := x
	if m < 0 {
		m = -m
	}
	var y float32
	if m >= f.D {
		y = pow32(float32(f.A*m)+f.B, f.G) + f.E
	} else {
		y = float32(f.C*m) + f.F
	}
	if x < 0 {
		return -y
	}
	return y
}

const (
	pqM1 = 0.1593017578125
	pqM2 = 78.84375
	pqC1 = 0.8359375
	pqC2 = 18.8515625
	pqC3 = 18.6875
)

func pqDecode(v float32) float32 {
	m := pow32(max(v, 0), 1.0/pqM2)
	return pow32(max(m-pqC1, 0)/(pqC2-float32(pqC3*m)), 1.0/pqM1)
}

func pqEncode(l float32) float32 {
	m := pow32(max(l, 0), pqM1)
	return pow32((pqC1+float32(pqC2*m))/(1+float32(pqC3*m)), pqM2)
}

func hlgDecode(v float32) float32 {
	e := max(v, 0)
	if e <= 0.5 {
		return float32(e*e) / 3
	}
	return (float32(gomath.Exp(float64((e-0.55991073)/0.17883277))) + 0.28466892) / 12
}

func (t *ColorTransform) decode(c [3]float32) [3]float32 {
	f := t.Decode
	switch f.Kind {
	case ColorCurve:
		return [3]float32{f.curve(c[0]), f.curve(c[1]), f.curve(c[2])}
	case ColorPQ:
		return [3]float32{pqDecode(c[0]), pqDecode(c[1]), pqDecode(c[2])}
	case ColorHLG:
		s := [3]float32{hlgDecode(c[0]), hlgDecode(c[1]), hlgDecode(c[2])}
		y := pow32(max(float32(t.Luma[0]*s[0])+float32(t.Luma[1]*s[1])+float32(t.Luma[2]*s[2]), 0.000001), t.HLGGamma-1)
		return [3]float32{float32(s[0] * y), float32(s[1] * y), float32(s[2] * y)}
	}
	return c
}

func (t *ColorTransform) encode(c [3]float32) [3]float32 {
	f := t.Encode
	switch f.Kind {
	case ColorCurve:
		return [3]float32{f.curve(c[0]), f.curve(c[1]), f.curve(c[2])}
	case ColorPQ:
		return [3]float32{pqEncode(c[0]), pqEncode(c[1]), pqEncode(c[2])}
	}
	return c
}

func (t *ColorTransform) applyDecoded(l [3]float32) [3]float32 {
	m := t.Matrix
	row := func(i int) float32 {
		return float32(m[i]*l[0]) + float32(m[i+1]*l[1]) + float32(m[i+2]*l[2])
	}
	rgb := [3]float32{row(0), row(3), row(6)}
	if peak := max(rgb[0], rgb[1], rgb[2]); t.Compress > 0 && peak > t.Knee {
		over := peak - t.Knee
		k := (t.Knee + over/(1+float32(over*t.Compress))) / peak
		rgb = [3]float32{float32(rgb[0] * k), float32(rgb[1] * k), float32(rgb[2] * k)}
	}
	return t.encode([3]float32{float32(rgb[0] * t.Scale), float32(rgb[1] * t.Scale), float32(rgb[2] * t.Scale)})
}

const colorLimit = 1000000

func finite(v float32) float32 {
	if v > -colorLimit && v < colorLimit {
		return v
	}
	return 0
}

// Apply is colorApply in Go: c, premultiplied, through the transform. What a
// client's float buffer holds beyond the finite, NaN and infinities, comes out
// as nothing, so it cannot stick in a target that blends over it for good.
func (t *ColorTransform) Apply(c [4]float32) [4]float32 {
	if !(c[3] > 0) {
		return [4]float32{}
	}
	a := min(c[3], 1)
	rgb := t.applyDecoded(t.decode([3]float32{c[0] / c[3], c[1] / c[3], c[2] / c[3]}))
	return [4]float32{float32(finite(rgb[0]) * a), float32(finite(rgb[1]) * a), float32(finite(rgb[2]) * a), a}
}
