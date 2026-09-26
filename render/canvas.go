package render

import (
	"encoding/binary"
	"fmt"
	"image"
	"math"
)

// Canvas paints into a raw ARGB8888 premultiplied pixel buffer: the exact
// format of a wl_shm buffer at any scale. All drawing clips to the
// intersection of the requested rect, the canvas bounds, and any clip set
// with PushClip.
//
// The canvas carries a device scale: a rational number of device (buffer)
// pixels per logical pixel - 240/120 is 2x, 150/120 is the fractional
// 1.25x. Primitive arguments (FillRect, RoundedRect, text, ...) are in
// logical pixels and land on device pixels rounded outward, so a logical
// rect fully covers the device area it spans; text is shaped at its
// logical size and rasterized at the device scale, which keeps it crisp
// at any factor. The clip set with PushClip, the bounds returned by Rect,
// and the explicit device bridge (MapRect, ClearDevice) are in device
// pixels.
type Canvas struct {
	data   []byte
	stride int
	w, h   int
	clip   Rect
	// num/denom is the device scale: device pixels per logical pixel.
	num, denom int
	// touched counts the pixels drawn so far. Paint-count tests read
	// it through Touched to pin how much of a frame damage-restricted
	// repainting actually wrote.
	touched int
}

// Touched returns the number of pixels written since the last
// ResetTouched (or since the canvas was created). Blends count even
// when their source color equals the destination.
func (c *Canvas) Touched() int { return c.touched }

// ResetTouched zeroes the drawn-pixel counter and returns the previous
// value.
func (c *Canvas) ResetTouched() int {
	n := c.touched
	c.touched = 0
	return n
}

// Stride returns the row stride in bytes for a width in pixels
// (ARGB8888).
func Stride(widthPx int) int { return widthPx * 4 }

// New returns a canvas over data: height rows of stride bytes holding
// width ARGB8888 pixels each, at device scale 1 (one device pixel per
// logical pixel).
func New(data []byte, stride, width, height int) *Canvas {
	return NewScaled(data, stride, width, height, 1, 1)
}

// NewScaled returns a canvas over data at the given device scale: num
// device pixels per denom logical pixels (240/120 doubles). width and
// height are the buffer's device size. It panics on a non-positive
// scale, which is a programming error, not a runtime condition.
func NewScaled(data []byte, stride, width, height, num, denom int) *Canvas {
	if num <= 0 || denom <= 0 {
		panic(fmt.Sprintf("render: invalid device scale %d/%d", num, denom))
	}
	c := &Canvas{data: data, stride: stride, w: width, h: height, num: num, denom: denom}
	c.clip = c.Rect()
	return c
}

// DeviceScale returns the canvas's device scale as a rational: num
// device pixels per denom logical pixels.
func (c *Canvas) DeviceScale() (num, denom int) { return c.num, c.denom }

// divFloor divides a by b rounding toward negative infinity; divCeil
// rounds toward positive infinity. b must be positive.
func divFloor(a, b int) int {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

func divCeil(a, b int) int { return -divFloor(-a, b) }

// MapRect maps the logical rect r into device pixels at scale num/denom,
// rounding the origin down and the far edge up so the device rect fully
// covers everything r spans. Package-level so the frame pipeline can map
// damage without a canvas instance.
func MapRect(r Rect, num, denom int) Rect {
	x0 := divFloor(r.X*num, denom)
	y0 := divFloor(r.Y*num, denom)
	x1 := divCeil((r.X+r.W)*num, denom)
	y1 := divCeil((r.Y+r.H)*num, denom)
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// MapRect maps the logical rect r into device pixels on this canvas.
func (c *Canvas) MapRect(r Rect) Rect { return MapRect(r, c.num, c.denom) }

// Rect returns the full canvas bounds in device pixels.
func (c *Canvas) Rect() Rect {
	return Rect{X: 0, Y: 0, W: c.w, H: c.h}
}

// PushClip narrows subsequent drawing to the intersection of r and the
// current clip. r is in device pixels - MapRect converts a logical rect.
// It returns the previous clip; restore it with PopClip.
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
	c.touched++
	o := y*c.stride + x*4
	binary.LittleEndian.PutUint32(c.data[o:o+4], uint32(v))
}

// Clear overwrites the logical rect with col, ignoring what is
// underneath. ClearDevice is the device-space counterpart for callers
// that already hold mapped rects.
func (c *Canvas) Clear(r Rect, col Color) {
	c.ClearDevice(c.MapRect(r), col)
}

// ClearDevice overwrites the device-pixel rect r with col, ignoring what
// is underneath. Unlike Clear it applies no logical mapping: it is the
// bridge for frame pipelines that track damage in device pixels.
func (c *Canvas) ClearDevice(r Rect, col Color) {
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
	c.touched += r.W * r.H
}

func colBytes(c Color) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(c))
	return b[:]
}

