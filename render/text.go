package render

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/segmenter"
	"github.com/go-text/typesetting/shaping"
	"golang.org/x/image/math/fixed"

	"github.com/stubbedev/gelm/internal/text"
)

// Font shapes and paints text: a single Typeface, or a Chain that
// falls back across faces for runes the primary face lacks. Widgets
// take a Font, so either works at every text call site.
type Font interface {
	// Shape lays text out at the given pixel size, resolving the base
	// paragraph direction from the text's first strong character.
	Shape(s string, px float64) *ShapedText
	// ShapeDir is Shape under an explicit base direction; see
	// text.Direction.
	ShapeDir(s string, px float64, d text.Direction) *ShapedText
	// ShapeRune shapes one rune at the pixel size - the per-rune probe
	// wrap measurement and advance math use. Rune-keyed in the caches,
	// so a probe never builds a one-rune string per call.
	ShapeRune(r rune, px float64) *ShapedText
	// Draw paints a shaped line with its baseline at logical
	// (x, baselineY).
	Draw(cv *Canvas, s *ShapedText, x, baselineY int, col Color)
	// DrawAligned draws text inside box with the given alignment; start
	// and end mirror for a right-to-left paragraph.
	DrawAligned(cv *Canvas, s string, box Rect, px float64, col Color, h Alignment) *ShapedText
	// DrawAlignedDir is DrawAligned under an explicit base direction.
	DrawAlignedDir(cv *Canvas, s string, box Rect, px float64, col Color, h Alignment, d text.Direction) *ShapedText
}

var (
	_ Font = (*Typeface)(nil)
	_ Font = (*Chain)(nil)
)

// Typeface is a loaded font plus the machinery to shape and rasterize it.
// One instance per font family; it is not safe for concurrent use.
type Typeface struct {
	face   *font.Face
	upem   float64
	shaper shaping.HarfbuzzShaper

	// features shape with OpenType features on (the tnum twin a
	// stylesheet's font-feature-settings asks for); nil shapes plain.
	features []shaping.FontFeature
	// tabular memoizes this face's tnum twin, so the variant keys its
	// own shaping cache entries instead of missing every style pass.
	tabular *Typeface

	// strikePpem is the pixels-per-em of the largest embedded bitmap
	// strike (0 when the face has none), and bitmaps caches decoded
	// bitmap glyphs. Both fill lazily; color emoji faces hit them on
	// every draw.
	strikePpem float64
	strikeRead bool
	bitmaps    map[font.GID]bitmapGlyph
}

// Tabular returns the face shaping with the tnum OpenType feature on:
// digits on a uniform advance grid, what a clock or a numeric column
// wants. Memoized; the twin shares nothing the lazy caches fill, so
// the two entries never contend.
func (t *Typeface) Tabular() *Typeface {
	if t.tabular == nil {
		c := *t
		c.features = []shaping.FontFeature{{Tag: ot.MustNewTag("tnum"), Value: 1}}
		c.shaper = shaping.HarfbuzzShaper{}
		c.tabular = nil
		c.strikePpem, c.strikeRead, c.bitmaps = 0, false, nil
		t.tabular = &c
	}
	return t.tabular
}

// LoadFont parses font data (TTF or OTF).
func LoadFont(data []byte) (*Typeface, error) {
	face, err := font.ParseTTF(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("render: parse font: %w", err)
	}
	return &Typeface{face: face, upem: float64(face.Upem())}, nil
}

// NewTypeface wraps an already-resolved face, as returned by a font
// matcher.
func NewTypeface(face *font.Face) (*Typeface, error) {
	if face == nil {
		return nil, errors.New("render: nil face")
	}
	return &Typeface{face: face, upem: float64(face.Upem())}, nil
}

// Family returns the resolved face's family name.
func (t *Typeface) Family() string {
	return t.face.Describe().Family
}

// Describe returns the face's font description: family, style, weight,
// and stretch. Callers compare it to pin which variant a request
// resolved to.
func (t *Typeface) Describe() font.Description {
	return t.face.Describe()
}

// IsMonospace reports whether the face's glyphs are fixed-width (its
// post table's flag), what a monospace filter asks.
func (t *Typeface) IsMonospace() bool { return t.face.IsMonospace() }

// Covers reports whether the face has a glyph for r. Fallback chains
// consult it per rune to decide where a glyph comes from.
func (t *Typeface) Covers(r rune) bool {
	_, ok := t.face.NominalGlyph(r)
	return ok
}

