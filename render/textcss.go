package render

import (
	"fmt"
	"strconv"
	"strings"

	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/shaping"
	"golang.org/x/image/math/fixed"

	"github.com/stubbedev/gelm/internal/text"
)

// WordSep reports whether r is a CSS word separator: the characters
// word-spacing widens and justify distributes space across.
func WordSep(r rune) bool {
	return r == ' ' || r == '\u00a0' || r == '\u3000'
}

// WordSpaced returns the face shaping with px of word spacing added
// after every word separator (CSS word-spacing): like letter spacing,
// the advances themselves carry it, so measuring, wrapping, and
// painting agree. Memoized; zero returns the face itself.
func (t *Typeface) WordSpaced(px float64) *Typeface {
	if px == t.word {
		return t
	}
	if v, ok := t.worded[px]; ok {
		return v
	}
	c := *t
	c.word, c.tabular, c.worded, c.featured = px, nil, nil, nil
	if t.worded == nil {
		t.worded = map[float64]*Typeface{}
	}
	t.worded[px] = &c
	return &c
}

// WithFeatureSpec returns the face shaping with the OpenType features
// in spec on: the canonical `tag=value` list, semicolon separated, the
// form font-feature-settings and the font-variant properties compute
// to. Tags the face lacks are ignored by the shaper. Memoized, so the
// shaping cache keeps hitting; the empty spec returns the face itself.
func (t *Typeface) WithFeatureSpec(spec string) *Typeface {
	if spec == "" {
		return t
	}
	if v, ok := t.featured[spec]; ok {
		return v
	}
	feats, ok := parseFeatureSpec(spec)
	if !ok {
		return t
	}
	c := *t
	c.features = feats
	c.shaper = shaping.HarfbuzzShaper{}
	c.tabular, c.worded, c.featured = nil, nil, nil
	if t.featured == nil {
		t.featured = map[string]*Typeface{}
	}
	t.featured[spec] = &c
	return &c
}

// parseFeatureSpec reads the canonical feature list `tag=v;tag=v`:
// tags exactly four ASCII letters or digits, values non-negative
// integers or reals. It reports false on any malformed entry, so the
// caller keeps the base face.
func parseFeatureSpec(spec string) ([]shaping.FontFeature, bool) {
	var feats []shaping.FontFeature
	for kv := range strings.SplitSeq(spec, ";") {
		tag, val, ok := strings.Cut(kv, "=")
		if !ok || len(tag) != 4 {
			return nil, false
		}
		for i := range 4 {
			c := tag[i]
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
				return nil, false
			}
		}
		f, err := strconv.ParseFloat(val, 32)
		if err != nil || f < 0 {
			return nil, false
		}
		feats = append(feats, shaping.FontFeature{Tag: ot.MustNewTag(strings.ToLower(tag)), Value: uint32(f)})
	}
	return feats, true
}

// WordSpaced returns the chain shaping with px of word spacing after
// every word separator, whichever face a rune lands on (see
// Typeface.WordSpaced).
func (c *Chain) WordSpaced(px float64) *Chain {
	if px == c.word {
		return c
	}
	return c.variant(fmt.Sprintf("ws:%g", px), func(n *Chain) { n.word = px })
}

// WithFeatureSpec returns the chain shaping with the OpenType features
// in spec on, whichever face a rune lands on (see
// Typeface.WithFeatureSpec).
func (c *Chain) WithFeatureSpec(spec string) *Chain {
	if spec == "" {
		return c
	}
	return c.variant("feat:"+spec, func(n *Chain) { n.features = spec })
}

// HyphensMode selects how WrapOptions treats soft hyphens.
type HyphensMode uint8

// Hyphen modes: none hides U+00AD entirely, manual breaks at it (the
// hyphen renders where a break lands), auto also needs a dictionary
// the engine does not ship and reads as manual.
const (
	HyphensOff HyphensMode = iota
	HyphensManual
	HyphensAuto
	// HyphensLegacy leaves soft hyphens untouched: the plain Wrap
	// path, which never read the CSS property and keeps shaping the
	// U+00AD glyph as before.
	HyphensLegacy
)

// WrapWordMode selects word-break.
type WrapWordMode uint8

// Word-break modes.
const (
	WrapWordNormal WrapWordMode = iota
	WrapWordBreakAll
	WrapWordKeepAll
)

// WrapOptions selects how WrapTextOpts breaks lines beyond the default
// UAX #14 fill: word-break, hyphens, line-break anywhere, and the
// white-space family's space preservation. The zero value is the plain
// WrapText behavior.
type WrapOptions struct {
	Break          WrapWordMode
	Hyphens        HyphensMode
	Anywhere       bool
	PreserveSpaces bool
}

// WrapTextOpts breaks text into lines of at most maxWidth pixels under
// the wrap options; see WrapText for the default behavior.
func WrapTextOpts(f Font, s string, maxWidth, px float64, o WrapOptions) []string {
	return wrapLines(f, s, maxWidth, px, o)
}

// wrapOptions is WrapText's default: the pre-CSS wrap behavior, with
// soft hyphens passed through untouched.
var wrapOptions = WrapOptions{Hyphens: HyphensLegacy}

