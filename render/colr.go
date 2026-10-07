package render

import (
	"image"
	"math"
	"slices"

	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/font/opentype/tables"
)

// COLR color glyphs (v0 layers and the v1 paint graph), painted into a
// small premultiplied float layer in device pixels and cached as RGBA
// like a scaled bitmap strike.
//
// The model is the spec's: a paint fills the whole plane, PaintGlyph
// clips its child to a glyph outline, transforms move their child's
// space, layers composite source-over, and PaintComposite combines a
// source and a backdrop by mode. Clips are coverage masks, so nested
// glyph clips multiply. Variable paints apply their deltas at the
// face's variation coordinates. The non-separable blend modes (hue,
// saturation, color, luminosity) composite source-over.

// colrDepthLimit bounds paint-graph recursion (PaintColrGlyph cycles).
const colrDepthLimit = 64

// colrPainter paints one color glyph.
type colrPainter struct {
	face  *Typeface
	pal   []tables.ColorRecord
	fg    Color
	w, h  int
	depth int
}

// layer is a w*h premultiplied RGBA float buffer (nil: transparent).
type layer []float32

// renderCOLR paints gid's color glyph at scale (device px per font
// unit) for a pen at the integer origin plus (fx, fy), returning the
// image and its origin relative to the integer pen; ok is false when
// the glyph has no color data.
func renderCOLR(t *Typeface, gid font.GID, scale, fx, fy float64, fg Color) (*image.RGBA, int, int, bool) {
	paint, ok := t.face.GlyphDataColor(gid)
	if !ok || paint.Paint == nil {
		return nil, 0, 0, false
	}
	// Font units to device: y flips, the pen offset applies.
	base := Affine{A: scale, D: -scale, E: fx, F: fy}
	box, ok := colrBounds(t, gid, paint.Paint, base)
	if !ok {
		return nil, 0, 0, false
	}
	p := &colrPainter{face: t, fg: fg, w: box.W, h: box.H}
	if len(t.face.CPAL) > 0 {
		p.pal = t.face.CPAL[0]
	}
	// Buffer pixels: device minus the box origin.
	m := Translate(float64(-box.X), float64(-box.Y)).Mul(base)
	out := p.paint(paint.Paint, m, nil)
	img := image.NewRGBA(image.Rect(0, 0, box.W, box.H))
	for i := 0; out != nil && i < box.W*box.H; i++ {
		a := clamp01(out[i*4+3])
		img.Pix[i*4+0] = uint8(clamp01(out[i*4+0])*255 + 0.5)
		img.Pix[i*4+1] = uint8(clamp01(out[i*4+1])*255 + 0.5)
		img.Pix[i*4+2] = uint8(clamp01(out[i*4+2])*255 + 0.5)
		img.Pix[i*4+3] = uint8(a*255 + 0.5)
	}
	return img, box.X, box.Y, true
}

