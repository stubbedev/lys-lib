package render

import (
	"encoding/binary"
	"hash/maphash"
	"image"
	"image/draw"
	"math"
	"sync"

	"github.com/stubbedev/lys-lib/third_party/vector"
)

// The clip-path machinery: a CSS clip shape becomes a Path (inset with
// rounded corners, circle and ellipse as four-arc cubics, polygon, or
// a path() string), and the path's coverage multiplies a layer's
// pixels — the same alpha-mask step a mask-image goes through.

// kappa is the cubic Bézier circle constant: the control-point handle
// for a quarter arc.
const kappa = 0.5522847498307936

// RoundedRectPath appends the rounded rect r (logical pixels, radii
// clockwise from top-left) to p as one closed subpath.
func RoundedRectPath(p *Path, r Rect, radii Corners) {
	alone := p.Empty()
	tl := clampRadius(radii.TopLeft, r.W, r.H)
	tr := clampRadius(radii.TopRight, r.W, r.H)
	br := clampRadius(radii.BottomRight, r.W, r.H)
	bl := clampRadius(radii.BottomLeft, r.W, r.H)
	x0, y0 := float64(r.X), float64(r.Y)
	x1, y1 := float64(r.X+r.W), float64(r.Y+r.H)
	p.MoveTo(x0+tl, y0)
	p.LineTo(x1-tr, y0)
	if k := float64(kappa * tr); k > 0 {
		p.CubeTo(x1-tr+k, y0, x1, y0+tr-k, x1, y0+tr)
	}
	p.LineTo(x1, y1-br)
	if k := float64(kappa * br); k > 0 {
		p.CubeTo(x1, y1-br+k, x1-br+k, y1, x1-br, y1)
	}
	p.LineTo(x0+bl, y1)
	if k := float64(kappa * bl); k > 0 {
		p.CubeTo(x0+bl-k, y1, x0, y1-bl+k, x0, y1-bl)
	}
	p.LineTo(x0, y0+tl)
	if k := float64(kappa * tl); k > 0 {
		p.CubeTo(x0, y0+tl-k, x0+tl-k, y0, x0+tl, y0)
	}
	p.Close()
	p.rounded, p.rect, p.radii = alone, r, radii
}

// RoundedRect is the rounded rect p is when RoundedRectPath alone made
// it and it lands on whole device pixels at num/denom with radii that need
// no clamping, the cases where a distance field paints the outline's shape:
// a renderer then fills or masks it without rasterizing.
func (p *Path) RoundedRect(num, denom int) (Rect, Corners, bool) {
	if !p.rounded {
		return Rect{}, Corners{}, false
	}
	r, c := p.rect, p.radii
	for _, v := range [4]int{r.X, r.Y, r.W, r.H} {
		if v*num%denom != 0 {
			return Rect{}, Corners{}, false
		}
	}
	limit := min(r.W, r.H)
	if 2*max(c.TopLeft, c.TopRight, c.BottomRight, c.BottomLeft) > limit {
		return Rect{}, Corners{}, false
	}
	return r, c, true
}

// clampRadius bounds a corner radius by half of each side it spans.
func clampRadius(rad, w, h int) float64 {
	return math.Min(float64(rad), float64(min(w, h))/2)
}

// EllipsePath appends the ellipse centered at (cx, cy) with radii rx
// and ry (logical pixels) to p as one closed subpath.
func EllipsePath(p *Path, cx, cy, rx, ry float64) {
	if rx <= 0 || ry <= 0 {
		return
	}
	kx, ky := float64(kappa*rx), float64(kappa*ry)
	p.MoveTo(cx+rx, cy)
	p.CubeTo(cx+rx, cy+ky, cx+kx, cy+ry, cx, cy+ry)
	p.CubeTo(cx-kx, cy+ry, cx-rx, cy+ky, cx-rx, cy)
	p.CubeTo(cx-rx, cy-ky, cx-kx, cy-ry, cx, cy-ry)
	p.CubeTo(cx+kx, cy-ry, cx+rx, cy-ky, cx+rx, cy)
	p.Close()
}

// PathScratch is what rasterizing a path works in, reused from call to
// call: it allocates only to grow.
type PathScratch struct {
	z    *vector.Rasterizer
	mask image.Alpha
}

var pathScratches sync.Pool

func takePathScratch() *PathScratch {
	if sc, ok := pathScratches.Get().(*PathScratch); ok {
		return sc
	}
	return &PathScratch{}
}

