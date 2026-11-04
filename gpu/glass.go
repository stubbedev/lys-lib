package gpu

import (
	_ "embed" // the glass GLSL below
	"errors"
	"fmt"
	"image"
	"math"

	"github.com/stubbedev/lys-lib/render"
)

// GlassGLSL is the GLSL of a Glass draw, glassBackdrop and glassAdjust,
// valid as GLSL 4.50 and as GLSL ES 1.00 after MaskGLSL and with SAMPLE
// naming the sampling function, for renderers outside this package to
// share.
//
//go:embed shaders/glass.glsl
var GlassGLSL string

// Glass is how DrawGlass turns a blurred backdrop into a glass surface:
// the factors of saturate(), brightness() and contrast(), where 1 changes
// nothing, and the bezel the backdrop bends through along the edge, with
// its displacement at the rim, both in target pixels, as -lys-refract()
// does; an empty bezel or no displacement bends nothing.
type Glass struct {
	Saturation, Brightness, Contrast float32
	Bezel, Amount                    float32
}

// Refracts reports whether the glass bends its backdrop at all.
func (g Glass) Refracts() bool {
	return g.Bezel > 0 && g.Amount > 0
}

// Check reports a factor or length that is negative, infinite or NaN,
// and a mask DrawGlass cannot draw through: a falloff or a merge, which
// a glass surface has no use for.
func (g Glass) Check(m Mask) error {
	for _, v := range [5]float32{g.Saturation, g.Brightness, g.Contrast, g.Bezel, g.Amount} {
		if !(v >= 0) || math.IsInf(float64(v), 0) {
			return fmt.Errorf("gpu: glass %+v has a value that is not zero or more", g)
		}
	}
	if m.Falloff != (Falloff{}) || !m.Merge.Empty() {
		return errors.New("gpu: glass through a mask with a falloff or a merge")
	}
	return m.Check()
}

// Mask is the mask a glass draw over dst uses: m, whose Keep is also the
// shape the bezel runs along, taking dst for a Keep it does not have.
func (g Glass) Mask(m Mask, dst image.Rectangle) Mask {
	if m.Keep.Rect.Empty() {
		m.Keep.Rect = dst
	}
	return m
}

// RefractPlan is the bend through a bezel along box, the arithmetic
// lys-ui's -lys-refract() runs, for a renderer without a shader.
func (g Glass) RefractPlan(box RoundRect) render.RefractPlan {
	r := box.Rect
	rad := max(min(box.Radius, float32(r.Dx())*0.5, float32(r.Dy())*0.5), 0)
	return render.RefractPlan{
		Box:      [4]float32{float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y)},
		Radii:    [4]float32{rad, rad, rad, rad},
		InvBezel: render.Reciprocal(g.Bezel),
		Disp:     g.Amount,
	}
}

// Adjust is the shader's glassAdjust over a premultiplied R, G, B, A in
// 0..255.
func (g Glass) Adjust(c [4]float32) [4]float32 {
	if c[3] <= 0 || (g.Saturation == 1 && g.Brightness == 1 && g.Contrast == 1) {
		return c
	}
	unit := func(v float32) float32 { return min(max(v, 0), 1) }
	a := c[3] / 255
	r, gr, b := c[0]/255/a, c[1]/255/a, c[2]/255/a
	luma := float32(0.2126*r) + float32(0.7152*gr) + float32(0.0722*b)
	step := func(v float32) float32 {
		v = unit(luma + float32((v-luma)*g.Saturation))
		v = unit(float32(v * g.Brightness))
		v = unit(float32((v-0.5)*g.Contrast) + 0.5)
		return float32(float32(v*a) * 255)
	}
	return [4]float32{step(r), step(gr), step(b), c[3]}
}

// DrawGlass blends src of t, scaled to dst and bent and adjusted by g, at
// alpha over dst's damaged part within m, always sampling linearly, so
// that a bent pixel lands between texels. The bezel runs along m's Keep,
// or along dst without one, and the adjusted colour goes through what
// SetColor set. The pipeline is built at the first draw.
func (p *Pass) DrawGlass(t *Texture, src FRect, dst image.Rectangle, alpha float32, m Mask, g Glass) {
	if !p.open("DrawGlass") {
		return
	}
	switch {
	case t == nil || t.closed:
		p.fail(errors.New("gpu: DrawGlass of a closed texture"))
		return
	case t.d != p.d:
		p.fail(errors.New("gpu: DrawGlass of another device's texture"))
		return
	case t.surf == &p.t.s:
		p.fail(errors.New("gpu: DrawGlass of the pass's own target"))
		return
	case t.yuv():
		p.fail(errors.New("gpu: DrawGlass of a YCbCr texture"))
		return
	case !(alpha >= 0 && alpha <= 1):
		p.fail(fmt.Errorf("gpu: alpha %v outside [0, 1]", alpha))
		return
	case !(src.W > 0 && src.H > 0 && src.X >= 0 && src.Y >= 0 && src.X+src.W <= float64(t.w) && src.Y+src.H <= float64(t.h)):
		p.fail(fmt.Errorf("gpu: source %+v outside a %dx%d texture", src, t.w, t.h))
		return
	}
	if err := g.Check(m); err != nil {
		p.fail(err)
		return
	}
	if dst.Empty() || alpha == 0 {
		return
	}
	set, err := t.set(filterLinear)
	if err != nil {
		p.fail(err)
		return
	}
	op := passOp{kind: opTexture, dst: dst, tex: t, set: set, layout: p.d.comp.layout}
	if p.colorSet != 0 {
		op.pipe, err = p.d.colorGlassPipeline(p.ct)
		op.layout, op.color = p.d.comp.color.layout, p.colorSet
	} else {
		op.pipe, err = p.d.glassPipeline(p.ct)
	}
	if err != nil {
		p.fail(err)
		return
	}
	dc := drawConstants{target: [4]float32{2: alpha}}
	dc.setMask(g.Mask(m, dst))
	u0, v0 := float32(src.X/float64(t.w)), float32(src.Y/float64(t.h))
	u1, v1 := float32((src.X+src.W)/float64(t.w)), float32((src.Y+src.H)/float64(t.h))
	dc.extra = [2][4]float32{{u0, v0, u1, v0}, {u0, v1, u1, v1}}
	dc.merge = [4]float32{g.Saturation, g.Brightness, g.Contrast}
	if g.Refracts() {
		dc.radii[2], dc.merge[3] = render.Reciprocal(g.Bezel), g.Amount
	}
	op.dc = dc
	p.add(op)
}
