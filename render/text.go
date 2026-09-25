package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/shaping"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// Typeface is a loaded font plus the machinery to shape and rasterize it.
// One instance per font family; it is not safe for concurrent use.
type Typeface struct {
	face   *font.Face
	upem   float64
	shaper shaping.HarfbuzzShaper
}

// LoadFont parses font data (TTF or OTF).
func LoadFont(data []byte) (*Typeface, error) {
	face, err := font.ParseTTF(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("render: parse font: %w", err)
	}
	return &Typeface{face: face, upem: float64(face.Upem())}, nil
}

// ShapedText is text shaped at a fixed pixel size. Glyph positions are
// relative to the line origin.
type ShapedText struct {
	run  shaping.Output
	text string
	px   float64
}

// Shape lays text out at the given pixel size.
func (t *Typeface) Shape(text string, px float64) *ShapedText {
	runes := []rune(text)
	run := t.shaper.Shape(shaping.Input{
		Text:      runes,
		RunStart:  0,
		RunEnd:    len(runes),
		Direction: di.DirectionLTR,
		Face:      t.face,
		Size:      f266(px),
	})
	return &ShapedText{run: run, text: text, px: px}
}

// Advance returns the line width in pixels.
func (s *ShapedText) Advance() float64 {
	return f64(s.run.Advance)
}

// Ascent returns the font's ascent above the baseline in pixels.
func (s *ShapedText) Ascent() float64 {
	return f64(s.run.LineBounds.Ascent)
}

// Descent returns the font's descent below the baseline in pixels
// (positive).
func (s *ShapedText) Descent() float64 {
	return -f64(s.run.LineBounds.Descent)
}

// Text returns the string the run was shaped from.
func (s *ShapedText) Text() string { return s.text }

// Draw paints the run with its baseline at (x, baselineY), clipped to the
// canvas like every other primitive.
func (t *Typeface) Draw(cv *Canvas, s *ShapedText, x, baselineY int, col Color) {
	clip := cv.clip
	if clip.Empty() {
		return
	}
	scale := s.px / t.upem
	dotX := float64(x)
	base := float64(baselineY)
	for i := range s.run.Glyphs {
		g := &s.run.Glyphs[i]
		penX := dotX + f64(g.XOffset) + f64(g.XBearing)
		penY := base + f64(g.YOffset)
		t.drawGlyph(cv, clip, g.GlyphID, scale, penX, penY, col)
		dotX += f64(g.Advance)
	}
}

// drawGlyph rasterizes one glyph outline with its baseline pen at (penX,
// penY) into the clip.
func (t *Typeface) drawGlyph(cv *Canvas, clip Rect, gid font.GID, scale float64, penX, penY float64, col Color) {
	outline, ok := t.face.GlyphDataOutline(gid)
	if !ok || len(outline.Segments) == 0 {
		return
	}

	// Bounds of the glyph in canvas pixels, grown a pixel for AA.
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, seg := range outline.Segments {
		for _, p := range seg.Args {
			x := penX + float64(p.X)*scale
			y := penY - float64(p.Y)*scale
			minX, minY = math.Min(minX, x), math.Min(minY, y)
			maxX, maxY = math.Max(maxX, x), math.Max(maxY, y)
		}
	}
	gb := Rect{X: int(math.Floor(minX)) - 1, Y: int(math.Floor(minY)) - 1, W: 0, H: 0}
	gb.W = int(math.Ceil(maxX)) + 1 - gb.X
	gb.H = int(math.Ceil(maxY)) + 1 - gb.Y
	gb = gb.Intersect(clip)
	if gb.Empty() {
		return
	}

	rast := vector.NewRasterizer(gb.W, gb.H)
	toRaster := func(p ot.SegmentPoint) (float32, float32) {
		return float32(penX+float64(p.X)*scale) - float32(gb.X),
			float32(penY-float64(p.Y)*scale) - float32(gb.Y)
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
	mask := image.NewAlpha(image.Rect(0, 0, gb.W, gb.H))
	rast.Draw(mask, mask.Bounds(), image.NewUniform(color.Alpha{A: 255}), image.Point{})
	for y := gb.Y; y < gb.Y+gb.H; y++ {
		for x := gb.X; x < gb.X+gb.W; x++ {
			m := uint32(mask.AlphaAt(x-gb.X, y-gb.Y).A)
			if m == 0 {
				continue
			}
			src := Color((uint32(col.A())*m/255)<<24 |
				(uint32(col.R())*m/255)<<16 |
				(uint32(col.G())*m/255)<<8 |
				(uint32(col.B())*m)/255)
			cv.set(x, y, src.over(cv.get(x, y)))
		}
	}
}

// Wrap breaks text into lines of at most maxWidth pixels, greedily, on
// spaces. A single word longer than maxWidth stays on its own line and
// overflows. Trailing spaces are dropped.
func (t *Typeface) Wrap(text string, maxWidth, px float64) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{text}
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		candidate := cur + " " + w
		if cur != "" && t.Shape(candidate, px).Advance() > maxWidth {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur = candidate
	}
	return append(lines, cur)
}

// Ellipsize shortens text to fit maxWidth, replacing the cut remainder
// with an ellipsis. Text that already fits is returned unchanged.
func (t *Typeface) Ellipsize(text string, maxWidth, px float64) string {
	if t.Shape(text, px).Advance() <= maxWidth {
		return text
	}
	const ell = "…"
	runes := []rune(text)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		candidate := string(runes[:mid]) + ell
		if t.Shape(candidate, px).Advance() <= maxWidth {
			lo = mid
			continue
		}
		hi = mid - 1
	}
	return string(runes[:lo]) + ell
}

// Alignment selects how a drawn run is positioned inside its box.
type Alignment uint8

const (
	AlignStart Alignment = iota
	AlignCenter
	AlignEnd
)

// DrawAligned draws text inside box with the given alignment, skipping it
// entirely when the box cannot hold one line of the font.
func (t *Typeface) DrawAligned(cv *Canvas, text string, box Rect, px float64, col Color, h Alignment) *ShapedText {
	s := t.Shape(text, px)
	lineH := s.Ascent() + s.Descent()
	if float64(box.H) < lineH {
		return nil
	}
	var x float64
	switch h {
	case AlignCenter:
		x = float64(box.X) + (float64(box.W)-s.Advance())/2
	case AlignEnd:
		x = float64(box.X) + float64(box.W) - s.Advance()
	default:
		x = float64(box.X)
	}
	baseline := box.Y + int(math.Round((float64(box.H)-lineH)/2+s.Ascent()))
	t.Draw(cv, s, int(math.Round(x)), baseline, col)
	return s
}

func f266(px float64) fixed.Int26_6 {
	return fixed.Int26_6(int32(math.Round(px * 64)))
}

func f64(v fixed.Int26_6) float64 {
	return float64(int32(v)) / 64
}
