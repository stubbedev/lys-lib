package render

import "math"

// GradientKind is a gradient's geometry.
type GradientKind uint8

const (
	// GradientLinear runs along a line at an angle (CSS linear-gradient).
	GradientLinear GradientKind = iota
	// GradientRadial grows from a center out to an ellipse or circle
	// (CSS radial-gradient).
	GradientRadial
	// GradientConic sweeps around a center (CSS conic-gradient).
	GradientConic
)

// Extend says what lies beyond a gradient's first and last stops.
type Extend uint8

const (
	// ExtendPad continues the end colors (CSS's plain gradients).
	ExtendPad Extend = iota
	// ExtendRepeat repeats the stop range (CSS's repeating-*).
	ExtendRepeat
	// ExtendReflect mirrors the stop range back and forth (COLR).
	ExtendReflect
)

// RadialSize is a radial gradient's ending-shape size, the CSS
// keywords plus explicit radii.
type RadialSize uint8

const (
	// FarthestCorner reaches the farthest corner (the CSS default).
	FarthestCorner RadialSize = iota
	// ClosestSide touches the nearest side.
	ClosestSide
	// ClosestCorner reaches the nearest corner.
	ClosestCorner
	// FarthestSide touches the farthest side.
	FarthestSide
	// RadiusExplicit uses Gradient.RadiusX and RadiusY.
	RadiusExplicit
)

// Gradient is one gradient fill: its geometry, its stops (sorted by
// position; positions may repeat for a hard stop), and its extend
// mode. The zero value is a padded linear gradient pointing up; build
// one with Linear, Radial or Conic.
type Gradient struct {
	Kind   GradientKind
	Stops  []GradientStop
	Extend Extend
	// Angle is the linear direction, or the conic start, in degrees on
	// CSS's compass: 0 up, 90 right, clockwise.
	Angle float64
	// CenterX and CenterY place a radial or conic center as fractions
	// of the box (0.5 is its middle).
	CenterX, CenterY float64
	// Circle makes a radial gradient's ending shape a circle instead
	// of an ellipse.
	Circle bool
	// Size picks the radial ending shape's size; RadiusX and RadiusY
	// are its explicit radii in logical pixels under RadiusExplicit (a
	// circle takes RadiusX).
	Size             RadialSize
	RadiusX, RadiusY float64
}

// Linear is a linear gradient at angle (CSS degrees).
func Linear(angle float64, stops ...GradientStop) Gradient {
	return Gradient{Kind: GradientLinear, Angle: angle, Stops: stops}
}

// Radial is a centered farthest-corner ellipse.
func Radial(stops ...GradientStop) Gradient {
	return Gradient{Kind: GradientRadial, CenterX: 0.5, CenterY: 0.5, Stops: stops}
}

// Conic is a centered conic gradient starting at from (CSS degrees).
func Conic(from float64, stops ...GradientStop) Gradient {
	return Gradient{Kind: GradientConic, Angle: from, CenterX: 0.5, CenterY: 0.5, Stops: stops}
}

// Repeating returns g repeating its stop range (CSS repeating-*).
func (g Gradient) Repeating() Gradient {
	g.Extend = ExtendRepeat
	return g
}

// PaintGradient fills r with g, clipped to its rounded outline (radii
// in logical pixels). Colors interpolate premultiplied between the
// stops. A linear gradient's line spans the box's corners along its
// angle, CSS's rule; radial and conic geometry sizes against the box.
func (c *Canvas) PaintGradient(r Rect, radii Corners, g Gradient) {
	if c.rec != nil {
		c.record(op{kind: opGradient, r: r, radii: radii, grad: c.keepGradient(&g)})
		return
	}
	if len(g.Stops) == 0 {
		return
	}
	b, rc := c.devBox(r, radii)
	m, ok := g.mapping(b, float64(c.num)/float64(c.denom))
	if !ok {
		return
	}
	sp := c.span(b)
	for y := sp.Y; y < sp.Y+sp.H; y++ {
		for x := sp.X; x < sp.X+sp.W; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			cov := coverage(sdBox(px, py, b, rc))
			if cov <= 0 {
				continue
			}
			c.blendCov(x, y, sampleStops(g.Stops, g.Extend, m.At(px, py)), cov)
		}
	}
}

