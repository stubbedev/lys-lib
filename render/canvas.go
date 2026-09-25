package render

import (
	"encoding/binary"
	"image"
	"math"
)

// Canvas paints into a raw ARGB8888 premultiplied pixel buffer: the exact
// format of a wl_shm buffer at any integer scale. All drawing clips to the
// intersection of the requested rect, the canvas bounds, and any clip set
// with PushClip.
type Canvas struct {
	data   []byte
	stride int
	w, h   int
	clip   Rect
}

// Stride returns the row stride in bytes for a width in pixels
// (ARGB8888).
func Stride(widthPx int) int { return widthPx * 4 }

// New returns a canvas over data: height rows of stride bytes holding
// width ARGB8888 pixels each.
func New(data []byte, stride, width, height int) *Canvas {
	c := &Canvas{data: data, stride: stride, w: width, h: height}
	c.clip = c.Rect()
	return c
}

// Rect returns the full canvas bounds.
func (c *Canvas) Rect() Rect {
	return Rect{X: 0, Y: 0, W: c.w, H: c.h}
}

// PushClip narrows subsequent drawing to the intersection of r and the
// current clip. It returns the previous clip; restore it with PopClip.
func (c *Canvas) PushClip(r Rect) Rect {
	prev := c.clip
	c.clip = c.clip.Intersect(r)
	return prev
}

// PopClip restores a clip returned by PushClip.
func (c *Canvas) PopClip(prev Rect) {
	c.clip = prev
}

// get returns the pixel value at (x, y). Callers must ensure the pixel
// is inside both the canvas and the clip.
func (c *Canvas) get(x, y int) Color {
	o := y*c.stride + x*4
	return Color(binary.LittleEndian.Uint32(c.data[o : o+4]))
}

// set overwrites the pixel at (x, y).
func (c *Canvas) set(x, y int, v Color) {
	o := y*c.stride + x*4
	binary.LittleEndian.PutUint32(c.data[o:o+4], uint32(v))
}

// Clear overwrites the rect with col, ignoring what is underneath.
func (c *Canvas) Clear(r Rect, col Color) {
	r = c.clip.Intersect(r)
	if r.Empty() {
		return
	}
	for y := r.Y; y < r.Y+r.H; y++ {
		row := c.data[y*c.stride:]
		px := binary.LittleEndian.Uint32(colBytes(col))
		for x := r.X; x < r.X+r.W; x++ {
			binary.LittleEndian.PutUint32(row[x*4:x*4+4], px)
		}
	}
}

func colBytes(c Color) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(c))
	return b[:]
}

// FillRect blends col over the rect with source-over compositing.
func (c *Canvas) FillRect(r Rect, col Color) {
	r = c.clip.Intersect(r)
	if r.Empty() {
		return
	}
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			c.set(x, y, col.over(c.get(x, y)))
		}
	}
}

// BorderRect blends col over a hollow rect of the given thickness. The
// stroke grows inward from the rect edges.
func (c *Canvas) BorderRect(r Rect, width int, col Color) {
	c.FillRect(Rect{X: r.X, Y: r.Y, W: r.W, H: width}, col)
	c.FillRect(Rect{X: r.X, Y: r.Y + r.H - width, W: r.W, H: width}, col)
	c.FillRect(Rect{X: r.X, Y: r.Y, W: width, H: r.H}, col)
	c.FillRect(Rect{X: r.X + r.W - width, Y: r.Y, W: width, H: r.H}, col)
}

// RoundedRect blends col over a filled rect with corners rounded by radius
// pixels, anti-aliased with per-pixel signed-distance coverage.
func (c *Canvas) RoundedRect(r Rect, radius int, col Color) {
	r = c.clip.Intersect(r)
	if r.Empty() {
		return
	}
	rad := math.Min(float64(radius), math.Min(float64(r.W), float64(r.H))/2)
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			d := sdRoundRect(float64(x)+0.5, float64(y)+0.5, r, rad)
			cov := 0.5 - d
			if cov < 0 {
				continue
			}
			if cov > 1 {
				cov = 1
			}
			a := uint8(math.Round(cov * 255))
			partial := Color(uint32(a)<<24 |
				(uint32(col.R())*uint32(a)/255)<<16 |
				(uint32(col.G())*uint32(a)/255)<<8 |
				(uint32(col.B())*uint32(a))/255)
			c.set(x, y, partial.over(c.get(x, y)))
		}
	}
}