// hyphenPrep strips or keeps the text's soft hyphens before wrapping:
// none hides them, manual and auto break at them, legacy passes them
// through for the plain wrap path.
func hyphenPrep(s string, m HyphensMode) string {
	if m == HyphensOff {
		return strings.ReplaceAll(s, "\u00ad", "")
	}
	return s
}

// hyphenRow cleans one wrapped row's soft hyphens: a row whose break
// landed on the soft hyphen ends with a real hyphen, and the row's
// other soft hyphens stay hidden (CSS: the hyphenation point renders
// only where the break happened).
func hyphenRow(row string, m HyphensMode) string {
	if m != HyphensManual && m != HyphensAuto || !strings.Contains(row, "\u00ad") {
		return row
	}
	if rest, ok := strings.CutSuffix(row, "\u00ad"); ok {
		return rest + "-"
	}
	return strings.ReplaceAll(row, "\u00ad", "")
}

// rowText closes one row from runes[a:b]: collapsed modes trim the
// trailing spaces the break rode on, preserved modes keep them.
func rowText(runes []rune, a, b int, o WrapOptions) string {
	row := string(runes[a:b])
	if !o.PreserveSpaces {
		row = strings.TrimRight(row, " ")
	}
	return row
}

// skipRowSpaces steps past the spaces a collapsed break leaves at the
// head of the continuation row.
func skipRowSpaces(runes []rune, from int, o WrapOptions) int {
	if o.PreserveSpaces {
		return from
	}
	for from < len(runes) && runes[from] == ' ' {
		from++
	}
	return from
}

// wrapGraphemes fills lines breaking at every grapheme cluster
// boundary: word-break break-all and line-break anywhere.
func wrapGraphemes(s textShaper, runes []rune, maxWidth, px float64, o WrapOptions) []string {
	var lines []string
	start, end := 0, 0
	for i := 0; i < len(runes); {
		j := text.NextCluster(runes, i)
		if end > start && s.Shape(string(runes[start:j]), px).Advance() > maxWidth {
			lines = append(lines, rowText(runes, start, end, o))
			start = skipRowSpaces(runes, end, o)
			if start >= len(runes) {
				return lines
			}
			i, end = start, start
			continue
		}
		i, end = j, j
	}
	if end > start || len(lines) == 0 {
		lines = append(lines, rowText(runes, start, end, o))
	}
	return lines
}

// keepAllOK reports whether the UAX #14 opportunity at rune index end
// (a candidate row end) survives word-break keep-all: only spaces and
// hyphens may close a row; the CJK opportunities inside ideographic
// runs must not.
func keepAllOK(runes []rune, end int) bool {
	if end == 0 {
		return false
	}
	r := runes[end-1]
	return r == ' ' || r == '-'
}

// addWordSpacing widens the last glyph of every word-separator cluster
// by sp, and the run's advance with it (CSS word-spacing).
func addWordSpacing(run *shaping.Output, runes []rune, sp fixed.Int26_6) {
	gs := run.Glyphs
	for i := range gs {
		ti := gs[i].TextIndex()
		if ti < len(runes) && WordSep(runes[ti]) && (i == len(gs)-1 || gs[i+1].TextIndex() != ti) {
			gs[i].Advance += sp
			run.Advance += sp
		}
	}
}

// SeparatorCount reports how many word separators the line holds: the
// justify distribution divides the leftover width across them.
func (s *ShapedText) SeparatorCount() int {
	n := 0
	for _, r := range s.text {
		if WordSep(r) {
			n++
		}
	}
	return n
}

// DrawJustified paints the line like Draw with gap pixels added after
// every word separator: the justify distribution's per-space share,
// which the caller computed from the row's leftover width. Rows
// without separators draw exactly like Draw.
func (s *ShapedText) DrawJustified(cv *Canvas, x, baselineY int, col Color, gap float64) {
	clip := cv.clip
	if clip.Empty() {
		return
	}
	dev := float64(cv.num) / float64(cv.denom)
	dotX := float64(x) * dev
	base := float64(baselineY) * dev
	gap *= dev
	rs := []rune(s.text)
	for i := range s.runs {
		r := &s.runs[i]
		scale := s.px * dev / r.face.upem
		penX := dotX
		for j := range r.out.Glyphs {
			g := &r.out.Glyphs[j]
			drawGlyph(cv, clip, r.face, g.GlyphID, scale,
				penX+f64(g.XOffset)*dev,
				base-f64(g.YOffset)*dev, col)
			penX += f64(g.Advance) * dev
			ti := r.start + g.TextIndex()
			if ti < len(rs) && WordSep(rs[ti]) && (j == len(r.out.Glyphs)-1 || r.out.Glyphs[j+1].TextIndex() != g.TextIndex()) {
				penX += gap
			}
		}
		dotX += f64(r.out.Advance)*dev + gap*float64(sepClusters(r, rs))
	}
}

// sepClusters counts the run's separator clusters, the gaps
// DrawJustified adds inside the run once its own glyphs are spent.
func sepClusters(r *shapedRun, rs []rune) int {
	n := 0
	for j := range r.out.Glyphs {
		g := &r.out.Glyphs[j]
		ti := r.start + g.TextIndex()
		if ti < len(rs) && WordSep(rs[ti]) && (j == len(r.out.Glyphs)-1 || r.out.Glyphs[j+1].TextIndex() != g.TextIndex()) {
			n++
		}
	}
	return n
}
