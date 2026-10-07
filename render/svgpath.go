package render

import (
	"bytes"
	"regexp"
	"strings"
)

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
	return pathData.ReplaceAllFunc(data, func(m []byte) []byte {
		i := bytes.IndexAny(m, `"'`)
		q := m[i]
		inner := string(m[i+1 : len(m)-1])
		return []byte(` d=` + string(q) + normalizePathData(inner) + string(q))
	})
}

// normalizePathData is normalizePaths for one d value.
func normalizePathData(d string) string {
	var out strings.Builder
	var cmd byte
	pos := 0 // the current command's next parameter index
	n := 0   // the parameters emitted for the current command
	i := 0
	for i < len(d) {
		c := d[i]
		switch {
		case c == ' ' || c == ',' || c == '\t' || c == '\n' || c == '\r':
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
			return d // not path data we understand: leave it
		}
		arity := pathArity[upper(cmd)]
		if arity == 0 {
			return d
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
				return d
			}
		}
		out.WriteByte(' ')
		out.WriteString(num)
		n++
		pos = (pos + 1) % arity
	}
	return strings.TrimSpace(out.String())
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