// colrBounds is the device box a color glyph paints in: its ClipBox
// when the font has one, else the union of the outlines its PaintGlyph
// nodes clip to (and the base glyph's own), padded a pixel.
func colrBounds(t *Typeface, gid font.GID, root tables.PaintTable, base Affine) (Rect, bool) {
	var fr [4]float64 // xmin ymin xmax ymax in font units
	found := false
	if colr := t.face.COLR; colr != nil {
		if cb, ok := colr.ClipList.Search(tables.GlyphID(gid)); ok {
			switch c := cb.(type) {
			case tables.ClipBoxFormat1:
				fr, found = [4]float64{float64(c.XMin), float64(c.YMin), float64(c.XMax), float64(c.YMax)}, true
			case tables.ClipBoxFormat2:
				fr, found = [4]float64{float64(c.XMin), float64(c.YMin), float64(c.XMax), float64(c.YMax)}, true
			}
		}
	}
	if !found {
		fr = [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
		add := func(g font.GID) {
			if o, ok := t.face.GlyphDataOutline(g); ok {
				for _, seg := range o.Segments {
					for _, a := range seg.Args {
						fr[0], fr[1] = math.Min(fr[0], float64(a.X)), math.Min(fr[1], float64(a.Y))
						fr[2], fr[3] = math.Max(fr[2], float64(a.X)), math.Max(fr[3], float64(a.Y))
					}
				}
			}
		}
		add(gid)
		colrGlyphs(t, root, 0, add)
		found = fr[0] <= fr[2]
	}
	if !found {
		return Rect{}, false
	}
	// Transforms inside the graph can move ink past these bounds; a
	// generous margin covers the common rotations and offsets.
	pad := (fr[2] - fr[0] + fr[3] - fr[1]) * 0.1
	r := base.MapBounds(Rect{
		X: int(math.Floor(fr[0] - pad)), Y: int(math.Floor(fr[1] - pad)),
		W: int(math.Ceil(fr[2]-fr[0]+2*pad)) + 1, H: int(math.Ceil(fr[3]-fr[1]+2*pad)) + 1,
	})
	r = Rect{X: r.X - 1, Y: r.Y - 1, W: r.W + 2, H: r.H + 2}
	if r.W <= 0 || r.H <= 0 || r.W > 4096 || r.H > 4096 {
		return Rect{}, false
	}
	return r, true
}

// colrGlyphs visits the glyph IDs a paint graph clips to.
func colrGlyphs(t *Typeface, node tables.PaintTable, depth int, visit func(font.GID)) {
	if depth > colrDepthLimit {
		return
	}
	switch n := node.(type) {
	case tables.PaintColrLayersResolved:
		for _, l := range n {
			visit(font.GID(l.GlyphID))
		}
	case tables.PaintColrLayers:
		if layers, err := t.face.COLR.LayerList.Resolve(n); err == nil {
			for _, c := range layers {
				colrGlyphs(t, c, depth+1, visit)
			}
		}
	case tables.PaintGlyph:
		visit(font.GID(n.GlyphID))
		colrGlyphs(t, n.Paint, depth+1, visit)
	case tables.PaintColrGlyph:
		if p, ok := t.face.COLR.Search(n.GlyphID); ok {
			colrGlyphs(t, p, depth+1, visit)
		}
	case tables.PaintComposite:
		colrGlyphs(t, n.SourcePaint, depth+1, visit)
		colrGlyphs(t, n.BackdropPaint, depth+1, visit)
	default:
		if child := colrChild(node); child != nil {
			colrGlyphs(t, child, depth+1, visit)
		}
	}
}

// colrChild is a transform node's child paint.
func colrChild(node tables.PaintTable) tables.PaintTable {
	switch n := node.(type) {
	case tables.PaintTransform:
		return n.Paint
	case tables.PaintVarTransform:
		return n.Paint
	case tables.PaintTranslate:
		return n.Paint
	case tables.PaintVarTranslate:
		return n.Paint
	case tables.PaintScale:
		return n.Paint
	case tables.PaintVarScale:
		return n.Paint
	case tables.PaintScaleAroundCenter:
		return n.Paint
	case tables.PaintVarScaleAroundCenter:
		return n.Paint
	case tables.PaintScaleUniform:
		return n.Paint
	case tables.PaintVarScaleUniform:
		return n.Paint
	case tables.PaintScaleUniformAroundCenter:
		return n.Paint
	case tables.PaintVarScaleUniformAroundCenter:
		return n.Paint
	case tables.PaintRotate:
		return n.Paint
	case tables.PaintVarRotate:
		return n.Paint
	case tables.PaintRotateAroundCenter:
		return n.Paint
	case tables.PaintVarRotateAroundCenter:
		return n.Paint
	case tables.PaintSkew:
		return n.Paint
	case tables.PaintVarSkew:
		return n.Paint
	case tables.PaintSkewAroundCenter:
		return n.Paint
	case tables.PaintVarSkewAroundCenter:
		return n.Paint
	}
	return nil
}

// delta is the variation delta for varIndexBase+k at the face's
// coordinates, 0 for a static face or no variation.
func (p *colrPainter) delta(base uint32, k int) float64 {
	colr := p.face.face.COLR
	coords := p.face.face.Coords()
	if base == 0xFFFFFFFF || colr == nil || colr.ItemVariationStore == nil || len(coords) == 0 {
		return 0
	}
	idx := base + uint32(k)
	var vi tables.VariationStoreIndex
	if colr.VarIndexMap != nil {
		vi = colr.VarIndexMap.Index(tables.GlyphID(idx))
	} else {
		vi = tables.VariationStoreIndex{DeltaSetOuter: uint16(idx >> 16), DeltaSetInner: uint16(idx)}
	}
	return float64(colr.ItemVariationStore.GetDelta(vi, coords))
}

// f214 converts a 2.14 fixed value.
func f214(v tables.Fixed214) float64 { return float64(v) / 16384 }

// varF214 is a 2.14 field plus its delta (deltas are 2.14 units too).
func (p *colrPainter) varF214(v tables.Fixed214, base uint32, k int) float64 {
	return f214(v) + p.delta(base, k)/16384
}

// varFU is a font-unit field plus its delta.
func (p *colrPainter) varFU(v int16, base uint32, k int) float64 {
	return float64(v) + p.delta(base, k)
}

// angle converts a COLR angle (180° per 1.0, counter-clockwise in font
// space) to a font-space rotation.
func colrRotate(turns float64) Affine {
	s, c := math.Sincos(turns * math.Pi)
	return Affine{A: c, B: s, C: -s, D: c}
}

// colrSkew is the skew by x and y angles (180° per 1.0); positive x
// skew leans counter-clockwise, as the spec's matrix has it.
func colrSkew(xTurns, yTurns float64) Affine {
	return Affine{A: 1, B: math.Tan(yTurns * math.Pi), C: -math.Tan(xTurns * math.Pi), D: 1}
}

// paint renders node under m (font units to buffer pixels) within
// clip (a coverage mask, nil for none).
func (p *colrPainter) paint(node tables.PaintTable, m Affine, clip []float32) layer {
	if p.depth > colrDepthLimit {
		return nil
	}
	p.depth++
	defer func() { p.depth-- }()
	switch n := node.(type) {
	case tables.PaintColrLayersResolved:
		var out layer
		for _, l := range n {
			mask := p.glyphMask(font.GID(l.GlyphID), m, clip)
			out = p.over(out, p.solid(l.PaletteIndex, 1, mask))
		}
		return out
	case tables.PaintColrLayers:
		layers, err := p.face.face.COLR.LayerList.Resolve(n)
		if err != nil {
			return nil
		}
		var out layer
		for _, c := range layers {
			out = p.over(out, p.paint(c, m, clip))
		}
		return out
	case tables.PaintSolid:
		return p.solid(n.PaletteIndex, f214(n.Alpha), clip)
	case tables.PaintVarSolid:
		return p.solid(n.PaletteIndex, p.varF214(n.Alpha, n.VarIndexBase, 0), clip)
	case tables.PaintLinearGradient:
		return p.linear(stops(n.ColorLine), n.ColorLine.Extend,
			[6]float64{float64(n.X0), float64(n.Y0), float64(n.X1), float64(n.Y1), float64(n.X2), float64(n.Y2)}, m, clip)
	case tables.PaintVarLinearGradient:
		b := n.VarIndexBase
		return p.linear(p.varStops(n.ColorLine), n.ColorLine.Extend,
			[6]float64{p.varFU(n.X0, b, 0), p.varFU(n.Y0, b, 1), p.varFU(n.X1, b, 2), p.varFU(n.Y1, b, 3), p.varFU(n.X2, b, 4), p.varFU(n.Y2, b, 5)}, m, clip)
	case tables.PaintRadialGradient:
		return p.radial(stops(n.ColorLine), n.ColorLine.Extend,
			[6]float64{float64(n.X0), float64(n.Y0), float64(n.Radius0), float64(n.X1), float64(n.Y1), float64(n.Radius1)}, m, clip)
	case tables.PaintVarRadialGradient:
		b := n.VarIndexBase
		return p.radial(p.varStops(n.ColorLine), n.ColorLine.Extend,
			[6]float64{
				p.varFU(n.X0, b, 0), p.varFU(n.Y0, b, 1), float64(n.Radius0) + p.delta(b, 2),
				p.varFU(n.X1, b, 3), p.varFU(n.Y1, b, 4), float64(n.Radius1) + p.delta(b, 5),
			}, m, clip)
	case tables.PaintSweepGradient:
		return p.sweep(stops(n.ColorLine), n.ColorLine.Extend, float64(n.CenterX), float64(n.CenterY),
			(f214(n.StartAngle)+1)*180, (f214(n.EndAngle)+1)*180, m, clip)
	case tables.PaintVarSweepGradient:
		b := n.VarIndexBase
		return p.sweep(p.varStops(n.ColorLine), n.ColorLine.Extend, p.varFU(n.CenterX, b, 0), p.varFU(n.CenterY, b, 1),
			(p.varF214(n.StartAngle, b, 2)+1)*180, (p.varF214(n.EndAngle, b, 3)+1)*180, m, clip)
	case tables.PaintGlyph:
		return p.paint(n.Paint, m, p.glyphMask(font.GID(n.GlyphID), m, clip))
	case tables.PaintColrGlyph:
		child, ok := p.face.face.COLR.Search(n.GlyphID)
		if !ok {
			return nil
		}
		return p.paint(child, m, clip)
	case tables.PaintComposite:
		return p.composite(n.CompositeMode, p.paint(n.SourcePaint, m, clip), p.paint(n.BackdropPaint, m, clip))
	}
	if t, ok := p.transform(node); ok {
		return p.paint(colrChild(node), m.Mul(t), clip)
	}
	return nil
}

// transform is a transform node's font-space map.
func (p *colrPainter) transform(node tables.PaintTable) (Affine, bool) {
	switch n := node.(type) {
	case tables.PaintTransform:
		a := n.Transform
		return Affine{A: float64(a.Xx), B: float64(a.Yx), C: float64(a.Xy), D: float64(a.Yy), E: float64(a.Dx), F: float64(a.Dy)}, true
	case tables.PaintVarTransform:
		a, b := n.Transform, n.Transform.VarIndexBase
		d := func(v float32, k int) float64 { return float64(v) + p.delta(b, k)/65536 }
		return Affine{A: d(a.Xx, 0), B: d(a.Yx, 1), C: d(a.Xy, 2), D: d(a.Yy, 3), E: d(a.Dx, 4), F: d(a.Dy, 5)}, true
	case tables.PaintTranslate:
		return Translate(float64(n.Dx), float64(n.Dy)), true
	case tables.PaintVarTranslate:
		return Translate(p.varFU(n.Dx, n.VarIndexBase, 0), p.varFU(n.Dy, n.VarIndexBase, 1)), true
	case tables.PaintScale:
		return Scale(f214(n.ScaleX), f214(n.ScaleY)), true
	case tables.PaintVarScale:
		return Scale(p.varF214(n.ScaleX, n.VarIndexBase, 0), p.varF214(n.ScaleY, n.VarIndexBase, 1)), true
	case tables.PaintScaleAroundCenter:
		return Scale(f214(n.ScaleX), f214(n.ScaleY)).About(float64(n.CenterX), float64(n.CenterY)), true
	case tables.PaintVarScaleAroundCenter:
		b := n.VarIndexBase
		return Scale(p.varF214(n.ScaleX, b, 0), p.varF214(n.ScaleY, b, 1)).About(p.varFU(n.CenterX, b, 2), p.varFU(n.CenterY, b, 3)), true
	case tables.PaintScaleUniform:
		s := f214(n.Scale)
		return Scale(s, s), true
	case tables.PaintVarScaleUniform:
		s := p.varF214(n.Scale, n.VarIndexBase, 0)
		return Scale(s, s), true
	case tables.PaintScaleUniformAroundCenter:
		s := f214(n.Scale)
		return Scale(s, s).About(float64(n.CenterX), float64(n.CenterY)), true
	case tables.PaintVarScaleUniformAroundCenter:
		b := n.VarIndexBase
		s := p.varF214(n.Scale, b, 0)
		return Scale(s, s).About(p.varFU(n.CenterX, b, 1), p.varFU(n.CenterY, b, 2)), true
	case tables.PaintRotate:
		return colrRotate(f214(n.Angle)), true
	case tables.PaintVarRotate:
		return colrRotate(p.varF214(n.Angle, n.VarIndexBase, 0)), true
	case tables.PaintRotateAroundCenter:
		return colrRotate(f214(n.Angle)).About(float64(n.CenterX), float64(n.CenterY)), true
	case tables.PaintVarRotateAroundCenter:
		b := n.VarIndexBase
		return colrRotate(p.varF214(n.Angle, b, 0)).About(p.varFU(n.CenterX, b, 1), p.varFU(n.CenterY, b, 2)), true
	case tables.PaintSkew:
		return colrSkew(f214(n.XSkewAngle), f214(n.YSkewAngle)), true
	case tables.PaintVarSkew:
		return colrSkew(p.varF214(n.XSkewAngle, n.VarIndexBase, 0), p.varF214(n.YSkewAngle, n.VarIndexBase, 1)), true
	case tables.PaintSkewAroundCenter:
		return colrSkew(f214(n.XSkewAngle), f214(n.YSkewAngle)).About(float64(n.CenterX), float64(n.CenterY)), true
	case tables.PaintVarSkewAroundCenter:
		b := n.VarIndexBase
		return colrSkew(p.varF214(n.XSkewAngle, b, 0), p.varF214(n.YSkewAngle, b, 1)).About(p.varFU(n.CenterX, b, 2), p.varFU(n.CenterY, b, 3)), true
	}
	return Affine{}, false
}

// glyphMask is gid's outline coverage under m, intersected with clip.
func (p *colrPainter) glyphMask(gid font.GID, m Affine, clip []float32) []float32 {
	mask := make([]float32, p.w*p.h)
	o, ok := p.face.face.GlyphDataOutline(gid)
	if !ok || len(o.Segments) == 0 {
		return mask
	}
	g := rasterOutlineXform(o, m)
	for y := range g.h {
		by := y + g.oy
		if by < 0 || by >= p.h {
			continue
		}
		for x := range g.w {
			bx := x + g.ox
			if bx < 0 || bx >= p.w {
				continue
			}
			v := float32(g.mask[y*g.w+x]) / 255
			if clip != nil {
				v *= clip[by*p.w+bx]
			}
			mask[by*p.w+bx] = v
		}
	}
	return mask
}

// rgba is a premultiplied float color.
type rgba [4]float64

// color resolves a palette entry (0xFFFF: the text color) at alpha,
// premultiplied.
func (p *colrPainter) color(idx uint16, alpha float64) rgba {
	var r, g, b, a float64
	switch {
	case idx == 0xFFFF:
		c := p.fg
		if c.A() > 0 { // Color is premultiplied; unpremultiply
			fa := float64(c.A())
			r, g, b, a = float64(c.R())/fa, float64(c.G())/fa, float64(c.B())/fa, fa/255
		}
	case int(idx) < len(p.pal):
		c := p.pal[idx]
		r, g, b, a = float64(c.Red)/255, float64(c.Green)/255, float64(c.Blue)/255, float64(c.Alpha)/255
	}
	a *= alpha
	return rgba{r * a, g * a, b * a, a}
}

// fill paints every covered pixel with at(x, y) (buffer pixel center).
func (p *colrPainter) fill(clip []float32, at func(x, y float64) rgba) layer {
	out := make(layer, p.w*p.h*4)
	for y := range p.h {
		for x := range p.w {
			cov := float32(1)
			if clip != nil {
				if cov = clip[y*p.w+x]; cov == 0 {
					continue
				}
			}
			c := at(float64(x)+0.5, float64(y)+0.5)
			i := (y*p.w + x) * 4
			out[i] = float32(c[0]) * cov
			out[i+1] = float32(c[1]) * cov
			out[i+2] = float32(c[2]) * cov
			out[i+3] = float32(c[3]) * cov
		}
	}
	return out
}

// solid fills with one palette color.
func (p *colrPainter) solid(idx uint16, alpha float64, clip []float32) layer {
	c := p.color(idx, alpha)
	return p.fill(clip, func(float64, float64) rgba { return c })
}

// stop is one resolved color stop.
type stop struct {
	at    float64
	idx   uint16
	alpha float64
}

// stops resolves a static color line, sorted.
func stops(cl tables.ColorLine) []stop {
	out := make([]stop, len(cl.ColorStops))
	for i, s := range cl.ColorStops {
		out[i] = stop{f214(s.StopOffset), s.PaletteIndex, f214(s.Alpha)}
	}
	slices.SortStableFunc(out, func(a, b stop) int { return cmpFloat(a.at, b.at) })
	return out
}

// varStops resolves a variable color line, sorted.
func (p *colrPainter) varStops(cl tables.VarColorLine) []stop {
	out := make([]stop, len(cl.ColorStops))
	for i, s := range cl.ColorStops {
		out[i] = stop{p.varF214(s.StopOffset, s.VarIndexBase, 0), s.PaletteIndex, p.varF214(s.Alpha, s.VarIndexBase, 1)}
	}
	slices.SortStableFunc(out, func(a, b stop) int { return cmpFloat(a.at, b.at) })
	return out
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// colorAt evaluates the color line at t under its extend mode,
// interpolating premultiplied.
func (p *colrPainter) colorAt(ss []stop, ext tables.Extend, t float64) rgba {
	if len(ss) == 0 {
		return rgba{}
	}
	lo, hi := ss[0].at, ss[len(ss)-1].at
	t = extendT(t, lo, hi, Extend(ext)) // the enums share their order
	if t <= lo {
		return p.color(ss[0].idx, ss[0].alpha)
	}
	if t >= hi {
		last := ss[len(ss)-1]
		return p.color(last.idx, last.alpha)
	}
	for i := 1; i < len(ss); i++ {
		if t <= ss[i].at {
			a, b := ss[i-1], ss[i]
			f := 0.0
			if b.at > a.at {
				f = (t - a.at) / (b.at - a.at)
			}
			ca, cb := p.color(a.idx, a.alpha), p.color(b.idx, b.alpha)
			return rgba{ca[0] + (cb[0]-ca[0])*f, ca[1] + (cb[1]-ca[1])*f, ca[2] + (cb[2]-ca[2])*f, ca[3] + (cb[3]-ca[3])*f}
		}
	}
	return rgba{}
}

// fontPoint maps a buffer pixel back to font space through m.
func fontPoint(inv Affine, x, y float64) (float64, float64) { return inv.Apply(x, y) }

// linear is the spec's linear gradient: p0 to p1, the direction
// perpendicular to the p0-p2 line.
func (p *colrPainter) linear(ss []stop, ext tables.Extend, pts [6]float64, m Affine, clip []float32) layer {
	inv, ok := m.Invert()
	if !ok {
		return nil
	}
	x0, y0, x1, y1, x2, y2 := pts[0], pts[1], pts[2], pts[3], pts[4], pts[5]
	// Project p1 onto the normal of p0p2: p3 is the effective end.
	nx, ny := y2-y0, -(x2 - x0)
	x3, y3 := x1, y1
	if n2 := nx*nx + ny*ny; n2 > 0 {
		d := ((x1-x0)*nx + (y1-y0)*ny) / n2
		x3, y3 = x0+nx*d, y0+ny*d
	}
	dx, dy := x3-x0, y3-y0
	l2 := dx*dx + dy*dy
	return p.fill(clip, func(x, y float64) rgba {
		fx, fy := fontPoint(inv, x, y)
		t := 0.0
		if l2 > 0 {
			t = ((fx-x0)*dx + (fy-y0)*dy) / l2
		}
		return p.colorAt(ss, ext, t)
	})
}

// radial is the two-point conical gradient between circles (c0, r0)
// and (c1, r1): the largest t whose circle passes the point.
func (p *colrPainter) radial(ss []stop, ext tables.Extend, v [6]float64, m Affine, clip []float32) layer {
	inv, ok := m.Invert()
	if !ok {
		return nil
	}
	cx0, cy0, r0, cx1, cy1, r1 := v[0], v[1], v[2], v[3], v[4], v[5]
	cdx, cdy, dr := cx1-cx0, cy1-cy0, r1-r0
	a := cdx*cdx + cdy*cdy - dr*dr
	return p.fill(clip, func(x, y float64) rgba {
		fx, fy := fontPoint(inv, x, y)
		pdx, pdy := fx-cx0, fy-cy0
		b := pdx*cdx + pdy*cdy + r0*dr
		c := pdx*pdx + pdy*pdy - r0*r0
		var t float64
		if math.Abs(a) < 1e-9 {
			if b == 0 {
				return rgba{}
			}
			t = c / (2 * b)
		} else {
			disc := b*b - a*c
			if disc < 0 {
				return rgba{}
			}
			sq := math.Sqrt(disc)
			t = (b + sq) / a
			if r0+t*dr < 0 {
				t = (b - sq) / a
			}
		}
		if r0+t*dr < 0 {
			return rgba{}
		}
		return p.colorAt(ss, ext, t)
	})
}

// sweep is the sweep gradient around (cx, cy) from start to end
// degrees, counter-clockwise in font space.
func (p *colrPainter) sweep(ss []stop, ext tables.Extend, cx, cy, start, end float64, m Affine, clip []float32) layer {
	inv, ok := m.Invert()
	if !ok {
		return nil
	}
	return p.fill(clip, func(x, y float64) rgba {
		fx, fy := fontPoint(inv, x, y)
		ang := math.Atan2(fy-cy, fx-cx) * 180 / math.Pi
		if ang < 0 {
			ang += 360
		}
		t := 0.0
		if end != start {
			t = (ang - start) / (end - start)
		}
		return p.colorAt(ss, ext, t)
	})
}

// over composites src over dst.
func (p *colrPainter) over(dst, src layer) layer {
	return p.composite(tables.CompositeSrcOver, src, dst)
}

// composite combines src over the backdrop dst by mode.
func (p *colrPainter) composite(mode tables.CompositeMode, src, dst layer) layer {
	if src == nil && dst == nil {
		return nil
	}
	if src == nil {
		src = make(layer, p.w*p.h*4)
	}
	if dst == nil {
		dst = make(layer, p.w*p.h*4)
	}
	out := make(layer, len(src))
	for i := 0; i < len(src); i += 4 {
		var s, d [4]float32
		copy(s[:], src[i:i+4])
		copy(d[:], dst[i:i+4])
		r := compositePixel(mode, s, d)
		copy(out[i:i+4], r[:])
	}
	return out
}

// compositePixel applies one mode to premultiplied s over d.
func compositePixel(mode tables.CompositeMode, s, d [4]float32) [4]float32 {
	sa, da := s[3], d[3]
	pd := func(fs, fd float32) [4]float32 { // Porter-Duff with factors
		return [4]float32{s[0]*fs + d[0]*fd, s[1]*fs + d[1]*fd, s[2]*fs + d[2]*fd, sa*fs + da*fd}
	}
	switch mode {
	case tables.CompositeClear:
		return [4]float32{}
	case tables.CompositeSrc:
		return s
	case tables.CompositeDest:
		return d
	case tables.CompositeDestOver:
		return pd(1-da, 1)
	case tables.CompositeSrcIn:
		return pd(da, 0)
	case tables.CompositeDestIn:
		return pd(0, sa)
	case tables.CompositeSrcOut:
		return pd(1-da, 0)
	case tables.CompositeDestOut:
		return pd(0, 1-sa)
	case tables.CompositeSrcAtop:
		return pd(da, 1-sa)
	case tables.CompositeDestAtop:
		return pd(1-da, sa)
	case tables.CompositeXor:
		return pd(1-da, 1-sa)
	case tables.CompositePlus:
		r := pd(1, 1)
		for k := range r {
			r[k] = min(r[k], 1)
		}
		return r
	}
	if blend := separableBlend(mode); blend != nil {
		// The W3C compositing formula: co = cs(1-ab) + cb(1-as) + as·ab·B(cb/ab, cs/as).
		var out [4]float32
		for k := range 3 {
			var cs, cb float32
			if sa > 0 {
				cs = s[k] / sa
			}
			if da > 0 {
				cb = d[k] / da
			}
			out[k] = s[k]*(1-da) + d[k]*(1-sa) + sa*da*blend(cb, cs)
		}
		out[3] = sa + da - sa*da
		return out
	}
	return pd(1, 1-sa) // source over (and the non-separable modes)
}

// separableBlend is a separable blend mode's B(cb, cs), nil for the
// rest.
func separableBlend(mode tables.CompositeMode) func(cb, cs float32) float32 {
	switch mode {
	case tables.CompositeScreen:
		return func(cb, cs float32) float32 { return cb + cs - cb*cs }
	case tables.CompositeOverlay:
		return func(cb, cs float32) float32 { return hardLight(cs, cb) }
	case tables.CompositeDarken:
		return func(cb, cs float32) float32 { return min(cb, cs) }
	case tables.CompositeLighten:
		return func(cb, cs float32) float32 { return max(cb, cs) }
	case tables.CompositeColorDodge:
		return func(cb, cs float32) float32 {
			switch {
			case cb == 0:
				return 0
			case cs >= 1:
				return 1
			}
			return min(1, cb/(1-cs))
		}
	case tables.CompositeColorBurn:
		return func(cb, cs float32) float32 {
			switch {
			case cb >= 1:
				return 1
			case cs <= 0:
				return 0
			}
			return 1 - min(1, (1-cb)/cs)
		}
	case tables.CompositeHardLight:
		return func(cb, cs float32) float32 { return hardLight(cb, cs) }
	case tables.CompositeSoftLight:
		return func(cb, cs float32) float32 {
			if cs <= 0.5 {
				return cb - (1-2*cs)*cb*(1-cb)
			}
			var dd float32
			if cb <= 0.25 {
				dd = ((16*cb-12)*cb + 4) * cb
			} else {
				dd = float32(math.Sqrt(float64(cb)))
			}
			return cb + (2*cs-1)*(dd-cb)
		}
	case tables.CompositeDifference:
		return func(cb, cs float32) float32 { return float32(math.Abs(float64(cb - cs))) }
	case tables.CompositeExclusion:
		return func(cb, cs float32) float32 { return cb + cs - 2*cb*cs }
	case tables.CompositeMultiply:
		return func(cb, cs float32) float32 { return cb * cs }
	}
	return nil
}

// hardLight is the W3C hard-light blend.
func hardLight(cb, cs float32) float32 {
	if cs <= 0.5 {
		return cb * 2 * cs
	}
	s := 2*cs - 1
	return cb + s - cb*s
}

func clamp01(v float32) float32 { return min(max(v, 0), 1) }
