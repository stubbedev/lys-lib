package i18n

import (
	"fmt"
	"strconv"
	"strings"
)

type pluralFunc func(n uint64) uint64

func parsePluralForms(header string) (nplurals int, plural pluralFunc, err error) {
	nplurals, plural = 2, germanic
	if header == "" {
		return nplurals, plural, nil
	}
	for part := range strings.SplitSeq(header, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "nplurals":
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 1 {
				return 0, nil, fmt.Errorf("i18n: Plural-Forms %q: bad nplurals", header)
			}
			nplurals = n
		case "plural":
			f, err := compilePlural(strings.TrimSpace(value))
			if err != nil {
				return 0, nil, fmt.Errorf("i18n: Plural-Forms %q: %w", header, err)
			}
			plural = f
		}
	}
	return nplurals, plural, nil
}

func germanic(n uint64) uint64 {
	if n == 1 {
		return 0
	}
	return 1
}

type pluralParser struct {
	src string
	pos int
}

func compilePlural(src string) (pluralFunc, error) {
	p := &pluralParser{src: src}
	f, err := p.ternary()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(p.src) {
		return nil, fmt.Errorf("unexpected %q at %d", p.src[p.pos:], p.pos)
	}
	return f, nil
}

func (p *pluralParser) space() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *pluralParser) eat(op string) bool {
	p.space()
	if strings.HasPrefix(p.src[p.pos:], op) {
		if (op == "<" || op == ">" || op == "!") && strings.HasPrefix(p.src[p.pos+1:], "=") {
			return false
		}
		p.pos += len(op)
		return true
	}
	return false
}

func (p *pluralParser) ternary() (pluralFunc, error) {
	cond, err := p.binary(0)
	if err != nil {
		return nil, err
	}
	if !p.eat("?") {
		return cond, nil
	}
	then, err := p.ternary()
	if err != nil {
		return nil, err
	}
	if !p.eat(":") {
		return nil, fmt.Errorf("missing ':' at %d", p.pos)
	}
	otherwise, err := p.ternary()
	if err != nil {
		return nil, err
	}
	return func(n uint64) uint64 {
		if cond(n) != 0 {
			return then(n)
		}
		return otherwise(n)
	}, nil
}

var levels = [][]string{
	{"||"},
	{"&&"},
	{"==", "!="},
	{"<=", ">=", "<", ">"},
	{"+", "-"},
	{"*", "/", "%"},
}

func (p *pluralParser) binary(level int) (pluralFunc, error) {
	if level == len(levels) {
		return p.unary()
	}
	left, err := p.binary(level + 1)
	if err != nil {
		return nil, err
	}
	for {
		op := ""
		for _, candidate := range levels[level] {
			if p.eat(candidate) {
				op = candidate
				break
			}
		}
		if op == "" {
			return left, nil
		}
		right, err := p.binary(level + 1)
		if err != nil {
			return nil, err
		}
		left = combine(op, left, right)
	}
}

func boolean(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

func combine(op string, a, b pluralFunc) pluralFunc {
	return func(n uint64) uint64 {
		x, y := a(n), b(n)
		switch op {
		case "||":
			return boolean(x != 0 || y != 0)
		case "&&":
			return boolean(x != 0 && y != 0)
		case "==":
			return boolean(x == y)
		case "!=":
			return boolean(x != y)
		case "<=":
			return boolean(x <= y)
		case ">=":
			return boolean(x >= y)
		case "<":
			return boolean(x < y)
		case ">":
			return boolean(x > y)
		case "+":
			return x + y
		case "-":
			return x - y
		case "*":
			return x * y
		case "/":
			if y == 0 {
				return 0
			}
			return x / y
		default:
			if y == 0 {
				return 0
			}
			return x % y
		}
	}
}

func (p *pluralParser) unary() (pluralFunc, error) {
	if p.eat("!") {
		inner, err := p.unary()
		if err != nil {
			return nil, err
		}
		return func(n uint64) uint64 { return boolean(inner(n) == 0) }, nil
	}
	if p.eat("(") {
		inner, err := p.ternary()
		if err != nil {
			return nil, err
		}
		if !p.eat(")") {
			return nil, fmt.Errorf("missing ')' at %d", p.pos)
		}
		return inner, nil
	}
	p.space()
	if p.pos < len(p.src) && p.src[p.pos] == 'n' {
		p.pos++
		return func(n uint64) uint64 { return n }, nil
	}
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		p.pos++
	}
	if start == p.pos {
		return nil, fmt.Errorf("expected a number, n, or '(' at %d", p.pos)
	}
	v, err := strconv.ParseUint(p.src[start:p.pos], 10, 64)
	if err != nil {
		return nil, err
	}
	return func(uint64) uint64 { return v }, nil
}