// GradientMap maps device pixel centers to gradient positions for one
// box: a plain value, so the per-pixel loop allocates nothing. At is
// the mapping; a GPU renderer evaluates the same one.
type GradientMap struct {
	Kind           GradientKind
	CX, CY         float64 // the center (radial, conic, linear's box middle)
	DX, DY, Length float64 // linear direction and line length
	RX, RY         float64 // radial radii
	From           float64 // conic start
	// InvLength, InvRX and InvRY are the reciprocals At multiplies by:
	// a GPU's division may round loosely, its multiplication may not,
	// so both renderers divide by multiplying with the same float32.
	InvLength, InvRX, InvRY float32
}

// Reciprocal is 1/v rounded to float32: the factor both renderers
// multiply by in place of dividing by v.
func Reciprocal[F float32 | float64](v F) F { return F(1 / float64(v)) }

// inv360 is 1/360, the conic turn's reciprocal.
var inv360 = Reciprocal(float32(360))

// DeviceMap is g's mapping over the box r at device scale num/denom,
// as PaintGradient maps it; false when it is degenerate.
func (g Gradient) DeviceMap(r Rect, radii Corners, num, denom int) (GradientMap, bool) {
	c := Canvas{num: num, denom: denom}
	b, _ := c.devBox(r, radii)
	return g.mapping(b, float64(num)/float64(denom))
}

// mapping resolves g's geometry over the device box b (scale is device
// pixels per logical pixel); false when it is degenerate.
func (g Gradient) mapping(b frect, scale float64) (GradientMap, bool) {
	w, h := b.x1-b.x0, b.y1-b.y0
	m := GradientMap{Kind: g.Kind, CX: b.x0 + g.CenterX*w, CY: b.y0 + g.CenterY*h, From: g.Angle}
	switch g.Kind {
	case GradientRadial:
		m.RX, m.RY = g.radii(b, m.CX, m.CY, scale)
		m.InvRX, m.InvRY = Reciprocal(float32(m.RX)), Reciprocal(float32(m.RY))
		return m, m.RX > 0 && m.RY > 0
	case GradientConic:
		return m, true
	}
	rad := g.Angle * math.Pi / 180
	m.DX, m.DY = math.Sin(rad), -math.Cos(rad)
	m.Length = math.Abs(w*m.DX) + math.Abs(h*m.DY)
	m.CX, m.CY = (b.x0+b.x1)/2, (b.y0+b.y1)/2
	m.InvLength = Reciprocal(float32(m.Length))
	return m, m.Length != 0
}

// LineLength is the length in logical pixels of g's gradient line over
// the box r — what a stop's length position is measured along: a
// linear gradient's corner-to-corner line, a radial one's ray (the
// ending shape's horizontal radius); a conic gradient has none.
func (g Gradient) LineLength(r Rect) float64 {
	b := frect{float64(r.X), float64(r.Y), float64(r.X + r.W), float64(r.Y + r.H)}
	m, ok := g.mapping(b, 1)
	switch {
	case !ok, g.Kind == GradientConic:
		return 0
	case g.Kind == GradientRadial:
		return m.RX
	}
	return m.Length
}

// At is the gradient position of the device point (px, py). It runs in
// float32 in the GPU shader's operation order, each product rounded on
// its own (no fused multiply-add), so the CPU and a GPU place hard stops
// and repeat boundaries on the same pixels: their float32 add, multiply
// and divide round alike, leaving only a GPU's looser sqrt and atan.
func (m GradientMap) At(px, py float64) float32 {
	x, y := float32(px), float32(py)
	cx, cy := float32(m.CX), float32(m.CY)
	switch m.Kind {
	case GradientRadial:
		dx := (x - cx) * m.InvRX
		dy := (y - cy) * m.InvRY
		return float32(math.Sqrt(float64(float32(dx*dx) + float32(dy*dy))))
	case GradientConic:
		a := atan2f(x-cx, -(y-cy)) * float32(180/math.Pi)
		return modFloor(a-float32(m.From), 360, inv360) * inv360
	}
	d := float32((x-cx)*float32(m.DX)) + float32((y-cy)*float32(m.DY))
	return d*m.InvLength + 0.5
}