// ShapedText is one line of text shaped at a fixed pixel size: a
// sequence of runs, each shaped with its own face and embedding
// direction, sharing one baseline, laid out in visual left-to-right
// order (the directional runs internal/text resolves). Glyph positions
// are relative to the line origin. A single face and direction produce
// a single run; fallback chains produce one run per face change.
//
// ShapedText is immutable once built - the process-wide shaping cache
// shares one instance between every caller that shapes the same
// (font, size, string) - so readers may hold and consult it freely but
// never write it.
type ShapedText struct {
	runs []shapedRun
	text string
	px   float64
	// carets is the per-rune-boundary x table carets() serves, built
	// once at shape time so repeated paint passes never rebuild it.
	carets []float64
}

// shapedRun is one face's shaped slice of the line: the runes of
// Text on [start, end), positioned after the preceding runs'
// advances. rtl marks a right-to-left embedding: its glyphs come back
// in visual order and its caret slots map one up.
type shapedRun struct {
	face       *Typeface
	out        shaping.Output
	start, end int // rune offsets of the run within Text
	rtl        bool
}

// Shape lays text out at the given pixel size, resolving the base
// direction from the text's first strong character. Results are served
// from the process-wide shaping cache: the second Shape of the same
// (face, size, string) is a map hit returning the identical ShapedText,
// so per-frame re-shapes - Entry's selection band, text, and caret, or
// a per-event click mapping - cost a lookup.
func (t *Typeface) Shape(s string, px float64) *ShapedText {
	return t.ShapeDir(s, px, text.DirectionAuto)
}

// ShapeDir lays text out under base direction d; see Shape and
// text.Direction.
func (t *Typeface) ShapeDir(s string, px float64, d text.Direction) *ShapedText {
	return cachedShape(t, px, s, d, func() *ShapedText { return t.shapeUncached(s, px, d) })
}

// ShapeRune shapes one rune; see Font.ShapeRune. Identical result to
// Shape(string(r)) - the fill goes through the same uncached path -
// but the cache keys on the rune, so per-rune probes (wrap measurement
// walks one rune at a time) stay allocation-free in the steady state.
func (t *Typeface) ShapeRune(r rune, px float64) *ShapedText {
	return cachedShapeRune(t, px, r, func() *ShapedText {
		return t.shapeUncached(string(r), px, text.DirectionAuto)
	})
}

// shapeUncached does the actual shaping work, bypassing the cache.
func (t *Typeface) shapeUncached(s string, px float64, d text.Direction) *ShapedText {
	if s == "" {
		return newShapedText(s, px, []shapedRun{t.shapeRun(s, px, 0, false)})
	}
	var runs []shapedRun
	bidiPieces(s, d, func(piece string, start int, rtl bool) {
		runs = append(runs, t.shapeRun(piece, px, start, rtl))
	})
	return newShapedText(s, px, runs)
}

// bidiPieces walks s's directional runs - internal/text.BidiRuns, the
// one resolution every text path shares - in visual order, yielding
// each as a piece with its rune start and direction for the shaper to
// consume. Runs never split a grapheme cluster, so piece boundaries
// are caret boundaries too.
func bidiPieces(s string, d text.Direction, yield func(piece string, start int, rtl bool)) {
	runs := text.BidiRuns(s, d)
	rs := []rune(s)
	for _, r := range runs {
		yield(string(rs[r.Start:r.End]), r.Start, r.RTL)
	}
}

// newShapedText builds an immutable ShapedText, caret table included.
func newShapedText(text string, px float64, runs []shapedRun) *ShapedText {
	s := &ShapedText{text: text, px: px, runs: runs}
	s.carets = s.buildCarets()
	return s
}

// shapeRun shapes text as one run positioned at rune indexes
// [start, end) of the line, shaped right to left when rtl.
func (t *Typeface) shapeRun(text string, px float64, start int, rtl bool) shapedRun {
	runes := []rune(text)
	dir := di.DirectionLTR
	if rtl {
		dir = di.DirectionRTL
	}
	run := t.shaper.Shape(shaping.Input{
		Text:         runes,
		RunStart:     0,
		RunEnd:       len(runes),
		Direction:    dir,
		Face:         t.face,
		Size:         f266(px),
		FontFeatures: t.features,
	})
	return shapedRun{face: t, out: run, start: start, end: start + len(runes), rtl: rtl}
}

// Runs returns how many faces the line was shaped across. A single
// typeface always returns 1.
func (s *ShapedText) Runs() int { return len(s.runs) }

// NotDefCount returns how many shaped glyphs are the .notdef box
// (glyph 0), i.e. runes no face in the chain could render. Fallback
// chains aim for zero on scripts the system covers.
func (s *ShapedText) NotDefCount() int {
	n := 0
	for i := range s.runs {
		for j := range s.runs[i].out.Glyphs {
			if s.runs[i].out.Glyphs[j].GlyphID == 0 {
				n++
			}
		}
	}
	return n
}

// Advance returns the line width in pixels: the sum over the runs.
func (s *ShapedText) Advance() float64 {
	var adv float64
	for i := range s.runs {
		adv += f64(s.runs[i].out.Advance)
	}
	return adv
}