// FillRect blends col over the logical rect with source-over compositing.
func (c *Canvas) FillRect(r Rect, col Color) {
	r = c.clip.Intersect(c.MapRect(r))
	if r.Empty() {
		return
	}
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			c.set(x, y, col.over(c.get(x, y)))
		}
	}
}

// BorderRect blends col over a hollow rect of the given logical thickness.
// The stroke grows inward from the rect edges.
func (c *Canvas) BorderRect(r Rect, width int, col Color) {
	r = c.MapRect(r)
	t := max(1, divCeil(width*c.num, c.denom))
	c.FillRectDevice(Rect{X: r.X, Y: r.Y, W: r.W, H: t}, col)
	c.FillRectDevice(Rect{X: r.X, Y: r.Y + r.H - t, W: r.W, H: t}, col)
	c.FillRectDevice(Rect{X: r.X, Y: r.Y, W: t, H: r.H}, col)
	c.FillRectDevice(Rect{X: r.X + r.W - t, Y: r.Y, W: t, H: r.H}, col)
}

// FillRectDevice blends col over the device-pixel rect with source-over
// compositing; the device counterpart of FillRect.
func (c *Canvas) FillRectDevice(r Rect, col Color) {
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

// RoundedRect blends col over a filled logical rect with corners rounded
// by radius logical pixels, anti-aliased with per-pixel signed-distance
// coverage. The rasterization runs at the device scale.
func (c *Canvas) RoundedRect(r Rect, radius int, col Color) {
	r = c.clip.Intersect(c.MapRect(r))
	if r.Empty() {
		return
	}
	rad := math.Min(float64(radius)*float64(c.num)/float64(c.denom), math.Min(float64(r.W), float64(r.H))/2)
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
// the opposite edge) over the logical rect. Horizontal runs left to
// right; otherwise top to bottom.
func (c *Canvas) LinearGradient(r Rect, from, to Color, horizontal bool) {
	r = c.clip.Intersect(c.MapRect(r))
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

// Line blends col along the segment from logical (x0, y0) to (x1, y1)
// with the given thickness in logical pixels, anti-aliased with
// per-pixel signed-distance coverage at the device scale. Caps are round.
func (c *Canvas) Line(x0, y0, x1, y1, width int, col Color) {
	if width < 1 || c.clip.Empty() {
		return
	}
	bx := c.MapRect(Rect{
		X: min(x0, x1) - width - 1,
		Y: min(y0, y1) - width - 1,
		W: abs(x1-x0) + 2*width + 2,
		H: abs(y1-y0) + 2*width + 2,
	})
	bx = c.clip.Intersect(bx)
	if bx.Empty() {
		return
	}

	dev := float64(c.num) / float64(c.denom)
	half := float64(width) * dev / 2
	ax := (float64(x0) + 0.5) * dev
	ay := (float64(y0) + 0.5) * dev
	dx := (float64(x1)+0.5)*dev - ax
	dy := (float64(y1)+0.5)*dev - ay
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

// DrawImage blends img onto the canvas, scaled from its natural (logical)
// size to the device rect its logical placement spans.
func (c *Canvas) DrawImage(img image.Image, x, y int) {
	b := img.Bounds()
	dst := c.MapRect(Rect{X: x, Y: y, W: b.Dx(), H: b.Dy()})
	dst = c.clip.Intersect(dst)
	if dst.Empty() {
		return
	}
	for py := dst.Y; py < dst.Y+dst.H; py++ {
		for pxx := dst.X; pxx < dst.X+dst.W; pxx++ {
			sx := (pxx - dst.X) * b.Dx() / dst.W
			sy := (py - dst.Y) * b.Dy() / dst.H
			sr, sg, sb, sa := img.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
			src := Color(sa>>8<<24 | sr>>8<<16 | sg>>8<<8 | sb>>8)
			c.set(pxx, py, src.over(c.get(pxx, py)))
		}
	}
}
