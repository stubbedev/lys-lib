// The shadow raster cache. Canvas.Shadow paints a blurred rounded-rect
// silhouette; the expensive half of that is the coverage field - the
// Gaussian kernel and the per-pixel signed-distance sweep - and it
// depends only on (rect size, corner radius, blur, device scale),
// never on the color or what is underneath. This file builds each
// field once and hands the cached raster back, so a hover twitch that
// repaints the same floating surface every frame re-blends the ring
// but never re-Gaussians it. The counters double as the paint-count
// proof: a second identical Shadow must be a hit, not a rebuild.
package render

import (
	"math"
	"sync"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// maxShadowBlur caps the kernel extent in device pixels. Theme blurs
// sit an order of magnitude below this; the cap exists so a corrupt
// or absurd blur cannot turn one frame into a multi-megapixel kernel
// sweep.
const maxShadowBlur = 64

// maxShadowCacheEntries and maxShadowCacheBytes bound each cache: a
// live desktop holds a handful of floating surfaces, so overflowing
// means a resize storm or a stream of one-shot geometries. On
// overflow the map resets and rebuilds - bounded, and a steady-state
// working set is never evicted.
const (
	maxShadowCacheEntries = 32
	maxShadowCacheBytes   = 8 << 20
)

// covRaster is one cached coverage field: quantized coverage (0..255),
// w x h, row-major. Its owner records where it sits relative to the
// box it shades.
type covRaster struct {
	identity
	w, h int
	cov  []uint8
}

// Pix is the field's coverage, Size's width bytes per row.
func (r *covRaster) Pix() []uint8 { return r.cov }

// Size is the field's size in device pixels.
func (r *covRaster) Size() (w, h int) { return r.w, r.h }

// CoverageMask is an immutable 8bpp coverage mask render caches and
// blends a color through: a glyph's (GlyphMask), a box-shadow core's
// or a rect shadow's (ShadowMask). A GPU renderer keys its
// mask atlas by its ID, which no other mask shares, whatever memory
// either lives in.
type CoverageMask interface {
	ID() ImageID
	Pix() []uint8
	Size() (w, h int)
}

// RectShadow is how Shadow paints a shadow of blur around the logical
// rect r with cornerRadius at device scale num/denom, as BoxShadowMask
// describes: the cached mask of a core box coreW x coreH and the box's
// device rect; false when there is none to paint. The mask depends on the
// box only through the rounded corners it clamps, so a resize rebuilds
// nothing.
func RectShadow(r Rect, cornerRadius, blur, num, denom int) (m *ShadowMask, box Rect, coreW, coreH int, ok bool) {
	if blur < 1 {
		return nil, Rect{}, 0, 0, false
	}
	box = MapRect(r, num, denom)
	if box.Empty() {
		return nil, Rect{}, 0, 0, false
	}
	devBlur := shadowInset(blur, num, denom) - 1
	devRadius := max(0, min(divCeil(cornerRadius*num, denom), min(box.W, box.H)/2))
	coreW, coreH = rectShadowCore(box.W, devRadius), rectShadowCore(box.H, devRadius)
	key := shadowKey{w: coreW, h: coreH, radius: devRadius, blur: devBlur}
	m = shadows.get(key, func() *ShadowMask { return buildRectShadow(key) })
	return m, box, coreW, coreH, true
}

func rectShadowCore(side, devRadius int) int { return min(side, 2*devRadius+3) }

func rectShadowArea(box Rect, m *ShadowMask, coreW, coreH int) Rect {
	return Rect{X: box.X + m.x0, Y: box.Y + m.y0, W: m.w + box.W - coreW, H: m.h + box.H - coreH}
}

// rasterCache memoizes coverage rasters by geometry. Canvases paint on
// several goroutines (the application loop and a grabbed menu's nested
// loop), so the map is lock-guarded. The counters are the paint-count
// proof: a repaint of the same geometry must hit, not rebuild.
type rasterCache[K comparable, V interface{ size() int }] struct {
	mu           sync.Mutex
	m            map[K]V
	hits, misses int
}

// get returns k's raster, building it on first use, outside the lock so
// another canvas's hit never waits for this one's miss.
func (c *rasterCache[K, V]) get(k K, build func() V) V {
	c.mu.Lock()
	if v, ok := c.m[k]; ok {
		c.hits++
		c.mu.Unlock()
		return v
	}
	c.misses++
	c.mu.Unlock()
	v := build()
	c.mu.Lock()
	defer c.mu.Unlock()
	if have, ok := c.m[k]; ok {
		return have
	}
	total := v.size()
	for _, cached := range c.m {
		total += cached.size()
	}
	if c.m == nil || len(c.m) >= maxShadowCacheEntries || total > maxShadowCacheBytes {
		c.m = map[K]V{}
	}
	c.m[k] = v
	return v
}

// counts reads and resets the hit and miss counters.
func (c *rasterCache[K, V]) counts() (hits, misses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hits, misses = c.hits, c.misses
	c.hits, c.misses = 0, 0
	return hits, misses
}

// shadowKey identifies one theme shadow raster, all in device pixels:
// the core box's size, the corner radius clamped to the whole box, and
// the blur.
type shadowKey struct {
	w, h, radius, blur int
}

var shadows rasterCache[shadowKey, *ShadowMask]

// shadowInset is how far a Shadow's raster reaches past its device
// box: the device blur, clamped, and a pixel.
func shadowInset(blur, num, denom int) int {
	return max(1, min(divCeil(blur*num, denom), maxShadowBlur)) + 1
}

// buildRectShadow computes the coverage field once: the signed distance
// to the (radius-rounded, device-space) core rect, mapped through the
// cumulative Gaussian kernel. Pixels inside the silhouette are full;
// coverage then falls off monotonically and reaches zero at the kernel
// extent, which is also the box inset.
func buildRectShadow(k shadowKey) *ShadowMask {
	inset := k.blur + 1
	rect := Rect{X: inset, Y: inset, W: k.w, H: k.h}
	ras := &ShadowMask{x0: -inset, y0: -inset}
	ras.w, ras.h = k.w+2*inset, k.h+2*inset
	ras.cov = make([]uint8, ras.w*ras.h)
	tail, half := shadowTail(k.blur)
	for y := range ras.h {
		for x := range ras.w {
			d := sdRoundRect(float32(x)+0.5, float32(y)+0.5, rect, float32(k.radius))
			ras.cov[y*ras.w+x] = tailCoverage(tail, half, d)
		}
	}
	return ras
}

// shadowTail builds the cumulative half-kernel of a unit-mass Gaussian
// with sigma devBlur/2 - the CSS box-shadow convention, where the blur
// radius spans two sigmas - truncated at the blur radius. tail[j] is
// the kernel mass at distance >= j from the silhouette: the blurred
// straight-edge coverage at pixel-center distance d is
// tail[round(d)], monotone in d, 1 at the edge, 0 past half. The
// truncation discards roughly exp(-2) of one tail, invisible at uint8
// resolution and what keeps every kernel bounded by the blur.
func shadowTail(devBlur int) (tail []float32, half int) {
	sigma := float64(devBlur) / 2
	half = devBlur
	weight := make([]float64, half+1)
	for i := range weight {
		weight[i] = gomath.Exp(-0.5 * float64(i*i) / (sigma * sigma))
	}
	total := weight[0]
	for i := 1; i <= half; i++ {
		total += float64(2 * weight[i])
	}
	sum := make([]float64, half+2)
	tail = make([]float32, half+2)
	for j := half; j >= 0; j-- {
		mass := weight[j] / total
		if j > 0 {
			mass *= 2 // the symmetric negative side folds in
		}
		sum[j] = sum[j+1] + mass
		tail[j] = float32(sum[j])
	}
	return tail, half
}

// tailCoverage quantizes the kernel tail at signed distance d: full
// inside the silhouette, zero past the kernel extent.
func tailCoverage(tail []float32, half int, d float32) uint8 {
	if d <= 0 {
		return 255
	}
	j := int(math.Round(float64(d)))
	if j > half {
		return 0
	}
	return uint8(min(cov255(tail[j]), 255))
}
