package render

import "math"

// The projective (3-D-lite) transform pipeline: CSS's 3-D transform
// functions — perspective(), rotateX/rotateY, translateZ — restricted
// to the z=0 plane each widget paints on. A 4x4 transform restricted
// to that plane is a 3x3 homography: (x, y, 1) mapped then divided by
// the third row, so a tilted or pushed plane foreshortens under
// perspective while everything else keeps the affine fast path.

// M3 is a 3x3 projective matrix acting on plane points: (x, y, 1)
// maps to (x', y', w') = M·(x, y, 1), then divides by w'. The zero
// value collapses everything to the origin; Identity3 fixes nothing.
type M3 [9]float64

// Identity3 is the projective map that moves nothing.
var Identity3 = M3{1, 0, 0, 0, 1, 0, 0, 0, 1}

// Affine3 embeds an affine map into a projective one.
func Affine3(m Affine) M3 {
	return M3{m.A, m.C, m.E, m.B, m.D, m.F, 0, 0, 1}
}

// Mul is m∘n: n first, then m.
func (m M3) Mul(n M3) M3 {
	var out M3
	for r := range 3 {
		for c := range 3 {
			v := 0.0
			for k := range 3 {
				v += m[r*3+k] * n[k*3+c]
			}
			out[r*3+c] = v
		}
	}
	return out
}

// Apply maps the point (x, y) with the perspective divide.
func (m M3) Apply(x, y float64) (float64, float64) {
	w := m[6]*x + m[7]*y + m[8]
	if w == 0 || math.IsNaN(w) || math.IsInf(w, 0) {
		return 0, 0
	}
	return (m[0]*x + m[1]*y + m[2]) / w, (m[3]*x + m[4]*y + m[5]) / w
}

// Invert returns the inverse map; false when m collapses the plane.
func (m M3) Invert() (M3, bool) {
	det := m[0]*(m[4]*m[8]-m[5]*m[7]) - m[1]*(m[3]*m[8]-m[5]*m[6]) + m[2]*(m[3]*m[7]-m[4]*m[6])
	if det == 0 || math.IsNaN(det) || math.IsInf(det, 0) {
		return M3{}, false
	}
	inv := 1 / det
	return M3{
		(m[4]*m[8] - m[5]*m[7]) * inv,
		(m[2]*m[7] - m[1]*m[8]) * inv,
		(m[1]*m[5] - m[2]*m[4]) * inv,
		(m[5]*m[6] - m[3]*m[8]) * inv,
		(m[0]*m[8] - m[2]*m[6]) * inv,
		(m[2]*m[3] - m[0]*m[5]) * inv,
		(m[3]*m[7] - m[4]*m[6]) * inv,
		(m[1]*m[6] - m[0]*m[7]) * inv,
		(m[0]*m[4] - m[1]*m[3]) * inv,
	}, true
}

// MapBounds is the smallest pixel rect covering r's image.
func (m M3) MapBounds(r Rect) Rect {
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

// Flat reports a map whose third row leaves w untouched: the purely
// affine case, which composites without the divide.
func (m M3) Flat() bool {
	return m[6] == 0 && m[7] == 0 && m[8] == 1
}

// Mat4 is a 4x4 transform over (x, y, z, 1) column vectors, the
// composition space of the 3-D functions.
type Mat4 [16]float64

// Identity4 moves nothing.
var Identity4 = Mat4{
	1, 0, 0, 0,
	0, 1, 0, 0,
	0, 0, 1, 0,
	0, 0, 0, 1,
}

// Mul is m∘n: n first, then m.
func (m Mat4) Mul(n Mat4) Mat4 {
	var out Mat4
	for r := range 4 {
		for c := range 4 {
			v := 0.0
			for k := range 4 {
				v += m[r*4+k] * n[k*4+c]
			}
			out[r*4+c] = v
		}
	}
	return out
}

// Plane3 induces m's action on the z=0 plane as a homography: the
// first two rows' x and y columns with the translation column, and
// the last row's x, y and w columns as the divide.
func (m Mat4) Plane3() M3 {
	return M3{
		m[0], m[1], m[3],
		m[4], m[5], m[7],
		m[12], m[13], m[15],
	}
}

// PlaneRotZ embeds a screen rotation (about z) into 4x4 space.
func PlaneRotZ(a Affine) Mat4 {
	return Mat4{
		a.A, a.C, 0, a.E,
		a.B, a.D, 0, a.F,
		0, 0, 1, 0,
		0, 0, 0, 1,
	}
}

// Perspective4 is CSS perspective()'s matrix: the viewer at distance
// d on the z axis, w shrinking as z approaches it.
func Perspective4(d float64) Mat4 {
	m := Identity4
	if d > 0 {
		m[14] = -1 / d
	}
	return m
}

// RotateX4 rotates about the x axis by deg degrees.
func RotateX4(deg float64) Mat4 {
	s, c := math.Sincos(deg * math.Pi / 180)
	m := Identity4
	m[5], m[6], m[9], m[10] = c, -s, s, c
	return m
}

// RotateY4 rotates about the y axis by deg degrees.
func RotateY4(deg float64) Mat4 {
	s, c := math.Sincos(deg * math.Pi / 180)
	m := Identity4
	m[0], m[2], m[8], m[10] = c, s, -s, c
	return m
}

// TranslateZ4 moves t along z.
func TranslateZ4(t float64) Mat4 {
	m := Identity4
	m[11] = t
	return m
}