// sdRoundRect is the signed distance from (px, py) to a rounded rect.
// Negative inside, positive outside, in pixels.
func sdRoundRect(px, py float64, r Rect, radius float64) float64 {
	cx := float64(r.X) + float64(r.W)/2
	cy := float64(r.Y) + float64(r.H)/2
	hw := float64(r.W)/2 - radius
	hh := float64(r.H)/2 - radius
	qx := math.Abs(px-cx) - hw
	qy := math.Abs(py-cy) - hh
	out := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	return out + math.Min(math.Max(qx, qy), 0) - radius
}

// LinearGradient blends a linear interpolation from (at one edge) to (at
// the opposite edge) over the rect. Horizontal runs left to right;
// otherwise top to bottom.
func (c *Canvas) LinearGradient(r Rect, from, to Color, horizontal bool) {
	r = c.clip.Intersect(r)
	if r.Empty() {
		return
	}
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			var t float64
			if horizontal {
				t = (float64(x) + 0.5 - float64(r.X)) / float64(r.W)
			} else {
				t = (float64(y) + 0.5 - float64(r.Y)) / float64(r.H)
			}
			c.set(x, y, lerp(from, to, t).over(c.get(x, y)))
		}
	}
}

// Line blends col along the segment from (x0, y0) to (x1, y1) with the
// given thickness in pixels, anti-aliased with per-pixel signed-distance
// coverage. Caps are round.
func (c *Canvas) Line(x0, y0, x1, y1, width int, col Color) {
	if width < 1 || c.clip.Empty() {
		return
	}
	bx := Rect{
		X: min(x0, x1) - width - 1,
		Y: min(y0, y1) - width - 1,
		W: abs(x1-x0) + 2*width + 2,
		H: abs(y1-y0) + 2*width + 2,
	}
	bx = c.clip.Intersect(bx)
	if bx.Empty() {
		return
	}

	half := float64(width) / 2
	ax, ay := float64(x0)+0.5, float64(y0)+0.5
	dx, dy := float64(x1)+0.5-ax, float64(y1)+0.5-ay
	len2 := dx*dx + dy*dy
	for y := bx.Y; y < bx.Y+bx.H; y++ {
		for x := bx.X; x < bx.X+bx.W; x++ {
			px, py := float64(x)+0.5-ax, float64(y)+0.5-ay
			t := 0.0
			if len2 > 0 {
				t = math.Min(1, math.Max(0, (px*dx+py*dy)/len2))
			}
			d := math.Hypot(px-dx*t, py-dy*t)
			cov := math.Min(1, math.Max(0, half+0.5-d))
			if cov == 0 {
				continue
			}
			a := uint32(math.Round(cov * 255))
			partial := Color(a<<24 |
				(uint32(col.R())*a/255)<<16 |
				(uint32(col.G())*a/255)<<8 |
				uint32(col.B())*a/255)
			c.set(x, y, partial.over(c.get(x, y)))
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// DrawImage blends img onto the canvas with its top-left corner at (x, y).
func (c *Canvas) DrawImage(img image.Image, x, y int) {
	b := img.Bounds()
	r := Rect{X: x + b.Min.X, Y: y + b.Min.Y, W: b.Dx(), H: b.Dy()}
	r = c.clip.Intersect(r)
	if r.Empty() {
		return
	}
	for py := r.Y; py < r.Y+r.H; py++ {
		for pxx := r.X; pxx < r.X+r.W; pxx++ {
			sr, sg, sb, sa := img.At(pxx-x, py-y).RGBA()
			src := Color(sa>>8<<24 | sr>>8<<16 | sg>>8<<8 | sb>>8)
			c.set(pxx, py, src.over(c.get(pxx, py)))
		}
	}
}
