package render

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"math"

	"github.com/go-text/typesetting/font"
)

// Color glyphs: COLR (v0 layers and the v1 paint graph, colr.go) and
// OT-SVG documents paint their own colors - they win over the text
// color, which reaches them only where the font asks for it (COLR's
// foreground palette entry, an SVG's currentColor). They take
// precedence over a glyph's monochrome fallback outline, and render
// into the same atlas budget as bitmap strikes, keyed by face, glyph,
// scale, subpixel pen bucket, and text color.

// colorKind says which color source a glyph has.
type colorKind uint8

const (
	colorNone colorKind = iota + 1
	colorCOLR
	colorSVG
)

// colorKey pins one rendered color glyph.
type colorKey struct {
	face   *Typeface
	gid    font.GID
	scale  float64
	fx, fy uint8
	fg     Color
}

// colorRaster is a rendered color glyph and its origin relative to the
// integer pen.
type colorRaster struct {
	img    *image.RGBA
	ox, oy int
}

var colorGlyphs = newLRU[colorKey, *colorRaster](glyphCacheBudget)

// colorOf memoizes which color source gid has, so the common
// monochrome glyph costs one map lookup per draw.
func (t *Typeface) colorOf(gid font.GID) colorKind {
	if k, ok := t.colors[gid]; ok {
		return k
	}
	k := colorNone
	if _, ok := t.face.GlyphDataColor(gid); ok {
		k = colorCOLR
	} else if _, ok := t.face.GlyphDataSVG(gid); ok {
		k = colorSVG
	}
	if t.colors == nil {
		t.colors = map[font.GID]colorKind{}
	}
	t.colors[gid] = k
	return k
}

// drawColorGlyph paints gid from its color source; false when it has
// none (or it failed to render), leaving the monochrome paths.
func drawColorGlyph(cv *Canvas, clip Rect, t *Typeface, gid font.GID, scale, penX, penY float64, col Color) bool {
	kind := t.colorOf(gid)
	if kind == colorNone {
		return false
	}
	fx, fy := penX-math.Floor(penX), penY-math.Floor(penY)
	key := colorKey{face: t, gid: gid, scale: scale, fx: subpixelBucket(fx), fy: subpixelBucket(fy), fg: col}
	r, ok := colorGlyphs.get(key)
	if !ok {
		var img *image.RGBA
		var ox, oy int
		var good bool
		if kind == colorCOLR {
			img, ox, oy, good = renderCOLR(t, gid, scale, fx, fy, col)
		} else {
			img, ox, oy, good = renderSVGGlyph(t, gid, scale, fx, fy, col)
		}
		if !good {
			r = nil
		} else {
			r = &colorRaster{img: img, ox: ox, oy: oy}
		}
		cost := 16
		if r != nil {
			cost = len(img.Pix)
		}
		colorGlyphs.put(key, r, cost)
	}
	if r == nil {
		return false
	}
	x0, y0 := int(math.Floor(penX))+r.ox, int(math.Floor(penY))+r.oy
	b := r.img.Bounds()
	for y := range b.Dy() {
		py := y0 + y
		if py < clip.Y || py >= clip.Y+clip.H {
			continue
		}
		for x := range b.Dx() {
			px := x0 + x
			if px < clip.X || px >= clip.X+clip.W {
				continue
			}
			i := y*r.img.Stride + x*4
			if a := r.img.Pix[i+3]; a != 0 {
				cv.blend(px, py, Color(uint32(a)<<24|uint32(r.img.Pix[i])<<16|uint32(r.img.Pix[i+1])<<8|uint32(r.img.Pix[i+2])))
			}
		}
	}
	return true
}

