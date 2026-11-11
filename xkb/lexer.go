package xkb

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

type tokenKind uint8

const (
	tokEOF tokenKind = iota
	tokString
	tokKeyName
	tokIdent
	tokDigit
	tokInteger
	tokFloat
	tokSemi
	tokOBrace
	tokCBrace
	tokEquals
	tokOBracket
	tokCBracket
	tokOParen
	tokCParen
	tokDot
	tokComma
	tokPlus
	tokMinus
	tokTimes
	tokDivide
	tokExclam
	tokInvert
	tokKeymap
	tokKeycodes
	tokTypes
	tokSymbols
	tokCompat
	tokGeometry
	tokSemantics
	tokLayout
	tokInclude
	tokOverride
	tokAugment
	tokReplace
	tokAlternate
	tokVirtualMods
	tokType
	tokInterpret
	tokAction
	tokKey
	tokAlias
	tokGroup
	tokModifierMap
	tokIndicator
	tokShape
	tokKeys
	tokRow
	tokSection
	tokOverlay
	tokText
	tokOutline
	tokSolid
	tokLogo
	tokVirtual
	tokPartial
	tokDefault
	tokHidden
	tokAlphanumericKeys
	tokModifierKeys
	tokKeypadKeys
	tokFunctionKeys
	tokAlternateGroup
)

var keywords struct {
	once   sync.Once
	byName map[string]tokenKind
}

// keywordKinds is the token kind of each keyword, built for the first
// keymap the process compiles.
func keywordKinds() map[string]tokenKind {
	keywords.once.Do(func() {
		keywords.byName = map[string]tokenKind{
			"action":                tokAction,
			"alias":                 tokAlias,
			"alphanumeric_keys":     tokAlphanumericKeys,
			"alternate_group":       tokAlternateGroup,
			"alternate":             tokAlternate,
			"augment":               tokAugment,
			"default":               tokDefault,
			"function_keys":         tokFunctionKeys,
			"group":                 tokGroup,
			"hidden":                tokHidden,
			"include":               tokInclude,
			"indicator":             tokIndicator,
			"interpret":             tokInterpret,
			"keypad_keys":           tokKeypadKeys,
			"key":                   tokKey,
			"keys":                  tokKeys,
			"logo":                  tokLogo,
			"modifier_keys":         tokModifierKeys,
			"modifier_map":          tokModifierMap,
			"mod_map":               tokModifierMap,
			"modmap":                tokModifierMap,
			"outline":               tokOutline,
			"overlay":               tokOverlay,
			"override":              tokOverride,
			"partial":               tokPartial,
			"replace":               tokReplace,
			"row":                   tokRow,
			"section":               tokSection,
			"shape":                 tokShape,
			"solid":                 tokSolid,
			"text":                  tokText,
			"type":                  tokType,
			"virtual_modifiers":     tokVirtualMods,
			"virtual":               tokVirtual,
			"xkb_compatibility_map": tokCompat,
			"xkb_compatibility":     tokCompat,
			"xkb_compat_map":        tokCompat,
			"xkb_compat":            tokCompat,
			"xkb_geometry":          tokGeometry,
			"xkb_keycodes":          tokKeycodes,
			"xkb_keymap":            tokKeymap,
			"xkb_layout":            tokLayout,
			"xkb_semantics":         tokSemantics,
			"xkb_symbols":           tokSymbols,
			"xkb_types":             tokTypes,
		}
	})
	return keywords.byName
}

type token struct {
	kind tokenKind
	text string
	num  int64
	line int
}

type lexer struct {
	ctx  *Context
	name string
	src  string
	pos  int
	line int
}

func newLexer(c *Context, name, src string) *lexer {
	return &lexer{ctx: c, name: name, src: src, line: 1}
}

const maxKeywordLen = len("xkb_compatibility_map")

