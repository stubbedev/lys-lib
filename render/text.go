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
	"github.com/go-text/typesetting/shaping"
	"golang.org/x/image/math/fixed"
)

// Font shapes and paints text: a single Typeface, or a Chain that
// falls back across faces for runes the primary face lacks. Widgets
// take a Font, so either works at every text call site.
type Font interface {
	// Shape lays text out at the given pixel size.
	Shape(text string, px float64) *ShapedText
	// Draw paints a shaped line with its baseline at logical
	// (x, baselineY).
	Draw(cv *Canvas, s *ShapedText, x, baselineY int, col Color)
	// DrawAligned draws text inside box with the given alignment.
	DrawAligned(cv *Canvas, text string, box Rect, px float64, col Color, h Alignment) *ShapedText
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

	// strikePpem is the pixels-per-em of the largest embedded bitmap
	// strike (0 when the face has none), and bitmaps caches decoded
	// bitmap glyphs. Both fill lazily; color emoji faces hit them on
	// every draw.
	strikePpem float64
	strikeRead bool
	bitmaps    map[font.GID]bitmapGlyph
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

// Covers reports whether the face has a glyph for r. Fallback chains
// consult it per rune to decide where a glyph comes from.
func (t *Typeface) Covers(r rune) bool {
	_, ok := t.face.NominalGlyph(r)
	return ok
}

// ShapedText is one line of text shaped at a fixed pixel size: a
// sequence of runs, each shaped with its own face, sharing one
// baseline. Glyph positions are relative to the line origin. A single
// face produces a single run; fallback chains produce one run per face
// change.
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

// shapedRun is one face's shaped slice of the line: the runes starting
// at rune index start of Text, positioned after the preceding runs'
// advances.
type shapedRun struct {
	face  *Typeface
	out   shaping.Output
	start int // rune offset of the run's first rune within Text
}

// Shape lays text out at the given pixel size. Results are served from
// the process-wide shaping cache: the second Shape of the same
// (face, size, string) is a map hit returning the identical ShapedText,
// so per-frame re-shapes - Entry's selection band, text, and caret, or
// a per-event click mapping - cost a lookup.
func (t *Typeface) Shape(text string, px float64) *ShapedText {
	return cachedShape(t, px, text, func() *ShapedText { return t.shapeUncached(text, px) })
}

// shapeUncached does the actual shaping work, bypassing the cache.
func (t *Typeface) shapeUncached(text string, px float64) *ShapedText {
	return newShapedText(text, px, []shapedRun{t.shapeRun(text, px, 0)})
}

// newShapedText builds an immutable ShapedText, caret table included.
func newShapedText(text string, px float64, runs []shapedRun) *ShapedText {
	s := &ShapedText{text: text, px: px, runs: runs}
	s.carets = s.buildCarets()
	return s
}

// shapeRun shapes text as one run positioned at rune index start of
// the line.
func (t *Typeface) shapeRun(text string, px float64, start int) shapedRun {
	runes := []rune(text)
	run := t.shaper.Shape(shaping.Input{
		Text:      runes,
		RunStart:  0,
		RunEnd:    len(runes),
		Direction: di.DirectionLTR,
		Face:      t.face,
		Size:      f266(px),
	})
	return shapedRun{face: t, out: run, start: start}
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
// 0..n of the line. The x values are monotone in LTR runs; boundaries
// inside a shaping cluster (a base rune plus its combining marks) snap
// to the cluster start, so a caret can never land inside a grapheme.
// Runs continue each other's x, like the runs of a rich label on one
// shared baseline. Built once at shape time - the table is shared with
// every reader of the cached ShapedText, which never writes it.
func (s *ShapedText) buildCarets() []float64 {
	n := utf8.RuneCountInString(s.text)
	xs := make([]float64, n+1)
	for i := range xs {
		xs[i] = -1
	}
	xs[0] = 0
	x := 0.0 // origin of the current run
	for i := range s.runs {
		r := &s.runs[i]
		gx := 0.0 // pen within the run
		prev := -1
		for j := range r.out.Glyphs {
			g := &r.out.Glyphs[j]
			ti := r.start + g.TextIndex()
			if ti != prev && ti <= n && xs[ti] < 0 {
				xs[ti] = x + gx
			}
			prev = ti
			gx += f64(g.Advance)
		}
		x += f64(r.out.Advance)
	}
	xs[n] = x
	last := xs[0]
	for i := 1; i < len(xs); i++ {
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
// caret, clamped to [0, rune count].
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
// hit-testing clicks in a text field.
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
		for j := range r.out.Glyphs {
			g := &r.out.Glyphs[j]
			drawGlyph(cv, clip, r.face, g.GlyphID, scale,
				penX+f64(g.XOffset)+f64(g.XBearing),
				base+f64(g.YOffset), col)
			penX += f64(g.Advance)
		}
		dotX += f64(r.out.Advance)
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

// Wrap breaks text into lines of at most maxWidth pixels, greedily, on
// spaces. A single word longer than maxWidth stays on its own line and
// overflows. Trailing spaces are dropped.
func (t *Typeface) Wrap(text string, maxWidth, px float64) []string {
	return wrapLines(t, text, maxWidth, px)
}

// ellipsizeText is the shaper-agnostic body of Ellipsize.
func ellipsizeText(s textShaper, text string, maxWidth, px float64) string {
	if s.Shape(text, px).Advance() <= maxWidth {
		return text
	}
	const ell = "…"
	runes := []rune(text)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		candidate := string(runes[:mid]) + ell
		if s.Shape(candidate, px).Advance() <= maxWidth {
			lo = mid
			continue
		}
		hi = mid - 1
	}
	return string(runes[:lo]) + ell
}

// Ellipsize shortens text to fit maxWidth, replacing the cut remainder
// with an ellipsis. Text that already fits is returned unchanged.
func (t *Typeface) Ellipsize(text string, maxWidth, px float64) string {
	return ellipsizeText(t, text, maxWidth, px)
}

// Alignment selects how a drawn run is positioned inside its box.
type Alignment uint8

const (
	AlignStart Alignment = iota
	AlignCenter
	AlignEnd
)

// textShaper is the shaping half of Font; the shared DrawAligned and
// line-breaking helpers work for any shaper, single face or chain.
type textShaper interface {
	Shape(text string, px float64) *ShapedText
}

// drawAligned is the shared body of Typeface.DrawAligned and
// Chain.DrawAligned: skip when the box cannot hold one line of the
// font, position by the alignment, draw on the rounded line box.
func drawAligned(cv *Canvas, s textShaper, text string, box Rect, px float64, col Color, h Alignment) *ShapedText {
	sh := s.Shape(text, px)
	lineH := float64(sh.LineHeight())
	if float64(box.H) < lineH {
		return nil
	}
	var x float64
	switch h {
	case AlignCenter:
		x = float64(box.X) + (float64(box.W)-sh.Advance())/2
	case AlignEnd:
		x = float64(box.X) + float64(box.W) - sh.Advance()
	default:
		x = float64(box.X)
	}
	baseline := box.Y + int(math.Round((float64(box.H)-lineH)/2+sh.Ascent()))
	sh.Draw(cv, int(math.Round(x)), baseline, col)
	return sh
}

// DrawAligned draws text inside box with the given alignment, skipping it
// entirely when the box cannot hold one line of the font. The guard uses
// the same rounded LineHeight the measurement reports.
func (t *Typeface) DrawAligned(cv *Canvas, text string, box Rect, px float64, col Color, h Alignment) *ShapedText {
	return drawAligned(cv, t, text, box, px, col, h)
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
}

// NewChain returns a chain shaping with primary and, for runes it
// lacks, the first covering face of fallback.
func NewChain(primary *Typeface, fallback ...*Typeface) *Chain {
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
	c.picks[r] = f
	return f
}

// Shape splits text into runs of consecutive runes sharing a face and
// shapes each with it, on one shared baseline, serving repeats from the
// process-wide shaping cache like Typeface.Shape. The chain is the key:
// a chain held by the app reuses its entries across frames, and two
// chains over the same faces never collide. Empty text still shapes one
// primary run, so its metrics reserve the font's line height.
func (c *Chain) Shape(text string, px float64) *ShapedText {
	return cachedShape(c, px, text, func() *ShapedText { return c.shapeUncached(text, px) })
}

// shapeUncached does the run-splitting and shaping work, bypassing the
// cache.
func (c *Chain) shapeUncached(text string, px float64) *ShapedText {
	runes := []rune(text)
	if len(runes) == 0 {
		return newShapedText(text, px, []shapedRun{c.primary.shapeRun(text, px, 0)})
	}
	var runs []shapedRun
	for i := 0; i < len(runes); {
		f := c.faceFor(runes[i])
		j := i + 1
		for j < len(runes) && c.faceFor(runes[j]) == f {
			j++
		}
		runs = append(runs, f.shapeRun(string(runes[i:j]), px, i))
		i = j
	}
	return newShapedText(text, px, runs)
}

// Draw paints a shaped line; see ShapedText.Draw.
func (c *Chain) Draw(cv *Canvas, s *ShapedText, x, baselineY int, col Color) {
	s.Draw(cv, x, baselineY, col)
}

// DrawAligned draws text inside box with the given alignment; see
// Typeface.DrawAligned.
func (c *Chain) DrawAligned(cv *Canvas, text string, box Rect, px float64, col Color, h Alignment) *ShapedText {
	return drawAligned(cv, c, text, box, px, col, h)
}

// Wrap breaks text into lines of at most maxWidth pixels; see
// Typeface.Wrap.
func (c *Chain) Wrap(text string, maxWidth, px float64) []string {
	return wrapLines(c, text, maxWidth, px)
}

// Ellipsize shortens text to fit maxWidth; see Typeface.Ellipsize.
func (c *Chain) Ellipsize(text string, maxWidth, px float64) string {
	return ellipsizeText(c, text, maxWidth, px)
}

// wrapLines is the shaper-agnostic body of Wrap.
func wrapLines(s textShaper, text string, maxWidth, px float64) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{text}
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		candidate := cur + " " + w
		if cur != "" && s.Shape(candidate, px).Advance() > maxWidth {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur = candidate
	}
	return append(lines, cur)
}

func f266(px float64) fixed.Int26_6 {
	return fixed.Int26_6(int32(math.Round(px * 64)))
}

func f64(v fixed.Int26_6) float64 {
	return float64(int32(v)) / 64
}
