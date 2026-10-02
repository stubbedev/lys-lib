package render

import (
	"image"
	"image/draw"
	"math"

	"github.com/srwiley/rasterx"
	"golang.org/x/image/math/fixed"
)

// svgScanner is rasterx's vector scanner with the even-odd rule it
// lacks: ScannerGV ignores SetWinding, so an even-odd path (a ring
// drawn as an outer and an inner subpath) filled solid. Nonzero paths
// go to ScannerGV unchanged; even-odd ones accumulate here, with the
// signed-area coverage golang.org/x/image/vector uses, folded into
// the even-odd triangle wave, and composite through the same source.
type svgScanner struct {
	*rasterx.ScannerGV
	evenOdd bool
	w, h    int
	area    []float32
	// The pen and the current subpath's start, in pixels.
	penX, penY, startX, startY float32
	open                       bool
	minX, minY, maxX, maxY     fixed.Int26_6
}

func newSVGScanner(w, h int, dest draw.Image) *svgScanner {
	s := &svgScanner{ScannerGV: rasterx.NewScannerGV(w, h, dest, dest.Bounds()), w: w, h: h}
	s.Clear()
	return s
}

// SetWinding picks the fill rule for the next path.
func (s *svgScanner) SetWinding(useNonZeroWinding bool) { s.evenOdd = !useNonZeroWinding }

// GetPathExtent is the accumulated path's extent, either rule.
func (s *svgScanner) GetPathExtent() fixed.Rectangle26_6 {
	return fixed.Rectangle26_6{Min: fixed.Point26_6{X: s.minX, Y: s.minY}, Max: fixed.Point26_6{X: s.maxX, Y: s.maxY}}
}

func (s *svgScanner) extend(a fixed.Point26_6) {
	s.minX, s.minY = min(s.minX, a.X), min(s.minY, a.Y)
	s.maxX, s.maxY = max(s.maxX, a.X), max(s.maxY, a.Y)
}

// Start begins a subpath at a, closing the one before.
func (s *svgScanner) Start(a fixed.Point26_6) {
	s.extend(a)
	if !s.evenOdd {
		s.ScannerGV.Start(a)
		return
	}
	s.closeSubpath()
	s.penX, s.penY = float32(a.X)/64, float32(a.Y)/64
	s.startX, s.startY, s.open = s.penX, s.penY, true
}

// Line adds a segment to the current subpath.
func (s *svgScanner) Line(b fixed.Point26_6) {
	s.extend(b)
	if !s.evenOdd {
		s.ScannerGV.Line(b)
		return
	}
	s.lineTo(float32(b.X)/64, float32(b.Y)/64)
}

// Draw composites the accumulated path through the source.
func (s *svgScanner) Draw() {
	if !s.evenOdd {
		s.ScannerGV.Draw()
		return
	}
	s.closeSubpath()
	mask := image.NewAlpha(image.Rect(0, 0, s.w, s.h))
	var acc float32
	for i, a := range s.area {
		acc += a
		c := float32(math.Mod(math.Abs(float64(acc)), 2))
		if c > 1 {
			c = 2 - c
		}
		mask.Pix[i] = uint8(c*255 + 0.5)
	}
	draw.DrawMask(s.Dest, s.Dest.Bounds(), s.Source, s.Offset, mask, image.Point{}, draw.Over)
}

// Clear drops the accumulated path, either rule.
func (s *svgScanner) Clear() {
	s.ScannerGV.Clear()
	if s.area == nil {
		s.area = make([]float32, s.w*s.h)
	} else {
		clear(s.area)
	}
	s.open = false
	const big = fixed.Int26_6(math.MaxInt32)
	s.minX, s.minY, s.maxX, s.maxY = big, big, -big, -big
}

// SetBounds resizes the target (rasterx calls it once, at creation).
func (s *svgScanner) SetBounds(w, h int) {
	s.ScannerGV.SetBounds(w, h)
	s.w, s.h, s.area = w, h, nil
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
	yMax := min(by, float32(s.h))
	width := int32(s.w)
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
	return uint(width)
}