func lookupKeyword(text string) (tokenKind, bool) {
	if len(text) > maxKeywordLen {
		return 0, false
	}
	var buf [maxKeywordLen]byte
	for i := range len(text) {
		c := text[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		buf[i] = c
	}
	kind, ok := keywordKinds()[string(buf[:len(text)])]
	return kind, ok
}

func (l *lexer) errorf(format string, args ...any) error {
	return fmt.Errorf("%s:%d: %w", l.name, l.line, fmt.Errorf(format, args...))
}

func (l *lexer) peekByte() byte {
	if l.pos >= len(l.src) {
		return 0
	}
	return l.src[l.pos]
}

func (l *lexer) skipBlank() {
	for l.pos < len(l.src) {
		rest := l.src[l.pos:]
		switch c := rest[0]; {
		case c == '\n':
			l.line++
			l.pos++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			l.pos++
		case strings.HasPrefix(rest, "\u200e") || strings.HasPrefix(rest, "\u200f"):
			l.pos += len("\u200e")
		case c == '#' || strings.HasPrefix(rest, "//"):
			end := strings.IndexByte(rest, '\n')
			if end < 0 {
				l.pos = len(l.src)
				return
			}
			l.pos += end
		default:
			return
		}
	}
}

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func (l *lexer) next() (token, error) {
	l.skipBlank()
	if l.pos >= len(l.src) {
		return token{kind: tokEOF, line: l.line}, nil
	}
	c := l.src[l.pos]
	switch c {
	case '"':
		return l.stringToken()
	case '<':
		return l.keyNameToken()
	}
	if kind, ok := punctuation(c); ok {
		l.pos++
		return token{kind: kind, line: l.line}, nil
	}
	if isAlpha(c) || c == '_' {
		start := l.pos
		for l.pos < len(l.src) && (isAlpha(l.src[l.pos]) || isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
			l.pos++
		}
		text := l.src[start:l.pos]
		if kind, ok := lookupKeyword(text); ok {
			return token{kind: kind, text: text, line: l.line}, nil
		}
		return token{kind: tokIdent, text: text, line: l.line}, nil
	}
	if isDigit(c) {
		return l.numberToken()
	}
	return token{}, l.errorf("unrecognized token %q", string(rune(c)))
}

func punctuation(c byte) (tokenKind, bool) {
	switch c {
	case ';':
		return tokSemi, true
	case '{':
		return tokOBrace, true
	case '}':
		return tokCBrace, true
	case '=':
		return tokEquals, true
	case '[':
		return tokOBracket, true
	case ']':
		return tokCBracket, true
	case '(':
		return tokOParen, true
	case ')':
		return tokCParen, true
	case '.':
		return tokDot, true
	case ',':
		return tokComma, true
	case '+':
		return tokPlus, true
	case '-':
		return tokMinus, true
	case '*':
		return tokTimes, true
	case '/':
		return tokDivide, true
	case '!':
		return tokExclam, true
	case '~':
		return tokInvert, true
	}
	return tokEOF, false
}

func (l *lexer) numberToken() (token, error) {
	rest := l.src[l.pos:]
	if strings.HasPrefix(rest, "0x") {
		end := 2
		for end < len(rest) && isHex(rest[end]) {
			end++
		}
		if end == 2 {
			return token{}, l.errorf("malformed number literal")
		}
		v, err := strconv.ParseInt(rest[2:end], 16, 64)
		if err != nil {
			return token{}, l.errorf("malformed number literal %q: %w", rest[:end], err)
		}
		l.pos += end
		return token{kind: tokInteger, num: v, line: l.line}, nil
	}
	end := 0
	for end < len(rest) && isDigit(rest[end]) {
		end++
	}
	v, err := strconv.ParseInt(rest[:end], 10, 64)
	if err != nil {
		return token{}, l.errorf("malformed number literal %q: %w", rest[:end], err)
	}
	kind := tokInteger
	if end == 1 {
		kind = tokDigit
	}
	if end < len(rest) && rest[end] == '.' {
		end++
		for end < len(rest) && isDigit(rest[end]) {
			end++
		}
		kind = tokFloat
	}
	l.pos += end
	return token{kind: kind, num: v, line: l.line}, nil
}

func isHex(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func (l *lexer) keyNameToken() (token, error) {
	start := l.pos + 1
	end := start
	for end < len(l.src) && l.src[end] > ' ' && l.src[end] < 0x7f && l.src[end] != '>' {
		end++
	}
	if end >= len(l.src) || l.src[end] != '>' {
		return token{}, l.errorf("unterminated key name literal")
	}
	l.pos = end + 1
	return token{kind: tokKeyName, text: l.src[start:end], line: l.line}, nil
}

func (l *lexer) stringToken() (token, error) {
	l.pos++
	var b strings.Builder
	for l.pos < len(l.src) && l.src[l.pos] != '\n' && l.src[l.pos] != '"' {
		c := l.src[l.pos]
		l.pos++
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		l.escape(&b)
	}
	if l.pos >= len(l.src) || l.src[l.pos] != '"' {
		return token{}, l.errorf("unterminated string literal")
	}
	l.pos++
	return token{kind: tokString, text: b.String(), line: l.line}, nil
}

func simpleEscape(c byte) (byte, bool) {
	switch c {
	case '\\', '"':
		return c, true
	case 'n':
		return '\n', true
	case 't':
		return '\t', true
	case 'r':
		return '\r', true
	case 'b':
		return '\b', true
	case 'f':
		return '\f', true
	case 'v':
		return '\v', true
	case 'e':
		return 0x1b, true
	}
	return 0, false
}

func (l *lexer) escape(b *strings.Builder) {
	c := l.peekByte()
	if out, ok := simpleEscape(c); ok {
		l.pos++
		b.WriteByte(out)
		return
	}
	if c == 'u' {
		l.pos++
		l.unicodeEscape(b)
		return
	}
	if v, n := octal(l.src[l.pos:]); n > 0 {
		l.pos += n
		if v > 0 {
			b.WriteByte(v)
		} else {
			l.ctx.warn(193, "%s:%d: invalid octal escape sequence in string literal\n", l.name, l.line)
		}
		return
	}
	l.ctx.warn(645, "%s:%d: unknown escape sequence \"\\%c\" in string literal\n", l.name, l.line, c)
}

func (l *lexer) unicodeEscape(b *strings.Builder) {
	if !l.tryUnicodeEscape(b) {
		l.ctx.warn(607, "%s:%d: invalid Unicode escape sequence in string literal\n", l.name, l.line)
	}
}

func (l *lexer) tryUnicodeEscape(b *strings.Builder) bool {
	rest := l.src[l.pos:]
	if !strings.HasPrefix(rest, "{") {
		return false
	}
	end := strings.IndexByte(rest, '}')
	if end < 2 {
		return false
	}
	v, err := strconv.ParseUint(rest[1:end], 16, 32)
	if err != nil {
		return false
	}
	l.pos += end + 1
	if r := rune(v); v > 0 && utf8.ValidRune(r) { //nolint:gosec // ValidRune checks it
		b.WriteRune(r)
		return true
	}
	return false
}

func octal(s string) (byte, int) {
	v, n := 0, 0
	overflow := false
	for n < 4 && n < len(s) && s[n] >= '0' && s[n] <= '7' {
		if v >= 0o40 {
			overflow = true
		} else {
			v = v*8 + int(s[n]-'0')
		}
		n++
	}
	if overflow {
		return 0, n
	}
	return byte(v), n
}
