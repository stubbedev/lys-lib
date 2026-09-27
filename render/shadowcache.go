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
)

// maxShadowBlur caps the kernel extent in device pixels. Theme blurs
// sit an order of magnitude below this; the cap exists so a corrupt
// or absurd blur cannot turn one frame into a multi-megapixel kernel
// sweep.
const maxShadowBlur = 64

// maxShadowCacheEntries and maxShadowCacheBytes bound the cache: a
// live desktop holds a handful of floating surfaces, so overflowing
// means a resize storm or a stream of one-shot geometries. On
// overflow the map resets and rebuilds - bounded, and a steady-state
// working set is never evicted.
const (
	maxShadowCacheEntries = 32
	maxShadowCacheBytes   = 8 << 20
)

// shadowKey identifies one cached raster: the mapped device-pixel rect
// size, the logical corner radius and blur, and the device scale they
// were rasterized at.
type shadowKey struct {
	w, h       int
	radius     int
	blur       int
	num, denom int
}

// shadowRaster is one cached coverage field. cov holds quantized
// coverage (0..255) for the rect grown by inset device pixels on every
// side, row-major from the box's top-left; inset is how far the box
// edge sits from the rect edge, so callers index with
// (x - (rect.X - inset), y - (rect.Y - inset)).
type shadowRaster struct {
	w, h  int
	inset int
	cov   []uint8
}

var (
	shadowMu     sync.Mutex
	shadowCache  = map[shadowKey]*shadowRaster{}
	shadowHits   int
	shadowMisses int
)

// shadowRasterFor returns the cached coverage raster for a shadow
// around a devW x devH device-pixel rect at the given device scale,
// building it on first use. Canvases paint on several goroutines (the
// application loop and a grabbed menu's nested loop), so the map is
// lock-guarded.
func shadowRasterFor(w, h, radius, blur, num, denom int) *shadowRaster {
	key := shadowKey{w: w, h: h, radius: radius, blur: blur, num: num, denom: denom}
	shadowMu.Lock()
	defer shadowMu.Unlock()
	if ras := shadowCache[key]; ras != nil {
		shadowHits++
		return ras
	}
	shadowMisses++
	ras := buildShadowRaster(w, h, radius, blur, num, denom)
	if len(shadowCache) >= maxShadowCacheEntries || shadowCacheBytes()+ras.size() > maxShadowCacheBytes {
		shadowCache = map[shadowKey]*shadowRaster{}
	}
	shadowCache[key] = ras
	return ras
}

// shadowCacheBytes sums the cached rasters' size; the caller holds
// shadowMu.
func shadowCacheBytes() int {
	total := 0
	for _, ras := range shadowCache {
		total += ras.size()
	}
	return total
}

func (r *shadowRaster) size() int { return len(r.cov) }

// buildShadowRaster computes the coverage field once: the signed
// distance to the (radius-rounded, device-space) rect, mapped through
// the cumulative Gaussian kernel. Pixels inside the silhouette are
// full; coverage then falls off monotonically and reaches zero at the
// kernel extent, which is also the box inset.
func buildShadowRaster(w, h, radius, blur, num, denom int) *shadowRaster {
	devBlur := max(1, min(divCeil(blur*num, denom), maxShadowBlur))
	inset := devBlur + 1
	devRadius := max(0, min(divCeil(radius*num, denom), min(w, h)/2))
	rect := Rect{X: inset, Y: inset, W: w, H: h}
	ras := &shadowRaster{
		w:     w + 2*inset,
		h:     h + 2*inset,
		inset: inset,
		cov:   make([]uint8, (w+2*inset)*(h+2*inset)),
	}
	tail, half := shadowTail(devBlur)
	for y := range ras.h {
		for x := range ras.w {
			d := sdRoundRect(float64(x)+0.5, float64(y)+0.5, rect, float64(devRadius))
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
func shadowTail(devBlur int) (tail []float64, half int) {
	sigma := float64(devBlur) / 2
	half = devBlur
	weight := make([]float64, half+1)
	for i := range weight {
		weight[i] = math.Exp(-0.5 * float64(i*i) / (sigma * sigma))
	}
	total := weight[0]
	for i := 1; i <= half; i++ {
		total += 2 * weight[i]
	}
	tail = make([]float64, half+2)
	for j := half; j >= 0; j-- {
		mass := weight[j] / total
		if j > 0 {
			mass *= 2 // the symmetric negative side folds in
		}
		tail[j] = tail[j+1] + mass
	}
	return tail, half
}

// tailCoverage quantizes the kernel tail at signed distance d: full
// inside the silhouette, zero past the kernel extent.
func tailCoverage(tail []float64, half int, d float64) uint8 {
	if d <= 0 {
		return 255
	}
	j := int(math.Round(d))
	if j > half {
		return 0
	}
	v := uint32(tail[j]*255 + 0.5)
	return uint8(min(v, uint32(255)))
}
