package render

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ParsePathData parses an SVG path data string (CSS path()'s argument)
// into a Path of move, line and cubic segments in the string's own
// coordinate space — the caller offsets it onto the box. Every command
// of the d grammar is handled; arcs subdivide into cubics (four spans
// per quarter turn, within a quarter pixel of the true arc). false
// for malformed data.
func ParsePathData(d string) (*Path, bool) {
	p := &Path{}
	var cur, start [2]float64
	var prevC2 [2]float64
	haveC, haveQ := false, false
	i := 0
	cmd := byte(0)
	for i < len(d) {
		for i < len(d) && isSVGSep(d[i]) {
			i++
		}
		if i >= len(d) {
			break
		}
		c := d[i]
		if isPathCmd(c) {
			cmd, i = c, i+1
		} else if cmd == 0 {
			return nil, false
		}
		if pathArity[upper(cmd)] == 0 {
			// Z takes no arguments and has no implicit repeat.
			p.Close()
			cur = start
			haveC, haveQ = false, false
			continue
		}
		rel := cmd >= 'a' && cmd <= 'z'
		up := upper(cmd)
		args, ni, ok := pathNumbers(d, i, pathArity[up])
		if !ok {
			return nil, false
		}
		i = ni
		nxt := func(v float64) float64 {
			if rel {
				return v
			}
			return 0
		}
		switch up {
		case 'M':
			for j := 0; j+1 < len(args); j += 2 {
				pt := [2]float64{args[j] + nxt(cur[0]), args[j+1] + nxt(cur[1])}
				if j == 0 {
					p.MoveTo(pt[0], pt[1])
					start = pt
				} else {
					p.LineTo(pt[0], pt[1])
				}
				cur = pt
				cmd = mapRel(cmd, 'L')
			}
			haveC, haveQ = false, false
		case 'L':
			for j := 0; j+1 < len(args); j += 2 {
				pt := [2]float64{args[j] + nxt(cur[0]), args[j+1] + nxt(cur[1])}
				p.LineTo(pt[0], pt[1])
				cur = pt
			}
			haveC, haveQ = false, false
		case 'H':
			for _, v := range args {
				cur[0] = v + nxt(cur[0])
				p.LineTo(cur[0], cur[1])
			}
			haveC, haveQ = false, false
		case 'V':
			for _, v := range args {
				cur[1] = v + nxt(cur[1])
				p.LineTo(cur[0], cur[1])
			}
			haveC, haveQ = false, false
		case 'C':
			for j := 0; j+5 < len(args); j += 6 {
				c1 := [2]float64{args[j] + nxt(cur[0]), args[j+1] + nxt(cur[1])}
				c2 := [2]float64{args[j+2] + nxt(cur[0]), args[j+3] + nxt(cur[1])}
				pt := [2]float64{args[j+4] + nxt(cur[0]), args[j+5] + nxt(cur[1])}
				p.CubeTo(c1[0], c1[1], c2[0], c2[1], pt[0], pt[1])
				cur, prevC2 = pt, c2
			}
			haveC, haveQ = true, false
		case 'S':
			for j := 0; j+3 < len(args); j += 4 {
				c1 := cur
				if haveC {
					c1 = [2]float64{2*cur[0] - prevC2[0], 2*cur[1] - prevC2[1]}
				}
				c2 := [2]float64{args[j] + nxt(cur[0]), args[j+1] + nxt(cur[1])}
				pt := [2]float64{args[j+2] + nxt(cur[0]), args[j+3] + nxt(cur[1])}
				p.CubeTo(c1[0], c1[1], c2[0], c2[1], pt[0], pt[1])
				cur, prevC2 = pt, c2
			}
			haveC, haveQ = true, false
		case 'Q':
			for j := 0; j+3 < len(args); j += 4 {
				q := [2]float64{args[j] + nxt(cur[0]), args[j+1] + nxt(cur[1])}
				pt := [2]float64{args[j+2] + nxt(cur[0]), args[j+3] + nxt(cur[1])}
				c1 := [2]float64{cur[0] + 2*(q[0]-cur[0])/3, cur[1] + 2*(q[1]-cur[1])/3}
				c2 := [2]float64{pt[0] + 2*(q[0]-pt[0])/3, pt[1] + 2*(q[1]-pt[1])/3}
				p.CubeTo(c1[0], c1[1], c2[0], c2[1], pt[0], pt[1])
				cur, prevC2 = pt, c2
			}
			haveC, haveQ = false, true
		case 'T':
			for j := 0; j+1 < len(args); j += 2 {
				pt := [2]float64{args[j] + nxt(cur[0]), args[j+1] + nxt(cur[1])}
				q := pt
				if haveQ {
					q = [2]float64{2*cur[0] - prevC2[0], 2*cur[1] - prevC2[1]}
				}
				c1 := [2]float64{cur[0] + 2*(q[0]-cur[0])/3, cur[1] + 2*(q[1]-cur[1])/3}
				c2 := [2]float64{pt[0] + 2*(q[0]-pt[0])/3, pt[1] + 2*(q[1]-pt[1])/3}
				p.CubeTo(c1[0], c1[1], c2[0], c2[1], pt[0], pt[1])
				cur, prevC2 = pt, c2
			}
			haveC, haveQ = false, true
		case 'A':
			for j := 0; j+6 < len(args); j += 7 {
				pt := [2]float64{args[j+5] + nxt(cur[0]), args[j+6] + nxt(cur[1])}
				arcToCubics(p, cur, args[j], args[j+1], args[j+2], args[j+3] != 0, args[j+4] != 0, pt)
				cur = pt
			}
			haveC, haveQ = false, false
		case 'Z':
			p.Close()
			cur = start
			haveC, haveQ = false, false
		}
	}
	if p.Empty() {
		return nil, false
	}
	return p, true
}

// mapRel keeps the implicit-repeat letter right: an M continues as L
// (m as l), preserving the case of the original.
func mapRel(cmd byte, up byte) byte {
	if cmd >= 'a' && cmd <= 'z' {
		return up + 'a' - 'A'
	}
	return up
}

// pathNumbers reads n numbers (or the whole run when n is 0 — a
// repeatable command's argument stream) from d at i.
func pathNumbers(d string, i int, n int) ([]float64, int, bool) {
	var out []float64
	for n == 0 || len(out) < n {
		for i < len(d) && isSVGSep(d[i]) {
			i++
		}
		if i >= len(d) || isPathCmd(d[i]) {
			break
		}
		s, ni := scanNumber(d, i)
		if s == "" {
			return nil, i, false
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, i, false
		}
		out = append(out, v)
		i = ni
	}
	if n > 0 && len(out) != n {
		return nil, i, false
	}
	if len(out) == 0 {
		return nil, i, false
	}
	return out, i, true
}

// arcToCubics appends the SVG arc from cur to pt (radii rx/ry, x-axis
// rotation rot, the large-arc and sweep flags) as subdivided cubic
// segments: the SVG spec's endpoint-to-center parameterization, one
// cubic per arc slice of at most a quarter turn.
func arcToCubics(p *Path, cur [2]float64, rx, ry, rot float64, large, sweep bool, pt [2]float64) {
	if rx == 0 || ry == 0 || (cur[0] == pt[0] && cur[1] == pt[1]) {
		p.LineTo(pt[0], pt[1])
		return
	}
	rad := rot * math.Pi / 180
	cosr, sinr := math.Cos(rad), math.Sin(rad)
	dx, dy := (cur[0]-pt[0])/2, (cur[1]-pt[1])/2
	x1 := cosr*dx + sinr*dy
	y1 := -sinr*dx + cosr*dy
	rx, ry = math.Abs(rx), math.Abs(ry)
	lam := x1*x1/(rx*rx) + y1*y1/(ry*ry)
	if lam > 1 {
		s := math.Sqrt(lam)
		rx, ry = rx*s, ry*s
	}
	num := rx*rx*ry*ry - rx*rx*y1*y1 - ry*ry*x1*x1
	den := rx*rx*y1*y1 + ry*ry*x1*x1
	if num < 0 {
		num = 0
	}
	co := math.Sqrt(num / den)
	if large == sweep {
		co = -co
	}
	cx := co * rx * y1 / ry
	cy := -co * ry * x1 / rx
	ctr := [2]float64{cosr*cx - sinr*cy + (cur[0]+pt[0])/2, sinr*cx + cosr*cy + (cur[1]+pt[1])/2}
	th1 := math.Atan2((y1-cy)/ry, (x1-cx)/rx)
	th2 := math.Atan2((-y1-cy)/ry, (-x1-cx)/rx)
	dth := th2 - th1
	if !sweep && dth > 0 {
		dth -= 2 * math.Pi
	} else if sweep && dth < 0 {
		dth += 2 * math.Pi
	}
	pointAt := func(th float64) [2]float64 {
		c, s := math.Cos(th), math.Sin(th)
		return [2]float64{ctr[0] + cosr*rx*c - sinr*ry*s, ctr[1] + sinr*rx*c + cosr*ry*s}
	}
	derivAt := func(th float64) [2]float64 {
		c, s := math.Cos(th), math.Sin(th)
		return [2]float64{-cosr*rx*s - sinr*ry*c, -sinr*rx*s + cosr*ry*c}
	}
	n := max(1, int(math.Ceil(math.Abs(dth)/(math.Pi/4))))
	step := dth / float64(n)
	k := 4.0 / 3.0 * math.Tan(step/4)
	prev, prevTh := cur, th1
	for i := 1; i <= n; i++ {
		th := th1 + step*float64(i)
		end := pointAt(th)
		d1, d2 := derivAt(prevTh), derivAt(th)
		p.CubeTo(prev[0]+k*d1[0], prev[1]+k*d1[1], end[0]-k*d2[0], end[1]-k*d2[1], end[0], end[1])
		prev, prevTh = end, th
	}
}

// pathData matches a d="..." attribute.
var pathData = regexp.MustCompile(`\sd\s*=\s*("[^"]*"|'[^']*')`)

// pathArity is each path command's parameter count.
var pathArity = map[byte]int{
	'M': 2, 'L': 2, 'H': 1, 'V': 1, 'C': 6, 'S': 4, 'Q': 4, 'T': 2, 'A': 7, 'Z': 0,
}

// normalizePaths rewrites every path's data into the explicit form
// oksvg reads: SVG lets a command repeat implicitly (a2 2 0 0 1 4 0 2 2
// 0 0 0 3 2 - two arcs, one letter; moveto pairs after the first are
// linetos) and lets arc flags run together (a1 1 0 011 1); oksvg
// mishandles both and drops the repeats, which broke icon sets that
// use them (Lucide's arcs). The output repeats every letter and spaces
// every number.
func normalizePaths(data []byte) []byte {
	return rewriteAttr(data, pathData, normalizePathData)
}

// normalizePathData is normalizePaths for one d value.
func normalizePathData(d string) string {
	out, ok := mapPathData(d, func(_ byte, _ int, num string) (string, bool) { return num, true })
	if !ok {
		return d // not path data we understand: leave it
	}
	return out
}

// scalePathData multiplies the coordinates and arc radii in d by k,
// leaving arc rotations and flags. false for path data mapPathData
// cannot read.
func scalePathData(d string, k float64) (string, bool) {
	return mapPathData(d, func(cmd byte, pos int, num string) (string, bool) {
		if upper(cmd) == 'A' && pos >= 2 && pos <= 4 {
			return num, true
		}
		v, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return "", false
		}
		return strconv.FormatFloat(v*k, 'g', -1, 64), true
	})
}