// Ascent returns the line's ascent above the baseline in pixels: the
// maximum over the runs, so a taller fallback face grows the line.
func (s *ShapedText) Ascent() float64 {
	asc := 0.0
	for i := range s.runs {
		asc = math.Max(asc, f64(s.runs[i].out.LineBounds.Ascent))
	}
	return asc
}

// Descent returns the line's descent below the baseline in pixels
// (positive): the maximum over the runs.
func (s *ShapedText) Descent() float64 {
	desc := 0.0
	for i := range s.runs {
		desc = math.Max(desc, -f64(s.runs[i].out.LineBounds.Descent))
	}
	return desc
}

// LineHeight returns the rounded line height: the smallest integer box
// height that fits the line's ascent and descent. Measure and drawing
// guards must both use this so a label's natural height is never smaller
// than the height its own painter requires.
func (s *ShapedText) LineHeight() int {
	return int(math.Ceil(s.Ascent() + s.Descent()))
}

// buildCarets computes the caret x position for every rune boundary
// 0..n of the line. The x values follow the line visually, not
// logically: boundaries inside a shaping cluster (a base rune plus its
// combining marks) snap to the cluster start, so a caret can never
// land inside a grapheme, and an RTL run maps its cluster edges one
// slot up (the caret before rune c sits at the run's right edge) while
// run junctions keep the position the two runs share, the way Pango
// places them. Runs continue each other's x, like the runs of a rich
// label on one shared baseline. Built once at shape time - the table
// is shared with every reader of the cached ShapedText, which never
// writes it.
func (s *ShapedText) buildCarets() []float64 {
	n := utf8.RuneCountInString(s.text)
	xs := make([]float64, n+1)
	for i := range xs {
		xs[i] = -1
	}
	if n == 0 {
		xs[0] = 0
		return xs
	}
	set := func(slot int, v float64) {
		if slot >= 0 && slot <= n && xs[slot] < 0 {
			xs[slot] = v
		}
	}
	x := 0.0 // origin of the current run
	for i := range s.runs {
		r := &s.runs[i]
		gx := 0.0 // pen within the run
		prev := -1
		for j := range r.out.Glyphs {
			g := &r.out.Glyphs[j]
			ti := r.start + g.TextIndex()
			if ti != prev {
				set(ti+r.rtlSlot(), x+gx)
			}
			prev = ti
			gx += f64(g.Advance)
		}
		if r.rtl {
			set(r.start, x+f64(r.out.Advance)) // before the first rune: the run's right edge
		} else {
			set(r.end, x+f64(r.out.Advance))
		}
		x += f64(r.out.Advance)
	}
	last := 0.0
	for i := range xs {
		if xs[i] < 0 {
			xs[i] = last
		} else {
			last = xs[i]
		}
	}
	return xs
}

// CaretPositions returns the caret x for every rune boundary 0..n at
// once - the table CaretX indexes, shared with every other reader of
// this line: read it, never modify it. Callers composing several runs
// into one line (rich labels) offset each run's table by its line x.
func (s *ShapedText) CaretPositions() []float64 {
	return s.carets
}

// CaretX returns the x offset of the caret placed before rune index
// caret, clamped to [0, rune count]. The x follows the line visually:
// before an RTL run's first rune it sits at that run's right edge.
func (s *ShapedText) CaretX(caret int) float64 {
	xs := s.carets
	if caret < 0 {
		caret = 0
	}
	if caret > len(xs)-1 {
		caret = len(xs) - 1
	}
	return xs[caret]
}

