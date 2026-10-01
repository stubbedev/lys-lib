package render

import "math"

// Affine is a 2-D affine map in device pixels: (x, y) goes to
// (A·x + C·y + E, B·x + D·y + F). The zero value maps everything to the
// origin; Identity leaves points where they are.
type Affine struct{ A, B, C, D, E, F float64 }

// Identity is the affine map that moves nothing.
var Identity = Affine{A: 1, D: 1}

// Translate is the map moving by (dx, dy).
func Translate(dx, dy float64) Affine { return Affine{A: 1, D: 1, E: dx, F: dy} }

// Scale is the map scaling about the origin.
func Scale(sx, sy float64) Affine { return Affine{A: sx, D: sy} }

// Rotate is the map rotating about the origin by deg degrees, clockwise
// on screen (y grows downward), as a GSK snapshot rotation does.
func Rotate(deg float64) Affine {
	s, c := math.Sincos(deg * math.Pi / 180)
	return Affine{A: c, B: s, C: -s, D: c}
}

// Mul is m∘n: the map applying n first, then m. A chain of snapshot
// operations t1, t2, t3 on a point is t1.Mul(t2).Mul(t3).
func (m Affine) Mul(n Affine) Affine {
	return Affine{
		A: m.A*n.A + m.C*n.B,
		B: m.B*n.A + m.D*n.B,
		C: m.A*n.C + m.C*n.D,
		D: m.B*n.C + m.D*n.D,
		E: m.A*n.E + m.C*n.F + m.E,
		F: m.B*n.E + m.D*n.F + m.F,
	}
}

// About is m applied about the pivot (px, py) instead of the origin.
func (m Affine) About(px, py float64) Affine {
	return Translate(px, py).Mul(m).Mul(Translate(-px, -py))
}

// Apply maps the point (x, y).
func (m Affine) Apply(x, y float64) (float64, float64) {
	return m.A*x + m.C*y + m.E, m.B*x + m.D*y + m.F
}

// Invert returns the inverse map; false when m collapses an axis.
func (m Affine) Invert() (Affine, bool) {
	det := m.A*m.D - m.B*m.C
	if det == 0 || math.IsNaN(det) || math.IsInf(det, 0) {
		return Affine{}, false
	}
	return Affine{
		A: m.D / det,
		B: -m.B / det,
		C: -m.C / det,
		D: m.A / det,
		E: (m.C*m.F - m.D*m.E) / det,
		F: (m.B*m.E - m.A*m.F) / det,
	}, true
}

// MapBounds is the smallest pixel rect covering r's image under m.
func (m Affine) MapBounds(r Rect) Rect {
	if r.Empty() {
		return Rect{}
	}
	x0, y0 := math.Inf(1), math.Inf(1)
	x1, y1 := math.Inf(-1), math.Inf(-1)
	for _, p := range [4][2]float64{
		{float64(r.X), float64(r.Y)},
		{float64(r.X + r.W), float64(r.Y)},
		{float64(r.X), float64(r.Y + r.H)},
		{float64(r.X + r.W), float64(r.Y + r.H)},
	} {
		x, y := m.Apply(p[0], p[1])
		x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x), max(y1, y)
	}
	if math.IsNaN(x0) || math.IsNaN(y0) || math.IsNaN(x1) || math.IsNaN(y1) {
		return Rect{}
	}
	l, t := int(math.Floor(x0)), int(math.Floor(y0))
	return Rect{X: l, Y: t, W: int(math.Ceil(x1)) - l, H: int(math.Ceil(y1)) - t}
}

// isTranslate reports a whole-pixel translation, which composites as a
// straight copy.
func (m Affine) isTranslate() (dx, dy int, ok bool) {
	if m.A != 1 || m.B != 0 || m.C != 0 || m.D != 1 || m.E != math.Trunc(m.E) || m.F != math.Trunc(m.F) {
		return 0, 0, false
	}
	return int(m.E), int(m.F), true
}

// Layer is an offscreen buffer shaped like a canvas: the same device
// size and scale, so a widget subtree paints into it exactly as it
// would onto the canvas, to be drawn back transformed by Composite.
type Layer struct {
	buf []byte
	cv  *Canvas
}

