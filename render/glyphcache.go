package render

// The glyph atlas: a process-wide cache of rasterized glyphs, keyed by
// (face, scale, glyph ID, subpixel bucket). Without it every Draw
// re-traces every outline through the rasterizer - a repainted label
// re-rasterizes its whole alphabet per frame - and every embedded
// bitmap strike re-scales through CatmullRom. Rasters are stored as
// 8bpp coverage masks and tinted at blit time, so one cached mask
// serves every text color; blits go through Canvas.blend like every
// other primitive, keeping the PushAlpha and premultiplied discipline
// by construction.

import (
	"image"
	"image/color"
	"math"

	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"golang.org/x/image/draw"
	"golang.org/x/image/vector"
)

// glyphKey pins one rasterized outline. scale is the device pixels per
// font unit the glyph rasterized at (the product of logical size and
// device scale); fx, fy are the quarter-pixel buckets of the fractional
// pen offset the mask was rendered with. A strike bitmap's pixels are a
// pure function of (face, glyph, scaled size) - the strike choice is
// itself a function of the face and glyph - so the bitmap cache keys on
// those three.
type glyphKey struct {
	face  *Typeface
	scale float64
	gid   font.GID
	fx    uint8
	fy    uint8
}

type bitmapKey struct {
	face *Typeface
	gid  font.GID
	w, h int
}

// glyphRaster is one cached 8bpp coverage mask. ox, oy locate the
// mask's origin relative to the glyph's integer pen position: the
// blit's target pixel is (penX + ox, penY + oy).
type glyphRaster struct {
	mask []uint8
	w, h int
	ox   int
	oy   int
}

var (
	glyphs = newLRU[glyphKey, *glyphRaster](glyphCacheBudget)
	// bitmapScales caches embedded bitmap strikes - the color-emoji
	// PNGs and black-and-white masks - scaled to device size. The
	// scale is the expensive half (CatmullRom over a PNG-sized
	// source); the decode is already cached on the Typeface.
	bitmapScales = newLRU[bitmapKey, *image.RGBA](glyphCacheBudget)
)

// subpixelBucket quantizes the fractional part of a pen coordinate:
// four buckets in [0,1).
func subpixelBucket(frac float64) uint8 {
	b := int(frac * subpixelBuckets)
	if b >= subpixelBuckets {
		b = subpixelBuckets - 1
	}
	return uint8(b)
}

// cachedOutlineRaster returns the atlas entry for the outline glyph,
// rasterizing it into the cache on a miss. ok is false when the face
// has no outline segments for gid (a space, or a bitmap-only glyph);
// that negative is cached too, so blank glyphs stop re-parsing their
// empty outlines per draw.
func cachedOutlineRaster(t *Typeface, gid font.GID, scale, penX, penY float64) (*glyphRaster, bool) {
	fx, fy := penX-math.Floor(penX), penY-math.Floor(penY)
	key := glyphKey{
		face:  t,
		scale: scale,
		gid:   gid,
		fx:    subpixelBucket(fx),
		fy:    subpixelBucket(fy),
	}
	if g, ok := glyphs.get(key); ok {
		if g == nil {
			// Cached negative: the face has no outline segments for
			// this glyph (a space, or a bitmap-only emoji). The
			// caller falls through to the bitmap path.
			return nil, false
		}
		return g, true
	}
	outline, ok := t.face.GlyphDataOutline(gid)
	if !ok || len(outline.Segments) == 0 {
		// Cache the negative so blank glyphs stop re-parsing their
		// (empty) outlines per draw.
		glyphs.put(key, nil, 16)
		return nil, false
	}
	g := rasterOutline(outline, scale, fx, fy)
	glyphs.put(key, g, len(g.mask))
	return g, true
}

// rasterOutline rasterizes one glyph outline into an 8bpp coverage
// mask, with the outline's fractional pen offset (fx, fy) at the mask's
// coordinates and the mask origin at the integer pen position. The
// bounds grow a pixel on every side, the anti-aliasing margin the
// per-pixel blit has always worked in.
func rasterOutline(outline font.GlyphOutline, scale, fx, fy float64) *glyphRaster {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, seg := range outline.Segments {
		for _, p := range seg.Args {
			x := fx + float64(p.X)*scale
			y := fy - float64(p.Y)*scale
			minX, minY = math.Min(minX, x), math.Min(minY, y)
			maxX, maxY = math.Max(maxX, x), math.Max(maxY, y)
		}
	}
	ox := int(math.Floor(minX)) - 1
	oy := int(math.Floor(minY)) - 1
	w := int(math.Ceil(maxX)) + 1 - ox
	h := int(math.Ceil(maxY)) + 1 - oy
	if w <= 0 || h <= 0 {
		return &glyphRaster{}
	}
	rast := vector.NewRasterizer(w, h)
	toRaster := func(p ot.SegmentPoint) (float32, float32) {
		return float32(fx+float64(p.X)*scale) - float32(ox),
			float32(fy-float64(p.Y)*scale) - float32(oy)
	}
	for _, seg := range outline.Segments {
		switch seg.Op {
		case ot.SegmentOpMoveTo:
			x, y := toRaster(seg.Args[0])
			rast.MoveTo(x, y)
		case ot.SegmentOpLineTo:
			x, y := toRaster(seg.Args[0])
			rast.LineTo(x, y)
		case ot.SegmentOpQuadTo:
			cx, cy := toRaster(seg.Args[0])
			ex, ey := toRaster(seg.Args[1])
			rast.QuadTo(cx, cy, ex, ey)
		case ot.SegmentOpCubeTo:
			c1x, c1y := toRaster(seg.Args[0])
			c2x, c2y := toRaster(seg.Args[1])
			ex, ey := toRaster(seg.Args[2])
			rast.CubeTo(c1x, c1y, c2x, c2y, ex, ey)
		}
	}
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	rast.Draw(mask, mask.Bounds(), image.NewUniform(color.Alpha{A: 255}), image.Point{})
	return &glyphRaster{mask: mask.Pix, w: w, h: h, ox: ox, oy: oy}
}

// cachedScaledBitmap returns the embedded bitmap strike scaled to the
// device size w×h, scaling and caching on a miss. The caller has
// already resolved bg from t.bitmap (which skips faces with no
// decodable strike), so the cache stores the scaled result, the half
// that costs (CatmullRom over a PNG-sized source).
func cachedScaledBitmap(t *Typeface, gid font.GID, bg bitmapGlyph, w, h int) *image.RGBA {
	key := bitmapKey{face: t, gid: gid, w: w, h: h}
	if img, ok := bitmapScales.get(key); ok {
		return img
	}
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), bg.img, bg.img.Bounds(), draw.Over, nil)
	bitmapScales.put(key, scaled, w*h*4)
	return scaled
}