// coverage rasterizes p's fill coverage over box (device pixels, p scaled
// by the device scale); the image is valid until the next call. Nil when
// nothing covers.
func (sc *PathScratch) coverage(p *Path, box Rect, scale float32) *image.Alpha {
	if p == nil || p.Empty() || box.Empty() {
		return nil
	}
	if sc.z == nil {
		sc.z = vector.NewRasterizer(box.W, box.H)
	} else {
		sc.z.Reset(box.W, box.H)
	}
	z := sc.z
	mx := func(x float64) float32 { return float32(float32(x)*scale) - float32(box.X) }
	my := func(y float64) float32 { return float32(float32(y)*scale) - float32(box.Y) }
	walkPath(p,
		func(x, y float64) { z.MoveTo(mx(x), my(y)) },
		func(x, y float64) { z.LineTo(mx(x), my(y)) },
		func(x1, y1, x2, y2, x, y float64) {
			z.CubeTo(mx(x1), my(y1), mx(x2), my(y2), mx(x), my(y))
		},
		func() { z.ClosePath() })
	if n := box.W * box.H; cap(sc.mask.Pix) < n {
		sc.mask.Pix = make([]uint8, n)
	} else {
		sc.mask.Pix = sc.mask.Pix[:n]
	}
	sc.mask.Stride, sc.mask.Rect = box.W, image.Rect(0, 0, box.W, box.H)
	z.DrawOp = draw.Src
	z.Draw(&sc.mask, sc.mask.Bounds(), image.Opaque, image.Point{})
	return &sc.mask
}

// walkPath replays p's segments through the move, line, cube and
// close callbacks.
func walkPath(p *Path, move func(float64, float64), line func(float64, float64), cube func(x1, y1, x2, y2, x, y float64), close func()) {
	open := false
	for _, op := range p.ops {
		switch op.kind {
		case pathMove:
			if open {
				close()
			}
			move(op.pts[0][0], op.pts[0][1])
			open = true
		case pathLine:
			line(op.pts[0][0], op.pts[0][1])
		case pathCube:
			cube(op.pts[0][0], op.pts[0][1], op.pts[1][0], op.pts[1][1], op.pts[2][0], op.pts[2][1])
		case pathClose:
			if open {
				close()
				open = false
			}
		}
	}
	if open {
		close()
	}
}

// PathMask is the path's coverage at device scale num/denom over box,
// the part of region its bounds reach (nil when they miss it), valid until
// the next call; the rest of region is uncovered. It is for a path too large
// for PathCoverageMask.
func (sc *PathScratch) PathMask(p *Path, region Rect, num, denom int) (*image.Alpha, Rect) {
	scale := devScale(num, denom)
	box := p.bounds(scale).Intersect(region)
	if box.Empty() {
		return nil, box
	}
	return sc.coverage(p, box, scale), box
}

// maxCachedPathMask bounds the coverage kept per path: past it a path
// rasterizes only the part a frame needs, each frame.
const maxCachedPathMask = 512 * 512

type pathMaskKey struct {
	hash uint64
	w, h int
}

type pathMask struct{ covRaster }

func (m *pathMask) size() int { return len(m.cov) }

var pathMasks rasterCache[pathMaskKey, *pathMask]

var pathHashSeeding struct {
	once sync.Once
	seed maphash.Seed
}

// pathHashSeed is the seed of the path hashes, drawn when the first
// path is hashed.
func pathHashSeed() maphash.Seed {
	pathHashSeeding.once.Do(func() { pathHashSeeding.seed = maphash.MakeSeed() })
	return pathHashSeeding.seed
}

func (p *Path) contentHash(scale float32, origin Rect) uint64 {
	var h maphash.Hash
	h.SetSeed(pathHashSeed())
	var b [12]byte
	for i := range p.ops {
		op := &p.ops[i]
		n := 1
		switch op.kind {
		case pathClose:
			n = 0
		case pathCube:
			n = 3
		}
		b[0] = byte(op.kind)
		_, _ = h.Write(b[:1])
		for _, pt := range op.pts[:n] {
			binary.LittleEndian.PutUint32(b[0:], math.Float32bits(float32(float32(pt[0])*scale)-float32(origin.X)))
			binary.LittleEndian.PutUint32(b[4:], math.Float32bits(float32(float32(pt[1])*scale)-float32(origin.Y)))
			_, _ = h.Write(b[:8])
		}
	}
	return h.Sum64()
}

