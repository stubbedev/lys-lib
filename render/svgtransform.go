package render

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/srwiley/rasterx"
)

// parseSVGTransform parses an SVG transform list into the matrix it
// applies: each function post-multiplied in order, so the rightmost
// acts on the points first. false for a malformed list.
func parseSVGTransform(v string) (rasterx.Matrix2D, bool) {
	m := rasterx.Identity
	i := 0
	for {
		for i < len(v) && isSVGSep(v[i]) {
			i++
		}
		if i >= len(v) {
			return m, true
		}
		open := strings.IndexByte(v[i:], '(')
		if open < 0 {
			return m, false
		}
		end := strings.IndexByte(v[i+open:], ')')
		if end < 0 {
			return m, false
		}
		name := strings.TrimSpace(v[i : i+open])
		args, ok := svgNumbers(v[i+open+1 : i+open+end])
		if !ok {
			return m, false
		}
		if m, ok = applyTransform(m, name, args); !ok {
			return m, false
		}
		i += open + end + 1
	}
}

// applyTransform post-multiplies m by one transform function.
func applyTransform(m rasterx.Matrix2D, name string, a []float64) (rasterx.Matrix2D, bool) {
	rad := func(deg float64) float64 { return deg * math.Pi / 180 }
	switch {
	case name == "matrix" && len(a) == 6:
		return m.Mult(rasterx.Matrix2D{A: a[0], B: a[1], C: a[2], D: a[3], E: a[4], F: a[5]}), true
	case name == "translate" && len(a) == 1:
		return m.Translate(a[0], 0), true
	case name == "translate" && len(a) == 2:
		return m.Translate(a[0], a[1]), true
	case name == "scale" && len(a) == 1:
		return m.Scale(a[0], a[0]), true
	case name == "scale" && len(a) == 2:
		return m.Scale(a[0], a[1]), true
	case name == "rotate" && len(a) == 1:
		return m.Rotate(rad(a[0])), true
	case name == "rotate" && len(a) == 3:
		return m.Translate(a[1], a[2]).Rotate(rad(a[0])).Translate(-a[1], -a[2]), true
	case name == "skewX" && len(a) == 1:
		return m.SkewX(rad(a[0])), true
	case name == "skewY" && len(a) == 1:
		return m.SkewY(rad(a[0])), true
	}
	return m, false
}

// svgNumbers reads a list of comma or space separated numbers: a
// transform function's arguments, a polygon's points.
func svgNumbers(s string) ([]float64, bool) {
	var out []float64
	i := 0
	for {
		for i < len(s) && isSVGSep(s[i]) {
			i++
		}
		if i >= len(s) {
			return out, true
		}
		num, ni := scanNumber(s, i)
		if num == "" {
			return nil, false
		}
		v, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return nil, false
		}
		out = append(out, v)
		i = ni
	}
}

// matrixString is m as the SVG matrix() function.
func matrixString(m rasterx.Matrix2D) string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	return "matrix(" + f(m.A) + " " + f(m.B) + " " + f(m.C) + " " + f(m.D) + " " + f(m.E) + " " + f(m.F) + ")"
}

// transformAttr matches a transform="..." attribute; styleAttr a
// style="..." one.
var (
	transformAttr = regexp.MustCompile(`\stransform\s*=\s*("[^"]*"|'[^']*')`)
	styleAttr     = regexp.MustCompile(`\sstyle\s*=\s*("[^"]*"|'[^']*')`)
)

// canonicalTransforms rewrites every transform, attribute or style
// declaration, into the one matrix() it composes to. oksvg's own
// parser reads scale(s) as scale(s, 0), collapsing the shape; handed a
// matrix, it composes exactly what parseSVGTransform reads, so the
// document has one transform semantics. A list that does not parse is
// left as written, for layoutSVG to reject.
func canonicalTransforms(data []byte) []byte {
	data = rewriteAttr(data, transformAttr, func(v string) string {
		if m, ok := parseSVGTransform(v); ok {
			return matrixString(m)
		}
		return v
	})
	return rewriteAttr(data, styleAttr, func(v string) string {
		decls := strings.Split(v, ";")
		for i, decl := range decls {
			k, val, found := strings.Cut(decl, ":")
			if !found || strings.TrimSpace(k) != "transform" {
				continue
			}
			if m, ok := parseSVGTransform(val); ok {
				decls[i] = k + ":" + matrixString(m)
			}
		}
		return strings.Join(decls, ";")
	})
}

// rewriteAttr replaces the value of every attribute re matches (its
// one group the quoted value) with f of it, keeping the quotes.
func rewriteAttr(data []byte, re *regexp.Regexp, f func(string) string) []byte {
	return re.ReplaceAllFunc(data, func(m []byte) []byte {
		loc := re.FindSubmatchIndex(m)
		q := m[loc[2]]
		v := string(m[loc[2]+1 : loc[3]-1])
		out := make([]byte, 0, len(m))
		out = append(out, m[:loc[2]+1]...)
		out = append(out, f(v)...)
		return append(append(out, q), m[loc[3]:]...)
	})
}