// renderSVGGlyph renders gid's OT-SVG document. SVG user space has its
// origin at the glyph origin with y down; the viewBox sets the scale
// (its width spans the em), as FreeType's and HarfBuzz's SVG paths
// read it. The rendered region is the em box widened to the fallback
// outline's ink. currentColor paints in the text color. A document
// shared by several glyphs is cut down to this glyph's element and the
// definitions (svgGlyphElement).
func renderSVGGlyph(t *Typeface, gid font.GID, scale, fx, fy float64, fg Color) (*image.RGBA, int, int, bool) {
	g, ok := t.face.GlyphDataSVG(gid)
	if !ok {
		return nil, 0, 0, false
	}
	src := svgGlyphElement(g.Source, gid)
	if bytes.Contains(src, []byte("currentColor")) {
		src = bytes.ReplaceAll(src, []byte("currentColor"), []byte(hexColor(fg)))
	}
	icon, err := parseSVG(src)
	if err != nil {
		return nil, 0, 0, false
	}
	k := 1.0 // font units per user unit
	if g.ViewBox.Width > 0 {
		k = t.upem / float64(g.ViewBox.Width)
	}
	// The font-unit region: the em and the advance across (padded),
	// ascender to descender, and whatever the fallback outline inks.
	x0, x1 := 0.0, t.upem
	y0, y1 := -0.25*t.upem, t.upem
	if ext, ok := t.face.FontHExtents(); ok {
		y0, y1 = float64(ext.Descender), float64(ext.Ascender)
	}
	x1 = math.Max(x1, float64(t.face.HorizontalAdvance(gid)))
	for _, seg := range g.Outline.Segments {
		for _, a := range seg.Args {
			x0, x1 = math.Min(x0, float64(a.X)), math.Max(x1, float64(a.X))
			y0, y1 = math.Min(y0, float64(a.Y)), math.Max(y1, float64(a.Y))
		}
	}
	pad := 0.1 * (x1 - x0)
	x0, x1 = x0-pad, x1+pad
	base := Affine{A: scale, D: -scale, E: fx, F: fy}
	box := base.MapBounds(Rect{X: int(math.Floor(x0)), Y: int(math.Floor(y0)), W: int(math.Ceil(x1-x0)) + 1, H: int(math.Ceil(y1-y0)) + 1})
	if box.W <= 0 || box.H <= 0 || box.W > 4096 || box.H > 4096 {
		return nil, 0, 0, false
	}
	// The device box in user units: device x = fx + ux*k*scale, device
	// y = fy + uy*k*scale (user y down, like the device).
	u := k * scale
	img := rasterSVG(icon, (float64(box.X)-fx)/u, (float64(box.Y)-fy)/u, float64(box.W)/u, float64(box.H)/u, box.W, box.H)
	return img, box.X, box.Y, true
}

// svgGlyphElement cuts a shared OT-SVG document down to glyph gid: the
// root element, its <defs>, and the top-level element with id
// "glyph<gid>" (the spec's addressing), byte for byte. A document
// without that element at the top level returns whole.
func svgGlyphElement(doc []byte, gid font.GID) []byte {
	want := fmt.Sprintf("glyph%d", gid)
	d := xml.NewDecoder(bytes.NewReader(doc))
	var out bytes.Buffer
	depth, found := 0, false
	var keepFrom int64 = -1
	for {
		start := d.InputOffset()
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch depth {
			case 1:
				out.Write(doc[:d.InputOffset()]) // through the root tag
			case 2:
				id := ""
				for _, a := range t.Attr {
					if a.Name.Local == "id" {
						id = a.Value
					}
				}
				if t.Name.Local == "defs" || id == want {
					keepFrom = start
					found = found || id == want
				}
			}
		case xml.EndElement:
			if depth == 2 && keepFrom >= 0 {
				out.Write(doc[keepFrom:d.InputOffset()])
				keepFrom = -1
			}
			depth--
		}
	}
	if !found {
		return doc
	}
	out.WriteString("</svg>")
	return out.Bytes()
}

// hexColor is c (premultiplied) as an opaque-or-alpha SVG hex color.
func hexColor(c Color) string {
	a := c.A()
	if a == 0 {
		return "#00000000"
	}
	un := func(v uint8) uint8 { return uint8(min(255, (int(v)*255+int(a)/2)/int(a))) }
	return fmt.Sprintf("#%02x%02x%02x%02x", un(c.R()), un(c.G()), un(c.B()), a)
}