// PathCoverageMask is p's whole coverage at device scale num/denom, cached
// by content whatever whole pixels it sits at, and the device rect it
// covers; false for a path past maxCachedPathMask or with no area. FillPath
// blends its color by it and ApplyPathMask multiplies a layer's region by it.
func PathCoverageMask(p *Path, num, denom int) (CoverageMask, Rect, bool) {
	scale := devScale(num, denom)
	at := p.bounds(scale)
	if at.Empty() || at.W*at.H > maxCachedPathMask {
		return nil, Rect{}, false
	}
	m := pathMasks.get(pathMaskKey{hash: p.contentHash(scale, at), w: at.W, h: at.H}, func() *pathMask {
		sc := takePathScratch()
		defer pathScratches.Put(sc)
		cov := sc.coverage(p, at, scale)
		m := &pathMask{}
		m.w, m.h = at.W, at.H
		m.cov = append([]uint8(nil), cov.Pix...)
		return m
	})
	return m, at, true
}

// ApplyPathMask multiplies l's pixels inside region by the path's
// coverage, scaled by the layer's device scale: the clip-path and the
// alpha mask both land here.
func (l *Layer) ApplyPathMask(p *Path, region Rect) {
	if l.cv.rec != nil {
		l.cv.record(opArgs{kind: opPathMask, layer: l, path: p, r: region})
		return
	}
	region = region.Intersect(l.cv.Rect())
	if region.Empty() {
		return
	}
	if r, radii, ok := p.RoundedRect(l.cv.num, l.cv.denom); ok {
		l.applyRoundedMask(r, radii, region)
		return
	}
	var pix []uint8
	var stride int
	var box Rect
	if m, at, ok := PathCoverageMask(p, l.cv.num, l.cv.denom); ok {
		pix, stride, box = m.Pix(), at.W, at
	} else {
		sc := takePathScratch()
		defer pathScratches.Put(sc)
		var mask *image.Alpha
		if mask, box = sc.PathMask(p, region, l.cv.num, l.cv.denom); mask != nil {
			pix, stride = mask.Pix, mask.Stride
		}
	}
	for y := region.Y; y < region.Y+region.H; y++ {
		for x := region.X; x < region.X+region.W; x++ {
			var cov uint32
			if pix != nil && x >= box.X && x < box.X+box.W && y >= box.Y && y < box.Y+box.H {
				cov = uint32(pix[(y-box.Y)*stride+x-box.X])
			}
			if cov == 255 {
				continue
			}
			col := l.cv.get(x, y)
			if cov == 0 {
				l.cv.set(x, y, 0)
				continue
			}
			l.cv.set(x, y, coverageScale(col, cov))
		}
	}
}

func (l *Layer) applyRoundedMask(r Rect, radii Corners, region Rect) {
	b, rc := DeviceBox(r, radii, l.cv.num, l.cv.denom)
	for y := region.Y; y < region.Y+region.H; y++ {
		py := float32(y) + 0.5
		for x := region.X; x < region.X+region.W; x++ {
			cov := cov255(coverage(sdBoxF(float32(x)+0.5, py, b, rc)))
			if cov == 255 {
				continue
			}
			if cov == 0 {
				l.cv.set(x, y, 0)
				continue
			}
			l.cv.set(x, y, coverageScale(l.cv.get(x, y), cov))
		}
	}
}

// ApplyMaskLayer multiplies l's pixels inside region by the mask
// layer's pixels: the alpha channel for mask-mode alpha, the
// luminance (WCAG weights over the straight channels) for
// mask-mode luminance. The mask is positioned at its own coordinates.
func (l *Layer) ApplyMaskLayer(mask *Layer, region Rect, luminance bool) {
	if mask == nil {
		return
	}
	if l.cv.rec != nil {
		l.cv.record(opArgs{kind: opMaskLayer, layer: l, other: mask, r: region, flag: luminance})
		return
	}
	region = region.Intersect(l.cv.Rect()).Intersect(mask.cv.Rect())
	if region.Empty() {
		return
	}
	for y := region.Y; y < region.Y+region.H; y++ {
		for x := region.X; x < region.X+region.W; x++ {
			m := mask.cv.get(x, y)
			var cov uint32
			if luminance {
				s := m.Straight()
				cov = (uint32(s[0])*77 + uint32(s[1])*151 + uint32(s[2])*28) / 256 * uint32(s[3]) / 255
			} else {
				cov = uint32(m.A())
			}
			if cov == 255 {
				continue
			}
			col := l.cv.get(x, y)
			if cov == 0 {
				l.cv.set(x, y, 0)
				continue
			}
			l.cv.set(x, y, coverageScale(col, cov))
		}
	}
}
