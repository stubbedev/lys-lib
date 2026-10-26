package render

import "math"

// Mapped is a projective map from canvas device pixels to a source's,
// held and evaluated in float32 in a fixed operation order, each
// product rounded on its own: the CPU's layer composites and turned
// icons sample through it, and a GPU shader evaluates the same rows in
// the same order, so both land each pixel center on the same source
// point. Inv maps a pixel center into the source; Fwd is the forward
// map's last row, whose sign at the source point rejects points behind
// the viewer.
type Mapped struct {
	Inv [3][3]float32
	Fwd [3]float32
}

// MappedOf is m (source to canvas) as a Mapped; false when m collapses
// the plane.
func MappedOf(m M3) (Mapped, bool) {
	inv, ok := m.Invert()
	if !ok {
		return Mapped{}, false
	}
	var out Mapped
	for r := range 3 {
		for c := range 3 {
			out.Inv[r][c] = float32(inv[r*3+c])
		}
		out.Fwd[r] = float32(m[6+r])
	}
	return out, true
}

// row is (r0*x + r1*y) + r2, each product rounded on its own.
func row(r [3]float32, x, y float32) float32 {
	return float32(float32(r[0]*x)+float32(r[1]*y)) + r[2]
}

// At is the source point the center of canvas pixel (x, y) maps to;
// false behind the viewer.
func (m *Mapped) At(x, y int) (float32, float32, bool) {
	qx, qy := float32(x)+0.5, float32(y)+0.5
	w := row(m.Inv[2], qx, qy)
	sx, sy := row(m.Inv[0], qx, qy)/w, row(m.Inv[1], qx, qy)/w
	if fw := row(m.Fwd, sx, sy); !(fw > 1e-9) {
		return 0, 0, false
	}
	return sx, sy, true
}

// bilinear is the bilinear blend of the four texels around (x, y) in
// float32, each premultiplied channel weighted alike so the result
// stays premultiplied, rounded half up; texels outside src are
// transparent, so a sampled edge antialiases.
func bilinear(src Rect, x, y float32, at func(px, py int) Color) Color {
	x0, y0 := float32(math.Floor(float64(x))), float32(math.Floor(float64(y)))
	fx, fy := x-x0, y-y0
	ix, iy := int(x0), int(y0)
	if ix < src.X-1 || iy < src.Y-1 || ix >= src.X+src.W || iy >= src.Y+src.H {
		return 0
	}
	tap := func(px, py int) [4]float32 {
		if px < src.X || py < src.Y || px >= src.X+src.W || py >= src.Y+src.H {
			return [4]float32{}
		}
		col := at(px, py)
		return [4]float32{float32(col.A()), float32(col.R()), float32(col.G()), float32(col.B())}
	}
	p00, p10, p01, p11 := tap(ix, iy), tap(ix+1, iy), tap(ix, iy+1), tap(ix+1, iy+1)
	gx, gy := 1-fx, 1-fy
	var ch [4]uint32
	for i := range ch {
		top := float32(p00[i]*gx) + float32(p10[i]*fx)
		bottom := float32(p01[i]*gx) + float32(p11[i]*fx)
		v := float32(top*gy) + float32(bottom*fy)
		ch[i] = uint32(min(255, max(0, math.Floor(float64(v+0.5)))))
	}
	return Color(ch[0]<<24 | ch[1]<<16 | ch[2]<<8 | ch[3])
}

// blendMapped blends, over the device rect dst, the source texels at
// supplies within src, sampled bilinearly half a texel below where m
// maps each pixel center.
func (c *Canvas) blendMapped(dst, src Rect, m *Mapped, at func(px, py int) Color) {
	for y := dst.Y; y < dst.Y+dst.H; y++ {
		for x := dst.X; x < dst.X+dst.W; x++ {
			sx, sy, ok := m.At(x, y)
			if !ok {
				continue
			}
			if col := bilinear(src, sx-0.5, sy-0.5, at); col != 0 {
				c.blend(x, y, col)
			}
		}
	}
}