// CaretAt returns the rune boundary nearest x: the inverse of CaretX for
// hit-testing clicks in a text field, direction-agnostic because the
// table already follows the line visually.
func (s *ShapedText) CaretAt(x float64) int {
	xs := s.carets
	best := 0
	bestD := math.Abs(x - xs[0])
	for i := 1; i < len(xs); i++ {
		if d := math.Abs(x - xs[i]); d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// rtlSlot maps a run's glyph cluster onto its caret slot: an RTL run's
// edges shift one up, because the caret before rune c sits at that
// run's right edge.
func (r *shapedRun) rtlSlot() int {
	if r.rtl {
		return 1
	}
	return 0
}

// AppendCaretBands appends the x spans covering the selected rune range
// [start, end) to buf and returns it: one span per visual run the range
// touches, left to right, so a selection across mixed-direction text
// highlights disjoint spans instead of spanning the middle. Spans are
// line-relative; callers offset them. The buffer is the caller's, kept
// across frames, so the render path stays allocation-free in the
// steady state.
func (s *ShapedText) AppendCaretBands(buf [][2]float64, start, end int) [][2]float64 {
	start = min(max(start, 0), len(s.carets)-1)
	end = min(max(end, start), len(s.carets)-1)
	if start >= end {
		return buf
	}
	x := 0.0 // origin of the current run
	for i := range s.runs {
		r := &s.runs[i]
		adv := f64(r.out.Advance)
		lo, hi := max(start, r.start), min(end, r.end)
		if lo < hi {
			var x0, x1 float64
			if r.rtl {
				// The caret table's junction convention collapses an RTL
				// run's outer edges onto one x; the band reads the run's
				// own geometry instead — visually from the left edge of
				// the last selected rune to the right edge of the first.
				x0 = s.carets[hi]
				x1 = x + adv
				if lo > r.start {
					x1 = s.carets[lo]
				}
			} else {
				x0, x1 = s.carets[lo], s.carets[hi]
			}
			if x1 < x0 {
				x0, x1 = x1, x0
			}
			if x1 > x0 {
				buf = append(buf, [2]float64{x0, x1})
			}
		}
		x += adv
	}
	return buf
}

// Text returns the string the run was shaped from.
func (s *ShapedText) Text() string { return s.text }

// Draw paints the line with its baseline at logical (x, baselineY),
// clipped to the canvas like every other primitive. Each run draws
// with its own face; every run was shaped at the logical pixel size
// and rasterizes at the canvas's device scale, so text stays crisp at
// any factor no matter which face supplied the glyph.
func (s *ShapedText) Draw(cv *Canvas, x, baselineY int, col Color) {
	clip := cv.clip
	if clip.Empty() {
		return
	}
	dev := float64(cv.num) / float64(cv.denom)
	dotX := float64(x) * dev
	base := float64(baselineY) * dev
	for i := range s.runs {
		r := &s.runs[i]
		scale := s.px * dev / r.face.upem
		penX := dotX
		// The shaped positions are logical pixels and the outlines
		// carry their own side bearing: the pen walks the advances at
		// the device scale and each glyph draws at its origin, its
		// offset y-up (a mark below the base sits below the baseline).
		for j := range r.out.Glyphs {
			g := &r.out.Glyphs[j]
			drawGlyph(cv, clip, r.face, g.GlyphID, scale,
				penX+f64(g.XOffset)*dev,
				base-f64(g.YOffset)*dev, col)
			penX += f64(g.Advance) * dev
		}
		dotX += f64(r.out.Advance) * dev
	}
}

// Draw paints a shaped line; see ShapedText.Draw.
func (t *Typeface) Draw(cv *Canvas, s *ShapedText, x, baselineY int, col Color) {
	s.Draw(cv, x, baselineY, col)
}

// drawGlyph paints one glyph with its baseline pen at (penX, penY)
// into the clip: the cached 8bpp coverage raster when the face has an
// outline, the cached scaled bitmap strike (color emoji) otherwise.
func drawGlyph(cv *Canvas, clip Rect, t *Typeface, gid font.GID, scale, penX, penY float64, col Color) {
	g, ok := cachedOutlineRaster(t, gid, scale, penX, penY)
	if ok {
		// The mask sits at the integer pen plus the raster's own
		// origin offset (its bounds relative to that pen).
		blitMask(cv, clip, g,
			int(math.Floor(penX))+g.ox,
			int(math.Floor(penY))+g.oy, col)
		return
	}
	drawBitmapGlyph(cv, clip, t, gid, scale, penX, penY, col)
}

// blitMask tints one 8bpp coverage raster with col and blends it at
// device (ox, oy), the atlas's blit half. Coverage ramps the
// premultiplied channels exactly as the per-draw rasterizer always
// did, and every write goes through cv.blend, so the PushAlpha stack
// and source-over discipline hold by construction.
func blitMask(cv *Canvas, clip Rect, g *glyphRaster, ox, oy int, col Color) {
	x0, y0 := max(ox, clip.X), max(oy, clip.Y)
	x1, y1 := min(ox+g.w, clip.X+clip.W), min(oy+g.h, clip.Y+clip.H)
	ar, ag, ab := uint32(col.A()), uint32(col.R()), uint32(col.G())
	abB := uint32(col.B())
	for y := y0; y < y1; y++ {
		row := (y - oy) * g.w
		for x := x0; x < x1; x++ {
			m := uint32(g.mask[row+x-ox])
			if m == 0 {
				continue
			}
			src := Color((ar*m/255)<<24 |
				(ag*m/255)<<16 |
				(ab*m/255)<<8 |
				(abB*m)/255)
			cv.blend(x, y, src)
		}
	}
}

// bitmapGlyph is a decoded embedded bitmap. mono marks a
// black-and-white strike: a mask that tints with the text color.
// Color bitmaps (emoji) paint their own pixels.
type bitmapGlyph struct {
	img  image.Image
	mono bool
}

// drawBitmapGlyph blits an embedded bitmap glyph - a CBDT/sbix color
// emoji, or a black-and-white strike tinted with col - with its origin
// box at the baseline pen. The scaled strike comes from the atlas (the
// CatmullRom resample is the expensive half; see cachedScaledBitmap),
// preserving the device-scale rasterization contract; PNG and
// black-and-white formats decode, others skip.
func drawBitmapGlyph(cv *Canvas, clip Rect, t *Typeface, gid font.GID, scale, penX, penY float64, col Color) {
	bg, ok := t.bitmap(gid)
	if !ok {
		return
	}
	ppem := t.bitmapPpem()
	if ppem <= 0 {
		return
	}
	// Device pixels per bitmap pixel: the strike renders one em at
	// ppem, the glyph's device em is scale*upem.
	f := scale * t.upem / ppem
	w := int(math.Ceil(float64(bg.img.Bounds().Dx()) * f))
	h := int(math.Ceil(float64(bg.img.Bounds().Dy()) * f))
	if w <= 0 || h <= 0 {
		return
	}
	scaled := cachedScaledBitmap(t, gid, bg, w, h)
	// Bitmaps sit on the baseline, left edge at the pen.
	x0, y0 := int(math.Floor(penX)), int(math.Ceil(penY))-h
	for y := range h {
		for x := range w {
			c := scaled.RGBAAt(x, y)
			if c.A == 0 {
				continue
			}
			px, py := x0+x, y0+y
			if px < clip.X || px >= clip.X+clip.W || py < clip.Y || py >= clip.Y+clip.H {
				continue
			}
			var src Color
			if bg.mono {
				m := uint32(c.A)
				src = Color((uint32(col.A())*m/255)<<24 |
					(uint32(col.R())*m/255)<<16 |
					(uint32(col.G())*m/255)<<8 |
					(uint32(col.B())*m)/255)
			} else {
				src = Color(uint32(c.A)<<24 | uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B))
			}
			cv.blend(px, py, src)
		}
	}
}

// bitmap returns the decoded embedded bitmap for gid; ok is false when
// the face has none in a supported format. Decodes cache per glyph.
func (t *Typeface) bitmap(gid font.GID) (bitmapGlyph, bool) {
	if t.bitmaps == nil {
		t.bitmaps = make(map[font.GID]bitmapGlyph)
	}
	if bg, ok := t.bitmaps[gid]; ok {
		return bg, bg.img != nil
	}
	bg := decodeBitmap(t.face, gid)
	t.bitmaps[gid] = bg
	return bg, bg.img != nil
}

// decodeBitmap extracts the bitmap glyph data for gid.
func decodeBitmap(f *font.Face, gid font.GID) bitmapGlyph {
	bm, ok := f.GlyphDataBitmap(gid)
	if !ok {
		return bitmapGlyph{}
	}
	switch bm.Format {
	case font.PNG:
		img, err := png.Decode(bytes.NewReader(bm.Data))
		if err != nil {
			return bitmapGlyph{}
		}
		return bitmapGlyph{img: img}
	case font.BlackAndWhite, font.BlackAndWhiteByteAligned:
		img := image.NewAlpha(image.Rect(0, 0, bm.Width, bm.Height))
		stride := 8 // bits per byte; byte-aligned rows pad per row
		if bm.Format == font.BlackAndWhite {
			stride = ((bm.Width + 7) / 8) * 8
		}
		for y := range bm.Height {
			for x := range bm.Width {
				bit := y*stride + x
				if bm.Data[bit/8]&(1<<(7-uint(bit%8))) != 0 {
					img.SetAlpha(x, y, color.Alpha{A: 255})
				}
			}
		}
		return bitmapGlyph{img: img, mono: true}
	}
	return bitmapGlyph{}
}

// bitmapPpem returns the pixels-per-em of the largest embedded bitmap
// strike - the one the glyph loader selects when no resolution is
// requested - or 0 when the face has no strikes.
func (t *Typeface) bitmapPpem() float64 {
	if !t.strikeRead {
		t.strikeRead = true
		for _, s := range t.face.BitmapSizes() {
			t.strikePpem = math.Max(t.strikePpem, float64(s.XPpem))
		}
	}
	return t.strikePpem
}

// Wrap breaks text into lines of at most maxWidth pixels, filling
// greedily along Unicode UAX #14 break opportunities: each line takes
// as much as fits before the first opportunity that overflows. Breaks
// land where real typesetting puts them - at spaces, after hyphens,
// inside CJK runs - not only at ASCII spaces. A run with no break
// opportunity before the width, an unbreakable token, stays whole on
// its own line and overflows. Hard newlines split unconditionally;
// trailing spaces at a break and leading spaces of a continuation are
// dropped.
func (t *Typeface) Wrap(text string, maxWidth, px float64) []string {
	return WrapText(t, text, maxWidth, px)
}

// EllipsizeMode selects which end of an overflowing line the ellipsis
// replaces.
type EllipsizeMode uint8

const (
	// EllipsizeNone never truncates: long text overflows and clips.
	EllipsizeNone EllipsizeMode = iota
	// EllipsizeStart cuts from the front: "…cated text".
	EllipsizeStart
	// EllipsizeMiddle keeps the head and the tail: "trunc…text" - the
	// mode for paths and filenames, whose both halves identify them.
	EllipsizeMiddle
	// EllipsizeEnd cuts from the back: "truncated te…".
	EllipsizeEnd
)

// Ellipsis is the mark a truncated text carries where it was cut.
const Ellipsis = "…"

// ellipsizeText is the shaper-agnostic body of Ellipsize and
// EllipsizeText. Fitting text is returned unchanged; otherwise a binary
// search over the kept-rune count finds the longest truncation whose
// advance still fits maxWidth - the kept prefix and suffix only ever
// grow with the count, so the search is monotone. EllipsizeNone cuts
// from the end, the common default, so a stray None never silently
// overflows.
func ellipsizeText(s textShaper, text string, mode EllipsizeMode, maxWidth, px float64) string {
	if s.Shape(text, px).Advance() <= maxWidth {
		return text
	}
	const ell = Ellipsis
	runes := []rune(text)
	fits := func(cand string) bool { return s.Shape(cand, px).Advance() <= maxWidth }
	// longest returns the largest keep whose built candidate fits.
	longest := func(build func(keep int) string) int {
		lo, hi := 0, len(runes)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if fits(build(mid)) {
				lo = mid
				continue
			}
			hi = mid - 1
		}
		return lo
	}
	switch mode {
	case EllipsizeStart:
		keep := longest(func(k int) string { return ell + string(runes[len(runes)-k:]) })
		return ell + string(runes[len(runes)-keep:])
	case EllipsizeMiddle:
		keep := longest(func(k int) string {
			head := k - k/2 // the head keeps the odd rune
			return string(runes[:head]) + ell + string(runes[len(runes)-(k-head):])
		})
		head := keep - keep/2
		return string(runes[:head]) + ell + string(runes[len(runes)-(keep-head):])
	default: // EllipsizeEnd, and EllipsizeNone as the safe reading
		keep := longest(func(k int) string { return string(runes[:k]) + ell })
		return string(runes[:keep]) + ell
	}
}