// radii resolves a radial gradient's ending-shape radii in device
// pixels for center (cx, cy) in box b, per CSS's size keywords.
func (g Gradient) radii(b frect, cx, cy, scale float64) (rx, ry float64) {
	sideX := [2]float64{math.Abs(cx - b.x0), math.Abs(b.x1 - cx)}
	sideY := [2]float64{math.Abs(cy - b.y0), math.Abs(b.y1 - cy)}
	var sx, sy float64
	switch g.Size {
	case RadiusExplicit:
		rx, ry = g.RadiusX*scale, g.RadiusY*scale
		if g.Circle {
			ry = rx
		}
		return rx, ry
	case ClosestSide, ClosestCorner:
		sx, sy = min(sideX[0], sideX[1]), min(sideY[0], sideY[1])
	default: // FarthestSide, FarthestCorner
		sx, sy = max(sideX[0], sideX[1]), max(sideY[0], sideY[1])
	}
	corner := g.Size == ClosestCorner || g.Size == FarthestCorner
	if g.Circle {
		r := min(sx, sy)
		switch {
		case corner:
			r = math.Hypot(sx, sy)
		case g.Size == FarthestSide:
			r = max(sx, sy)
		}
		return r, r
	}
	if corner {
		// The ellipse keeps the side ellipse's aspect and passes
		// through the corner.
		return sx * math.Sqrt2, sy * math.Sqrt2
	}
	return sx, sy
}

// atan2f is atan2(y, x) in float32 by Cephes' atanf: range reduction
// and a fixed polynomial, each step rounded on its own. The GPU shader
// runs the same steps, so a conic gradient's angles agree bit for bit
// where a GPU's own atan, allowed thousands of ulps, would not.
func atan2f(y, x float32) float32 {
	const pi, halfPi, quarterPi = float32(math.Pi), float32(math.Pi / 2), float32(math.Pi / 4)
	if x == 0 {
		switch {
		case y > 0:
			return halfPi
		case y < 0:
			return -halfPi
		}
		return 0
	}
	a := atanf(float32(y / x))
	switch {
	case x > 0:
		return a
	case y >= 0:
		return a + pi
	}
	return a - pi
}

// atanf is Cephes' float atan.
func atanf(v float32) float32 {
	const halfPi, quarterPi = float32(math.Pi / 2), float32(math.Pi / 4)
	x, sign := v, float32(1)
	if x < 0 {
		x, sign = -x, -1
	}
	var y0 float32
	switch {
	case x > 2.414213562373095:
		y0, x = halfPi, float32(-1/x)
	case x > 0.4142135623730950:
		y0, x = quarterPi, float32(float32(x-1)/float32(x+1))
	}
	z := float32(x * x)
	p := float32(float32(8.05374449538e-2*z) - 1.38776856032e-1)
	p = float32(float32(p*z) + 1.99777106478e-1)
	p = float32(float32(p*z) - 3.33329491539e-1)
	r := float32(float32(float32(p*z)*x) + x)
	return sign * float32(y0+r)
}

// extendT folds a gradient position into the stop range [lo, hi] per
// the extend mode; padding leaves it for the sampler to clamp.
func extendT[F float32 | float64](t, lo, hi F, ext Extend) F {
	span := hi - lo
	if span <= 0 {
		return t
	}
	inv := Reciprocal(span)
	switch ext {
	case ExtendRepeat:
		return lo + modFloor(t-lo, span, inv)
	case ExtendReflect:
		u := modFloor(t-lo, 2*span, inv*0.5)
		if u > span {
			u = 2*span - u
		}
		return lo + u
	}
	return t
}

// modFloor is x - y*floor(x*invY), GLSL's mod with the division as a
// multiplication by invY, 1/y: in [0, y) for a positive y, by the
// formula the GPU shader uses.
func modFloor[F float32 | float64](x, y, invY F) F {
	return x - F(y*F(math.Floor(float64(F(x*invY))))) //nolint:unconvert // the conversion rounds the product, so it cannot fuse into an FMA
}

// sampleStops is the color at position t of sorted stops under ext.
func sampleStops(stops []GradientStop, ext Extend, t float32) Color {
	return gradientAt(stops, extendT(t, float32(stops[0].Pos), float32(stops[len(stops)-1].Pos), ext))
}