// Layer returns a layer the size and scale of c whose region (device
// pixels) is transparent and is the layer's clip; drawing lands only
// there. old's buffer is reused when it is the right size, so a layer
// kept across frames allocates once.
func (c *Canvas) Layer(old *Layer, region Rect) *Layer {
	n := c.stride * c.h
	l := old
	if l == nil || len(l.buf) != n || l.cv.w != c.w || l.cv.h != c.h || l.cv.num != c.num || l.cv.denom != c.denom {
		l = &Layer{buf: make([]byte, n)}
		l.cv = NewScaled(l.buf, c.stride, c.w, c.h, c.num, c.denom)
	}
	l.cv.alpha, l.cv.bright = 1, 0
	l.cv.rescaleAlpha()
	l.cv.clip = l.cv.Rect()
	l.cv.ClearDevice(region, 0)
	l.cv.clip = l.cv.Rect().Intersect(region)
	return l
}

// Canvas is the layer's canvas to paint into.
func (l *Layer) Canvas() *Canvas { return l.cv }

// Composite draws the src device rect of l onto c through m (layer
// device pixels to canvas device pixels), bilinearly filtered, blended
// through c's opacity times alpha and clipped to c's clip. Pixels of l
// outside src count as transparent, so the drawn edge antialiases. A
// whole-pixel translation copies without filtering.
func (c *Canvas) Composite(l *Layer, src Rect, m Affine, alpha float64) {
	if l == nil || src.Empty() {
		return
	}
	src = src.Intersect(l.cv.Rect())
	prev := c.PushAlpha(alpha)
	defer c.PopAlpha(prev)
	if c.alphaScale == 0 {
		return
	}
	dst := m.MapBounds(src).Intersect(c.clip).Intersect(c.Rect())
	if dst.Empty() {
		return
	}
	if dx, dy, ok := m.isTranslate(); ok {
		for y := dst.Y; y < dst.Y+dst.H; y++ {
			for x := dst.X; x < dst.X+dst.W; x++ {
				sx, sy := x-dx, y-dy
				if sx < src.X || sy < src.Y || sx >= src.X+src.W || sy >= src.Y+src.H {
					continue
				}
				if col := l.cv.get(sx, sy); col != 0 {
					c.blend(x, y, col)
				}
			}
		}
		return
	}
	inv, ok := m.Invert()
	if !ok {
		return
	}
	for y := dst.Y; y < dst.Y+dst.H; y++ {
		for x := dst.X; x < dst.X+dst.W; x++ {
			sx, sy := inv.Apply(float64(x)+0.5, float64(y)+0.5)
			if col := l.sample(src, sx-0.5, sy-0.5); col != 0 {
				c.blend(x, y, col)
			}
		}
	}
}

// sample is the bilinear blend of the four pixels around (x, y), each
// premultiplied channel weighted alike so the result stays
// premultiplied; pixels outside src are transparent.
func (l *Layer) sample(src Rect, x, y float64) Color {
	x0, y0 := math.Floor(x), math.Floor(y)
	fx, fy := x-x0, y-y0
	ix, iy := int(x0), int(y0)
	if ix < src.X-1 || iy < src.Y-1 || ix >= src.X+src.W || iy >= src.Y+src.H {
		return 0
	}
	at := func(px, py int) [4]float64 {
		if px < src.X || py < src.Y || px >= src.X+src.W || py >= src.Y+src.H {
			return [4]float64{}
		}
		col := l.cv.get(px, py)
		return [4]float64{float64(col.A()), float64(col.R()), float64(col.G()), float64(col.B())}
	}
	p00, p10, p01, p11 := at(ix, iy), at(ix+1, iy), at(ix, iy+1), at(ix+1, iy+1)
	var ch [4]uint32
	for i := range ch {
		top := p00[i]*(1-fx) + p10[i]*fx
		bottom := p01[i]*(1-fx) + p11[i]*fx
		ch[i] = uint32(min(255, max(0, math.Round(top*(1-fy)+bottom*fy))))
	}
	return Color(ch[0]<<24 | ch[1]<<16 | ch[2]<<8 | ch[3])
}
