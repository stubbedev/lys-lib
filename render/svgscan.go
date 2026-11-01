package render

import (
	"image"
	"image/draw"
	"math"

	"github.com/srwiley/rasterx"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// svgScanner rasterizes each path over the box it covers, where
// rasterx's ScannerGV, which supplies its paint, clears and walks the
// whole target for every path. It adds the even-odd rule ScannerGV
// lacks, which ignores SetWinding, so an even-odd path (a ring drawn as
// an outer and an inner subpath) filled solid. Nonzero paths go through
// golang.org/x/image/vector; even-odd ones accumulate here, with the
// signed-area coverage vector uses, folded into the even-odd triangle
// wave, and composite through the same source.
type svgScanner struct {
	*rasterx.ScannerGV
	evenOdd bool
	w, h    int
	points  []svgPoint
	z       vector.Rasterizer
	area    []float32
	mask    image.Alpha
	// boxW and boxH are the even-odd area's size.
	boxW, boxH int
	// The pen and the current subpath's start, in pixels of the box.
	penX, penY, startX, startY float32
	open                       bool
	minX, minY, maxX, maxY     fixed.Int26_6
}

// svgPoint is one vertex of the accumulated path, in pixels.
type svgPoint struct {
	x, y  float32
	start bool
}

func newSVGScanner(w, h int, dest draw.Image) *svgScanner {
	s := &svgScanner{ScannerGV: rasterx.NewScannerGV(0, 0, dest, dest.Bounds()), w: w, h: h}
	s.Clear()
	return s
}

// SetColor sets the next path's paint. A gradient's source is
// rasterx's ColorFuncImage, which embeds image.Uniform and so inherits
// its RGBA64At; image/draw prefers that over At, and would paint the
// Uniform's leftover color instead of the gradient. The source is
// handed to draw behind an image that only answers At.
func (s *svgScanner) SetColor(c any) {
	s.ScannerGV.SetColor(c)
	if _, uniform := s.Source.(*image.Uniform); !uniform {
		s.Source = atOnly{s.Source}
	}
}

// atOnly is an image whose pixels come from At alone.
type atOnly struct{ image.Image }

// SetWinding picks the fill rule for the next path.
func (s *svgScanner) SetWinding(useNonZeroWinding bool) { s.evenOdd = !useNonZeroWinding }

// GetPathExtent is the accumulated path's extent, either rule.
func (s *svgScanner) GetPathExtent() fixed.Rectangle26_6 {
	return fixed.Rectangle26_6{Min: fixed.Point26_6{X: s.minX, Y: s.minY}, Max: fixed.Point26_6{X: s.maxX, Y: s.maxY}}
}

func (s *svgScanner) add(a fixed.Point26_6, start bool) {
	s.minX, s.minY = min(s.minX, a.X), min(s.minY, a.Y)
	s.maxX, s.maxY = max(s.maxX, a.X), max(s.maxY, a.Y)
	s.points = append(s.points, svgPoint{float32(a.X) / 64, float32(a.Y) / 64, start})
}

// Start begins a subpath at a, closing the one before.
func (s *svgScanner) Start(a fixed.Point26_6) { s.add(a, true) }

// Line adds a segment to the current subpath.
func (s *svgScanner) Line(b fixed.Point26_6) { s.add(b, false) }

// box is the pixels the accumulated path can touch: its extent and the
// column right of it, which a segment's coverage can reach, on the
// target. A path running off the target's left or right edge keeps the
// rasterizers' clamping, since the box then ends at that edge.
func (s *svgScanner) box() image.Rectangle {
	if s.minX > s.maxX {
		return image.Rectangle{}
	}
	return image.Rect(int(s.minX>>6), int(s.minY>>6), int((s.maxX+63)>>6)+1, int((s.maxY+63)>>6)+1).
		Intersect(image.Rect(0, 0, s.w, s.h))
}

// Draw composites the accumulated path through the source.
func (s *svgScanner) Draw() {
	box := s.box()
	if box.Empty() {
		return
	}
	at, sp := box.Add(s.Dest.Bounds().Min), s.Offset.Add(box.Min)
	ox, oy := float32(box.Min.X), float32(box.Min.Y)
	if !s.evenOdd {
		s.z.Reset(s.rasterizerWidth(box), box.Dy())
		for _, p := range s.points {
			if p.start {
				s.z.MoveTo(p.x-ox, p.y-oy)
			} else {
				s.z.LineTo(p.x-ox, p.y-oy)
			}
		}
		s.z.Draw(s.Dest, at, s.Source, sp)
		return
	}
	s.accumulateEvenOdd(box.Dx(), box.Dy(), ox, oy)
	draw.DrawMask(s.Dest, at, s.Source, sp, &s.mask, image.Point{}, draw.Over)
}

// vectorFixedMathLimit is the largest size in both dimensions that
// x/image/vector rasterizes in fixed point; beyond it, floating point.
const vectorFixedMathLimit = 512

// rasterizerWidth is the rasterizer's width for box: the box's, but past
// the fixed point limit on a target past it, since which math runs shows
// on edge pixels and must not depend on how small a path is.
func (s *svgScanner) rasterizerWidth(box image.Rectangle) int {
	if s.w > vectorFixedMathLimit || s.h > vectorFixedMathLimit {
		return max(box.Dx(), vectorFixedMathLimit+1)
	}
	return box.Dx()
}

// accumulateEvenOdd fills s.mask, w x h, with the even-odd coverage of
// the path moved by (-ox, -oy).
func (s *svgScanner) accumulateEvenOdd(w, h int, ox, oy float32) {
	n := w * h
	if cap(s.area) < n {
		s.area, s.mask.Pix = make([]float32, n), make([]uint8, n)
	}
	s.area, s.mask.Pix = s.area[:n], s.mask.Pix[:n]
	clear(s.area)
	s.mask.Stride, s.mask.Rect = w, image.Rect(0, 0, w, h)
	s.boxW, s.boxH, s.open = w, h, false
	for _, p := range s.points {
		if p.start {
			s.closeSubpath()
			s.penX, s.penY = p.x-ox, p.y-oy
			s.startX, s.startY, s.open = s.penX, s.penY, true
		} else {
			s.lineTo(p.x-ox, p.y-oy)
		}
	}
	s.closeSubpath()
	var acc float32
	for i, a := range s.area {
		acc += a
		c := float32(math.Mod(math.Abs(float64(acc)), 2))
		if c > 1 {
			c = 2 - c
		}
		s.mask.Pix[i] = uint8(c*255 + 0.5)
	}
}

// Clear drops the accumulated path, either rule.
func (s *svgScanner) Clear() {
	s.points = s.points[:0]
	const big = fixed.Int26_6(math.MaxInt32)
	s.minX, s.minY, s.maxX, s.maxY = big, big, -big, -big
}

// SetBounds resizes the target (rasterx calls it once, at creation).
func (s *svgScanner) SetBounds(w, h int) {
	s.w, s.h = w, h
	s.Clear()
}

func (s *svgScanner) closeSubpath() {
	if s.open && (s.penX != s.startX || s.penY != s.startY) {
		s.lineTo(s.startX, s.startY)
	}
	s.open = false
}

// lineTo accumulates the segment from the pen to (bx, by) into the
// signed area buffer: x/image/vector's floatingLineTo.
func (s *svgScanner) lineTo(bx, by float32) {
	ax, ay := s.penX, s.penY
	s.penX, s.penY = bx, by
	dir := float32(1)
	if ay > by {
		dir, ax, ay, bx, by = -1, bx, by, ax, ay
	}
	if by-ay <= 0.000001 {
		return
	}
	dxdy := (bx - ax) / (by - ay)
	x := ax
	y := float32(math.Floor(float64(ay)))
	yMax := min(by, float32(s.boxH))
	width := int32(s.boxW)
	for ; y < yMax; y++ {
		dy := min(y+1, yMax) - max(y, ay)
		xNext := x + float32(dy*dxdy)
		if y < 0 {
			x = xNext
			continue
		}
		buf := s.area[int32(y)*width:]
		add := func(i int32, v float32) {
			if j := clampIndex(i, width); j < uint(len(buf)) {
				buf[j] += v
			}
		}
		d := float32(dy * dir)
		x0, x1 := x, xNext
		if x > xNext {
			x0, x1 = x1, x0
		}
		x0i := int32(math.Floor(float64(x0)))
		x0Floor := float32(x0i)
		x1i := int32(math.Ceil(float64(x1)))
		x1Ceil := float32(x1i)
		if x1i <= x0i+1 {
			xmf := float32(0.5*(x+xNext)) - x0Floor
			add(x0i, d-float32(d*xmf))
			add(x0i+1, float32(d*xmf))
		} else {
			sc := 1 / (x1 - x0)
			x0f := x0 - x0Floor
			oneMinusX0f := 1 - x0f
			a0 := float32(0.5 * sc * oneMinusX0f * oneMinusX0f)
			x1f := x1 - x1Ceil + 1
			am := float32(0.5 * sc * x1f * x1f)
			add(x0i, float32(d*a0))
			if x1i == x0i+2 {
				add(x0i+1, float32(d*(1-a0-am)))
			} else {
				a1 := float32(sc * (1.5 - x0f))
				add(x0i+1, float32(d*(a1-a0)))
				dTimesS := float32(d * sc)
				for xi := x0i + 2; xi < x1i-1; xi++ {
					add(xi, dTimesS)
				}
				a2 := a1 + float32(sc*float32(x1i-x0i-3))
				add(x1i-1, float32(d*(1-a2-am)))
			}
			add(x1i, float32(d*am))
		}
		x = xNext
	}
}

// clampIndex pins a column into [0, width]: coverage left of the
// image lands in its first column, right of it past the row's end
// (the next row's start, which the running sum carries back to zero).
func clampIndex(i, width int32) uint {
	if i < 0 {
		return 0
	}
	if i < width {
		return uint(i)
	}
	return uint(max(width, 0))
}
