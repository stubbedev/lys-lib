package render

import (
	"image"
	"image/color"
	"math"
)

// Resample scales srcRect of src into a fresh dstW x dstH RGBA with the
// CatmullRom kernel. Cubic resampling is what keeps fractional and
// non-integer down- and upscales from the blockiness of a
// nearest-neighbor pass (stubbedev/lys-ui#14); the canvas's own image
// drawing is nearest-neighbor and must never see unresampled pixels.
//
// Memory stays bounded by the rows in flight, never the image: a
// downscale by two or more first box-averages whole factor blocks (one
// source row read at a time), and the cubic pass keeps only the
// horizontally filtered rows its vertical taps span. Any source model
// reads through a row converter, YCbCr and NRGBA without a per-pixel
// interface call.
func Resample(src image.Image, srcRect image.Rectangle, dstW, dstH int) *image.RGBA {
	if dstW <= 0 || dstH <= 0 {
		return image.NewRGBA(image.Rectangle{})
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	r := srcRect.Intersect(src.Bounds())
	if r.Empty() {
		return dst // a fully transparent raster of the requested size
	}
	rows := boxReduce(src, r, max(1, min(r.Dx()/dstW, r.Dy()/dstH)))
	cubicScale(dst, rows)
	return dst
}

// rowSource yields a source's premultiplied RGBA rows, w pixels wide
// and h tall.
type rowSource struct {
	w, h int
	row  func(y int) []uint8
}

// boxReduce reads r of src as premultiplied RGBA rows, each k x k
// block averaged into one pixel (k of 1 converts only). An RGBA source
// at k 1 is read in place.
func boxReduce(src image.Image, r image.Rectangle, k int) rowSource {
	read := rowReader(src, r)
	if k == 1 {
		return rowSource{w: r.Dx(), h: r.Dy(), row: read}
	}
	w, h := (r.Dx()+k-1)/k, (r.Dy()+k-1)/k
	sums := make([]uint32, w*4)
	out := make([]uint8, w*4)
	return rowSource{w: w, h: h, row: func(y int) []uint8 {
		clear(sums)
		y0, y1 := y*k, min((y+1)*k, r.Dy())
		for sy := y0; sy < y1; sy++ {
			line := read(sy)
			for x := range r.Dx() {
				o := (x / k) * 4
				sums[o] += uint32(line[x*4])
				sums[o+1] += uint32(line[x*4+1])
				sums[o+2] += uint32(line[x*4+2])
				sums[o+3] += uint32(line[x*4+3])
			}
		}
		for x := range w {
			n := uint32((min((x+1)*k, r.Dx()) - x*k) * (y1 - y0))
			for c := range 4 {
				out[x*4+c] = uint8((sums[x*4+c] + n/2) / n)
			}
		}
		return out
	}}
}

// rowReader returns row y (0 at r's top) of src over r as premultiplied
// RGBA, in a buffer reused between calls.
func rowReader(src image.Image, r image.Rectangle) func(y int) []uint8 {
	w := r.Dx()
	buf := make([]uint8, w*4)
	switch s := src.(type) {
	case *image.RGBA:
		return func(y int) []uint8 {
			o := s.PixOffset(r.Min.X, r.Min.Y+y)
			return s.Pix[o : o+w*4]
		}
	case *image.NRGBA:
		return func(y int) []uint8 {
			o := s.PixOffset(r.Min.X, r.Min.Y+y)
			for x := range w {
				p := s.Pix[o+x*4 : o+x*4+4]
				a := uint32(p[3])
				buf[x*4] = uint8((uint32(p[0])*a + 127) / 255)
				buf[x*4+1] = uint8((uint32(p[1])*a + 127) / 255)
				buf[x*4+2] = uint8((uint32(p[2])*a + 127) / 255)
				buf[x*4+3] = p[3]
			}
			return buf
		}
	case *image.YCbCr:
		return func(y int) []uint8 {
			for x := range w {
				yi := s.YOffset(r.Min.X+x, r.Min.Y+y)
				ci := s.COffset(r.Min.X+x, r.Min.Y+y)
				buf[x*4], buf[x*4+1], buf[x*4+2] = color.YCbCrToRGB(s.Y[yi], s.Cb[ci], s.Cr[ci])
				buf[x*4+3] = 0xff
			}
			return buf
		}
	case *image.Gray:
		return func(y int) []uint8 {
			o := s.PixOffset(r.Min.X, r.Min.Y+y)
			for x := range w {
				v := s.Pix[o+x]
				buf[x*4], buf[x*4+1], buf[x*4+2], buf[x*4+3] = v, v, v, 0xff
			}
			return buf
		}
	}
	return func(y int) []uint8 {
		for x := range w {
			cr, cg, cb, ca := src.At(r.Min.X+x, r.Min.Y+y).RGBA()
			buf[x*4], buf[x*4+1], buf[x*4+2], buf[x*4+3] = uint8(cr>>8), uint8(cg>>8), uint8(cb>>8), uint8(ca>>8)
		}
		return buf
	}
}

// taps are one output pixel's source indices and their weights.
type taps struct {
	first int
	w     []float32
}

// catmullRom is the cubic kernel at distance t (B 0, C 0.5).
func catmullRom(t float64) float64 {
	t = math.Abs(t)
	switch {
	case t < 1:
		return 1.5*t*t*t - 2.5*t*t + 1
	case t < 2:
		return -0.5*t*t*t + 2.5*t*t - 4*t + 2
	}
	return 0
}

// kernelTaps are the normalized CatmullRom taps of every one of dst
// output pixels over src source pixels, the kernel widened by the
// downscale so every source pixel contributes; indices past the edges
// clamp onto the edge pixels.
func kernelTaps(src, dst int) []taps {
	scale := float64(src) / float64(dst)
	support := 2 * max(scale, 1)
	stretch := max(scale, 1)
	out := make([]taps, dst)
	width := int(math.Ceil(2*support)) + 1
	backing := make([]float32, 0, dst*width)
	for d := range dst {
		center := (float64(d)+0.5)*scale - 0.5
		first := int(math.Floor(center-support)) + 1
		last := int(math.Floor(center + support))
		start := len(backing)
		sum := 0.0
		for i := first; i <= last; i++ {
			wgt := catmullRom((float64(i) - center) / stretch)
			backing = append(backing, float32(wgt))
			sum += wgt
		}
		ws := backing[start:len(backing):len(backing)]
		for i := range ws {
			ws[i] = float32(float64(ws[i]) / sum)
		}
		out[d] = taps{first: first, w: ws}
	}
	return out
}

// cubicScale fills dst from rows with the CatmullRom kernel, separably:
// each source row is filtered horizontally once, kept in a ring as
// long as the vertical taps reach it.
func cubicScale(dst *image.RGBA, rows rowSource) {
	dw, dh := dst.Rect.Dx(), dst.Rect.Dy()
	xt, yt := kernelTaps(rows.w, dw), kernelTaps(rows.h, dh)
	span := 0
	for _, t := range yt {
		span = max(span, len(t.w))
	}
	ring := make([][]float32, span)
	ringRow := make([]int, span)
	for i := range ring {
		ring[i] = make([]float32, dw*4)
		ringRow[i] = -1
	}
	clampRow := func(y int) int { return min(max(y, 0), rows.h-1) }
	filtered := func(y int) []float32 {
		slot := y % span
		if ringRow[slot] == y {
			return ring[slot]
		}
		line, out := rows.row(y), ring[slot]
		for dx, t := range xt {
			var acc [4]float32
			for i, wgt := range t.w {
				o := min(max(t.first+i, 0), rows.w-1) * 4
				acc[0] += wgt * float32(line[o])
				acc[1] += wgt * float32(line[o+1])
				acc[2] += wgt * float32(line[o+2])
				acc[3] += wgt * float32(line[o+3])
			}
			copy(out[dx*4:dx*4+4], acc[:])
		}
		ringRow[slot] = y
		return out
	}
	sums := make([]float32, dw*4)
	for dy, t := range yt {
		row := dst.Pix[dy*dst.Stride : dy*dst.Stride+dw*4]
		clear(sums)
		for i, wgt := range t.w {
			line := filtered(clampRow(t.first + i))
			for x, v := range line {
				sums[x] += wgt * v
			}
		}
		for x := 0; x < dw*4; x += 4 {
			a := byteOf(sums[x+3])
			row[x+3] = a
			row[x] = min(byteOf(sums[x]), a)
			row[x+1] = min(byteOf(sums[x+1]), a)
			row[x+2] = min(byteOf(sums[x+2]), a)
		}
	}
}

// byteOf rounds an accumulated channel into a byte.
func byteOf(v float32) uint8 { return uint8(clamp8(math.Round(float64(v)))) }