// EllipsizeText shortens text so its advance fits maxWidth, cutting at
// mode and inserting an ellipsis; see Typeface.Ellipsize. Package-level
// so any Font - a lone typeface or a fallback chain - truncates through
// the same code path the methods use.
func EllipsizeText(f Font, text string, mode EllipsizeMode, maxWidth, px float64) string {
	return ellipsizeText(f, text, mode, maxWidth, px)
}

// WrapText breaks text into lines of at most maxWidth pixels; see Wrap.
// Package-level so any Font wraps through the same code path.
func WrapText(f Font, text string, maxWidth, px float64) []string {
	return wrapLines(f, text, maxWidth, px)
}

// Ellipsize shortens text to fit maxWidth, replacing the cut remainder
// with an ellipsis. Text that already fits is returned unchanged. For
// the other cut points use EllipsizeText.
func (t *Typeface) Ellipsize(text string, maxWidth, px float64) string {
	return EllipsizeText(t, text, EllipsizeEnd, maxWidth, px)
}

// Alignment selects how a drawn run is positioned inside its box.
type Alignment uint8

// Text alignments: start (left for LTR lines), center, end.
const (
	AlignStart Alignment = iota
	AlignCenter
	AlignEnd
)

// textShaper is the shaping half of Font; the shared DrawAligned and
// line-breaking helpers work for any shaper, single face or chain.
type textShaper interface {
	Shape(s string, px float64) *ShapedText
	ShapeDir(s string, px float64, d text.Direction) *ShapedText
}