// mapPathData rewrites d into the explicit form, every command letter
// written out and every number spaced, passing each number through f
// with its command and parameter index. false for path data it cannot
// read or f rejects.
func mapPathData(d string, f func(cmd byte, pos int, num string) (string, bool)) (string, bool) {
	var out strings.Builder
	var cmd byte
	pos := 0 // the current command's next parameter index
	n := 0   // the parameters emitted for the current command
	i := 0
	for i < len(d) {
		c := d[i]
		switch {
		case isSVGSep(c):
			i++
			continue
		case isPathCmd(c):
			cmd, pos, n = c, 0, 0
			out.WriteByte(' ')
			out.WriteByte(c)
			i++
			continue
		}
		if cmd == 0 {
			return "", false
		}
		arity := pathArity[upper(cmd)]
		if arity == 0 {
			return "", false
		}
		if n > 0 && pos == 0 {
			// An implicit repeat: write the letter out (moveto continues
			// as lineto).
			rep := cmd
			switch cmd {
			case 'M':
				rep = 'L'
			case 'm':
				rep = 'l'
			}
			cmd = rep
			out.WriteByte(' ')
			out.WriteByte(rep)
		}
		var num string
		if upper(cmd) == 'A' && (pos == 3 || pos == 4) && (c == '0' || c == '1') {
			num, i = d[i:i+1], i+1 // a flag is one digit, maybe unspaced
		} else {
			num, i = scanNumber(d, i)
			if num == "" {
				return "", false
			}
		}
		mapped, ok := f(cmd, pos, num)
		if !ok {
			return "", false
		}
		out.WriteByte(' ')
		out.WriteString(mapped)
		n++
		pos = (pos + 1) % arity
	}
	return strings.TrimSpace(out.String()), true
}

