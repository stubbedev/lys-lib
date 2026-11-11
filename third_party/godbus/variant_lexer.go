package dbus

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Heavily inspired by the lexer from text/template.

type varToken struct {
	typ varTokenType
	val string
}

type varTokenType byte

const (
	tokEOF varTokenType = iota
	tokError
	tokNumber
	tokString
	tokBool
	tokArrayStart
	tokArrayEnd
	tokDictStart
	tokDictEnd
	tokVariantStart
	tokVariantEnd
	tokComma
	tokColon
	tokType
	tokByteString
)

type varLexer struct {
	input  string
	start  int
	pos    int
	width  int
	tokens []varToken
}

type lexState func(*varLexer) lexState

func varLex(s string) []varToken {
	l := &varLexer{input: s}
	l.run()
	return l.tokens
}

func (l *varLexer) accept(valid string) bool {
	if strings.ContainsRune(valid, l.next()) {
		return true
	}
	l.backup()
	return false
}

func (l *varLexer) backup() {
	l.pos -= l.width
}

func (l *varLexer) emit(t varTokenType) {
	l.tokens = append(l.tokens, varToken{t, l.input[l.start:l.pos]})
	l.start = l.pos
}

func (l *varLexer) errorf(format string, v ...any) lexState {
	l.tokens = append(l.tokens, varToken{
		tokError,
		fmt.Sprintf(format, v...),
	})
	return nil
}

func (l *varLexer) ignore() {
	l.start = l.pos
}

func (l *varLexer) next() rune {
	var r rune

	if l.pos >= len(l.input) {
		l.width = 0
		return -1
	}
	r, l.width = utf8.DecodeRuneInString(l.input[l.pos:])
	l.pos += l.width
	return r
}

func (l *varLexer) run() {
	for state := varLexNormal; state != nil; {
		state = state(l)
	}
}

func (l *varLexer) peek() rune {
	r := l.next()
	l.backup()
	return r
}

func varLexNormal(l *varLexer) lexState {
	for {
		r := l.next()
		switch {
		case r == -1:
			l.emit(tokEOF)
			return nil
		case r == '[':
			l.emit(tokArrayStart)
		case r == ']':
			l.emit(tokArrayEnd)
		case r == '{':
			l.emit(tokDictStart)
		case r == '}':
			l.emit(tokDictEnd)
		case r == '<':
			l.emit(tokVariantStart)
		case r == '>':
			l.emit(tokVariantEnd)
		case r == ':':
			l.emit(tokColon)
		case r == ',':
			l.emit(tokComma)
		case r == '\'' || r == '"':
			l.backup()
			return varLexString
		case r == '@':
			l.backup()
			return varLexType
		case unicode.IsSpace(r):
			l.ignore()
		case unicode.IsNumber(r) || r == '+' || r == '-':
			l.backup()
			return varLexNumber
		case r == 'b':
			pos := l.start
			if n := l.peek(); n == '"' || n == '\'' {
				return varLexByteString
			}
			// not a byte string; try to parse it as a type or bool below
			l.pos = pos + 1
			l.width = 1
			fallthrough
		default:
			// either a bool or a type. Try bools first.
			l.backup()
			if l.pos+4 <= len(l.input) {
				if l.input[l.pos:l.pos+4] == "true" {
					l.pos += 4
					l.emit(tokBool)
					continue
				}
			}
			if l.pos+5 <= len(l.input) {
				if l.input[l.pos:l.pos+5] == "false" {
					l.pos += 5
					l.emit(tokBool)
					continue
				}
			}
			// must be a type.
			return varLexType
		}
	}
}

// varTypeSig is the signature of the type a variant text names (lys-lib
// patch: a switch, where upstream built a map when the package was
// imported).
func varTypeSig(name string) (string, bool) {
	switch name {
	case "boolean":
		return "b", true
	case "byte":
		return "y", true
	case "int16":
		return "n", true
	case "uint16":
		return "q", true
	case "int32":
		return "i", true
	case "uint32":
		return "u", true
	case "int64":
		return "x", true
	case "uint64":
		return "t", true
	case "double":
		return "f", true
	case "string":
		return "s", true
	case "objectpath":
		return "o", true
	case "signature":
		return "g", true
	}
	return "", false
}

func varLexByteString(l *varLexer) lexState {
	q := l.next()
Loop:
	for {
		switch l.next() {
		case '\\':
			if r := l.next(); r != -1 {
				break
			}
			fallthrough
		case -1:
			return l.errorf("unterminated bytestring")
		case q:
			break Loop
		}
	}
	l.emit(tokByteString)
	return varLexNormal
}

func varLexNumber(l *varLexer) lexState {
	l.accept("+-")
	digits := "0123456789"
	if l.accept("0") {
		if l.accept("x") {
			digits = "0123456789abcdefABCDEF"
		} else {
			digits = "01234567"
		}
	}
	for strings.ContainsRune(digits, l.next()) {
	}
	l.backup()
	if l.accept(".") {
		for strings.ContainsRune(digits, l.next()) {
		}
		l.backup()
	}
	if l.accept("eE") {
		l.accept("+-")
		for strings.ContainsRune("0123456789", l.next()) {
		}
		l.backup()
	}
	if r := l.peek(); unicode.IsLetter(r) {
		l.next()
		return l.errorf("bad number syntax: %q", l.input[l.start:l.pos])
	}
	l.emit(tokNumber)
	return varLexNormal
}

func varLexString(l *varLexer) lexState {
	q := l.next()
Loop:
	for {
		switch l.next() {
		case '\\':
			if r := l.next(); r != -1 {
				break
			}
			fallthrough
		case -1:
			return l.errorf("unterminated string")
		case q:
			break Loop
		}
	}
	l.emit(tokString)
	return varLexNormal
}

func varLexType(l *varLexer) lexState {
	at := l.accept("@")
	for {
		r := l.next()
		if r == -1 {
			break
		}
		if unicode.IsSpace(r) {
			l.backup()
			break
		}
	}
	if at {
		if _, err := ParseSignature(l.input[l.start+1 : l.pos]); err != nil {
			return l.errorf("%s", err)
		}
	} else {
		if _, ok := varTypeSig(l.input[l.start:l.pos]); ok {
			l.emit(tokType)
			return varLexNormal
		}
		return l.errorf("unrecognized type %q", l.input[l.start:l.pos])
	}
	l.emit(tokType)
	return varLexNormal
}