// drawAligned is the shared body of Typeface.DrawAligned and
// Chain.DrawAligned: skip when the box cannot hold one line of the
// font, position by the alignment mirrored for the paragraph's
// resolved base direction (start hugs the right edge when it resolves
// right to left), draw on the rounded line box.
func drawAligned(cv *Canvas, sh textShaper, s string, box Rect, px float64, col Color, h Alignment, d text.Direction) *ShapedText {
	st := sh.ShapeDir(s, px, d)
	lineH := float64(st.LineHeight())
	if float64(box.H) < lineH {
		return nil
	}
	if text.RTL(s, d) {
		switch h {
		case AlignStart:
			h = AlignEnd
		case AlignEnd:
			h = AlignStart
		}
	}
	var x float64
	switch h {
	case AlignCenter:
		x = float64(box.X) + (float64(box.W)-st.Advance())/2
	case AlignEnd:
		x = float64(box.X) + float64(box.W) - st.Advance()
	default:
		x = float64(box.X)
	}
	baseline := box.Y + int(math.Round((float64(box.H)-lineH)/2+st.Ascent()))
	st.Draw(cv, int(math.Round(x)), baseline, col)
	return st
}

// DrawAligned draws text inside box with the given alignment, skipping it
// entirely when the box cannot hold one line of the font. The guard uses
// the same rounded LineHeight the measurement reports.
func (t *Typeface) DrawAligned(cv *Canvas, s string, box Rect, px float64, col Color, h Alignment) *ShapedText {
	return drawAligned(cv, t, s, box, px, col, h, text.DirectionAuto)
}