// isSVGSep reports whether c separates numbers in SVG attribute
// syntax: whitespace or a comma.
func isSVGSep(c byte) bool {
	return c == ' ' || c == ',' || c == '\t' || c == '\n' || c == '\r'
}

// isPathCmd reports whether c is a path command letter.
func isPathCmd(c byte) bool {
	_, ok := pathArity[upper(c)]
	return ok && c != 'e' && c != 'E'
}

// upper is c in upper case.
func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

// scanNumber reads one SVG number at i: sign, digits, one decimal
// point (a second starts the next number), exponent.
func scanNumber(d string, i int) (string, int) {
	start := i
	if i < len(d) && (d[i] == '-' || d[i] == '+') {
		i++
	}
	dot, digits := false, false
	for i < len(d) {
		switch c := d[i]; {
		case c >= '0' && c <= '9':
			digits = true
			i++
		case c == '.' && !dot:
			dot = true
			i++
		case (c == 'e' || c == 'E') && digits:
			j := i + 1
			if j < len(d) && (d[j] == '-' || d[j] == '+') {
				j++
			}
			if j < len(d) && d[j] >= '0' && d[j] <= '9' {
				i = j
				for i < len(d) && d[i] >= '0' && d[i] <= '9' {
					i++
				}
			}
			return d[start:i], i
		default:
			if !digits {
				return "", i
			}
			return d[start:i], i
		}
	}
	if !digits {
		return "", i
	}
	return d[start:i], i
}
