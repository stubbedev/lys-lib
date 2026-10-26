package render

import (
	"math"

	"github.com/go-text/typesetting/font"
)

// DecorationLine is a set of text decoration lines (CSS
// text-decoration-line).
type DecorationLine uint8

// Decoration lines.
const (
	Underline DecorationLine = 1 << iota
	Overline
	LineThrough
)

// DecorationStyle is how decoration lines are drawn (CSS
// text-decoration-style).
type DecorationStyle uint8

// Decoration styles.
const (
	DecorationSolid DecorationStyle = iota
	DecorationDouble
	DecorationDotted
	DecorationDashed
	DecorationWavy
)

// Decoration is a text decoration: its lines, their style, and their
// color (zero: the text's).
type Decoration struct {
	Lines DecorationLine
	Style DecorationStyle
	Color Color
}

// DrawDecoration draws d along the line s, drawn with its pen at x and
// baseline: each line spans the shaped advance - the line's visual
// extent, whatever its runs' directions - at the font's own underline,
// strikethrough, and ascent metrics (post and OS/2, variable-font
// deltas included), in d.Color or else col. Every text widget and a
// rich label's runs paint decorations through it.
func (s *ShapedText) DrawDecoration(cv *Canvas, x, baseline int, d Decoration, col Color) {
	if d.Lines == 0 || len(s.runs) == 0 {
		return
	}
	if d.Color != 0 {
		col = d.Color
	}
	m := decorationMetrics(s.runs[0].face, s.px)
	w := s.Advance()
	x0, x1 := float64(x), float64(x)+w
	for _, l := range []struct {
		line DecorationLine
		y    float64
	}{
		{Underline, float64(baseline) + m.under},
		{Overline, float64(baseline) - s.Ascent() + m.thick/2},
		{LineThrough, float64(baseline) - m.strike},
	} {
		if d.Lines&l.line != 0 {
			drawDecorationLine(cv, x0, x1, l.y, m.thick, d.Style, col)
		}
	}
}

// decoMetrics are a face's decoration offsets at a size, in pixels:
// the underline's center below the baseline, the strikethrough's
// center above it, and the line thickness.
type decoMetrics struct{ under, strike, thick float64 }

// decorationMetrics reads the face's metrics, falling back to the
// common proportions where the font leaves them zero.
func decorationMetrics(t *Typeface, px float64) decoMetrics {
	k := px / t.upem
	up := float64(t.face.LineMetric(font.UnderlinePosition))
	ut := float64(t.face.LineMetric(font.UnderlineThickness))
	sp := float64(t.face.LineMetric(font.StrikethroughPosition))
	m := decoMetrics{under: -up * k, strike: sp * k, thick: ut * k}
	if ut <= 0 {
		m.thick = px / 14
	}
	if up == 0 {
		m.under = px / 10
	}
	if sp == 0 {
		m.strike = px * 0.3
	}
	m.thick = math.Max(1, m.thick)
	return m
}

// drawDecorationLine draws one decoration line centered at y.
func drawDecorationLine(cv *Canvas, x0, x1, y, thick float64, style DecorationStyle, col Color) {
	t := int(math.Round(thick))
	rect := func(a, b, cy float64) {
		cv.FillRect(Rect{X: int(math.Round(a)), Y: int(math.Round(cy - thick/2)), W: int(math.Round(b - a)), H: t}, col)
	}
	switch style {
	case DecorationDouble:
		gap := thick * 1.5
		rect(x0, x1, y-gap/2)
		rect(x0, x1, y+gap/2)
	case DecorationDotted:
		for a := x0; a < x1; a += 2 * thick {
			rect(a, math.Min(a+thick, x1), y)
		}
	case DecorationDashed:
		dash := 3 * thick
		for a := x0; a < x1; a += 2 * dash {
			rect(a, math.Min(a+dash, x1), y)
		}
	case DecorationWavy:
		// A sine of amplitude and half-period from the thickness,
		// stroked in short segments.
		amp, half := thick*1.5, thick*3
		px, py := x0, y
		for a := x0 + 1; a <= x1; a++ {
			ny := y + amp*math.Sin((a-x0)*math.Pi/half)
			cv.Line(int(math.Round(px)), int(math.Round(py)), int(math.Round(a)), int(math.Round(ny)), t, col)
			px, py = a, ny
		}
	default:
		rect(x0, x1, y)
	}
}