// DrawAlignedDir draws text inside box under base direction d; start
// and end mirror for a right-to-left base. See text.Direction.
func (t *Typeface) DrawAlignedDir(cv *Canvas, s string, box Rect, px float64, col Color, h Alignment, d text.Direction) *ShapedText {
	return drawAligned(cv, t, s, box, px, col, h, d)
}

// Chain shapes mixed-script text across faces: every rune goes to the
// first face that covers it, so a line mixing latin, CJK, and emoji
// renders instead of drawing .notdef boxes. The picks cache per rune,
// so repeated shapes pay one lookup per distinct rune. Like Typeface,
// a chain is not safe for concurrent use.
type Chain struct {
	primary *Typeface
	extra   []*Typeface
	// resolve, when set, is consulted for runes neither primary nor
	// the fallback list covers; returning nil keeps the rune on the
	// primary face, rendering .notdef as before.
	resolve func(rune) *Typeface
	picks   map[rune]*Typeface
	// tabular shapes every run through its face's tnum twin.
	tabular bool
}

// Primary is the chain's first face: the one variants resolve from.
func (c *Chain) Primary() *Typeface { return c.primary }

// NewChain returns a chain shaping with primary and, for runes it
// lacks, the first covering face of fallback. A nil primary panics
// here, naming the argument — the same nil-face contract the widget
// constructors enforce — instead of failing on the first Shape.
func NewChain(primary *Typeface, fallback ...*Typeface) *Chain {
	if primary == nil {
		panic("render.NewChain: nil primary")
	}
	return &Chain{primary: primary, extra: fallback, picks: map[rune]*Typeface{}}
}

// WithResolver installs a store-backed face picker consulted for runes
// neither the primary nor the fallback list covers, and returns the
// chain. internal/sysfont.Fallback wires one to the system font store.
func (c *Chain) WithResolver(resolve func(rune) *Typeface) *Chain {
	c.resolve = resolve
	return c
}

// Face returns the face rune r shapes with: the diagnostic view of
// the fallback decision, and a way for tests to pin which family a
// rune lands on.
func (c *Chain) Face(r rune) *Typeface { return c.faceFor(r) }

// Tabular returns the chain shaping with each face's tnum twin: digits
// on a uniform advance grid, what a clock or a numeric column wants.
// The variant keeps its own rune picks, so it never rewrites the base
// chain's.
func (c *Chain) Tabular() *Chain {
	n := *c
	n.picks = map[rune]*Typeface{}
	n.tabular = true
	return &n
}

// faceFor returns the face rune r shapes with.
func (c *Chain) faceFor(r rune) *Typeface {
	if f, ok := c.picks[r]; ok {
		return f
	}
	f := c.primary
	if !f.Covers(r) {
		for _, e := range c.extra {
			if e.Covers(r) {
				f = e
				break
			}
		}
		if f == c.primary && c.resolve != nil {
			if rf := c.resolve(r); rf != nil {
				f = rf
			}
		}
	}
	if c.tabular {
		f = f.Tabular()
	}
	c.picks[r] = f
	return f
}

// Shape splits text into runs of consecutive runes sharing a face and
// shapes each with it, on one shared baseline, serving repeats from the
// process-wide shaping cache like Typeface.Shape. Within that, the
// line resolves into directional runs (internal/text.BidiRuns) laid
// out in visual order, so mixed Hebrew/Arabic + Latin lines render
// reading-correct. The chain is the key:
// a chain held by the app reuses its entries across frames, and two
// chains over the same faces never collide. Empty text still shapes one
// primary run, so its metrics reserve the font's line height.
func (c *Chain) Shape(s string, px float64) *ShapedText {
	return c.ShapeDir(s, px, text.DirectionAuto)
}

// ShapeDir shapes under base direction d; see Chain.Shape and
// text.Direction.
func (c *Chain) ShapeDir(s string, px float64, d text.Direction) *ShapedText {
	return cachedShape(c, px, s, d, func() *ShapedText { return c.shapeUncached(s, px, d) })
}

// ShapeRune shapes one rune; see Font.ShapeRune and Chain.Shape.
func (c *Chain) ShapeRune(r rune, px float64) *ShapedText {
	return cachedShapeRune(c, px, r, func() *ShapedText {
		return c.shapeUncached(string(r), px, text.DirectionAuto)
	})
}

// shapeUncached does the run-splitting and shaping work, bypassing the
// cache: each directional run splits per covering face, shaped with its
// own embedding direction.
func (c *Chain) shapeUncached(s string, px float64, d text.Direction) *ShapedText {
	if s == "" {
		return newShapedText(s, px, []shapedRun{c.primary.shapeRun(s, px, 0, false)})
	}
	var runs []shapedRun
	bidiPieces(s, d, func(piece string, start int, rtl bool) {
		runes := []rune(piece)
		for i := 0; i < len(runes); {
			f := c.faceFor(runes[i])
			j := i + 1
			for j < len(runes) && c.faceFor(runes[j]) == f {
				j++
			}
			runs = append(runs, f.shapeRun(string(runes[i:j]), px, start+i, rtl))
			i = j
		}
	})
	return newShapedText(s, px, runs)
}

// Draw paints a shaped line; see ShapedText.Draw.
func (c *Chain) Draw(cv *Canvas, s *ShapedText, x, baselineY int, col Color) {
	s.Draw(cv, x, baselineY, col)
}

// DrawAligned draws text inside box with the given alignment; see
// Typeface.DrawAligned.
func (c *Chain) DrawAligned(cv *Canvas, s string, box Rect, px float64, col Color, h Alignment) *ShapedText {
	return drawAligned(cv, c, s, box, px, col, h, text.DirectionAuto)
}

// DrawAlignedDir draws text inside box under base direction d; start
// and end mirror for a right-to-left base. See text.Direction.
func (c *Chain) DrawAlignedDir(cv *Canvas, s string, box Rect, px float64, col Color, h Alignment, d text.Direction) *ShapedText {
	return drawAligned(cv, c, s, box, px, col, h, d)
}

// Wrap breaks text into lines of at most maxWidth pixels; see
// Typeface.Wrap.
func (c *Chain) Wrap(text string, maxWidth, px float64) []string {
	return wrapLines(c, text, maxWidth, px)
}

// Ellipsize shortens text to fit maxWidth; see Typeface.Ellipsize.
func (c *Chain) Ellipsize(text string, maxWidth, px float64) string {
	return EllipsizeText(c, text, EllipsizeEnd, maxWidth, px)
}

// wrapLines is the shaper-agnostic body of Wrap: hard newlines split
// unconditionally, and each hard line fills greedily along its
// UAX #14 break opportunities (see wrapSoft).
func wrapLines(s textShaper, text string, maxWidth, px float64) []string {
	if text == "" {
		return []string{""}
	}
	var lines []string
	for hard := range strings.SplitSeq(text, "\n") {
		lines = append(lines, wrapSoft(s, strings.TrimSuffix(hard, "\r"), maxWidth, px)...)
	}
	return lines
}

// wrapSoft greedily fills one hard line - no newlines - along the
// UAX #14 break opportunities the typesetting segmenter computes. Each
// opportunity ends a candidate line; the first one that overflows
// closes the line before its segment, so a run with no opportunity
// before the width - an unbreakable token - is accepted whole and
// overflows rather than disappearing. Spaces ride at the end of the
// segment before a break, so closing a line trims them, and a
// continuation line never starts with the space that caused it.
func wrapSoft(s textShaper, line string, maxWidth, px float64) []string {
	runes := []rune(line)
	if len(runes) == 0 {
		return []string{""}
	}
	var seg segmenter.Segmenter
	if err := seg.InitWithString(line); err != nil {
		return []string{line} // invalid UTF-8: one overflowing line, never a failure
	}
	skipSpaces := func(from int) int {
		for from < len(runes) && runes[from] == ' ' {
			from++
		}
		return from
	}
	var lines []string
	start, cur := skipSpaces(0), 0
	for it := seg.LineIterator(); it.Next(); {
		l := it.Line()
		end := l.Offset + len(l.Text)
		if cur > start && s.Shape(string(runes[start:end]), px).Advance() > maxWidth {
			lines = append(lines, strings.TrimRight(string(runes[start:cur]), " "))
			start = skipSpaces(cur)
		}
		cur = end
	}
	if cur > start {
		lines = append(lines, strings.TrimRight(string(runes[start:cur]), " "))
		return lines
	}
	return append(lines, "") // whitespace only: an empty row, not a disappearance
}

func f266(px float64) fixed.Int26_6 {
	return fixed.Int26_6(int32(math.Round(px * 64)))
}

func f64(v fixed.Int26_6) float64 {
	return float64(int32(v)) / 64
}
