package xkb

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

//go:generate go run ./internal/genkeysyms -out keysym_gen.go

// Keysym is a key's symbolic meaning.
type Keysym uint32

// Keycode is an xkb keycode: an evdev code plus 8.
type Keycode uint32

// ModMask is a set of real modifiers, by bit.
type ModMask uint32

// The real modifiers.
const (
	ModShift   ModMask = 1 << 0
	ModLock    ModMask = 1 << 1
	ModControl ModMask = 1 << 2
	ModMod1    ModMask = 1 << 3
	ModMod2    ModMask = 1 << 4
	ModMod3    ModMask = 1 << 5
	ModMod4    ModMask = 1 << 6
	ModMod5    ModMask = 1 << 7
)

// KeysymFlags changes how KeysymFromName matches.
type KeysymFlags uint8

const (
	// KeysymNoFlags matches a name exactly.
	KeysymNoFlags KeysymFlags = 0
	// KeysymNameCaseInsensitive matches a name in any case, preferring
	// the lower case keysym when both exist.
	KeysymNameCaseInsensitive KeysymFlags = 1
)

const (
	keysymMax          = 0x1fffffff
	keysymUnicode      = 0x01000000
	keysymUnicodeMax   = 0x0110ffff
	keysymUnicodeNamed = 0x01000100
	surrogateMin       = 0xd800
	surrogateMax       = 0xdfff
)

type keysymName struct {
	name string
	sym  Keysym
}

type keysymRune struct {
	sym Keysym
	r   rune
}

type runeKeysym struct {
	r   rune
	sym Keysym
}

type caseMapping struct {
	sym, upper, lower Keysym
	flags             uint8
}

const (
	caseFlagLower uint8 = 1 << iota
	caseFlagUpper
)

func findName(table []keysymName, name string) (Keysym, bool) {
	i := sort.Search(len(table), func(i int) bool { return table[i].name >= name })
	if i < len(table) && table[i].name == name {
		return table[i].sym, true
	}
	return KeyNoSymbol, false
}

func asciiLower(s string) string {
	for i := range len(s) {
		if s[i] >= 'A' && s[i] <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

func parseKeysymHex(s string) (uint32, bool) {
	if s == "" || len(s) > 8 {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil || strings.HasPrefix(s, "+") {
		return 0, false
	}
	return uint32(v), true
}

// KeysymFromName is the keysym of a name, a U<hex> code point, a 0x<hex>
// value or an XF86_ spelling; KeyNoSymbol when it is none of these.
func KeysymFromName(name string, flags KeysymFlags) Keysym {
	icase := flags&KeysymNameCaseInsensitive != 0
	if icase {
		if sym, ok := findName(keysymFolded, asciiLower(name)); ok {
			return sym
		}
	} else if sym, ok := findName(keysymNames, name); ok {
		return sym
	}
	if name == "" {
		return KeyNoSymbol
	}
	if name[0] == 'U' || (icase && name[0] == 'u') {
		v, ok := parseKeysymHex(name[1:])
		if !ok {
			return KeyNoSymbol
		}
		if v > 0xff && v <= 0x10ffff {
			return Keysym(keysymUnicode + v)
		}
		return UTF32ToKeysym(rune(v)) //nolint:gosec // above MaxRune stays invalid, which gives no keysym
	}
	if strings.HasPrefix(name, "0x") || (icase && strings.HasPrefix(name, "0X")) {
		v, ok := parseKeysymHex(name[2:])
		if !ok || v > keysymMax {
			return KeyNoSymbol
		}
		return Keysym(v)
	}
	if strings.HasPrefix(name, "XF86_") || (icase && strings.EqualFold(name[:min(5, len(name))], "XF86_")) {
		return KeysymFromName(name[:4]+name[5:], flags)
	}
	return KeyNoSymbol
}

// KeysymGetName is a keysym's canonical name: its X11 name, U<hex> for a
// Unicode one, or 0x<hex>; "Invalid" past the keysym range.
func KeysymGetName(sym Keysym) string {
	if sym > keysymMax {
		return "Invalid"
	}
	if sym == KeyNoSymbol {
		return "NoSymbol"
	}
	i := sort.Search(len(keysymCanonical), func(i int) bool { return keysymCanonical[i].sym >= sym })
	if i < len(keysymCanonical) && keysymCanonical[i].sym == sym {
		return keysymCanonical[i].name
	}
	if sym >= keysymUnicodeNamed && sym <= keysymUnicodeMax {
		return fmt.Sprintf("U%04X", uint32(sym)&0xffffff)
	}
	return fmt.Sprintf("0x%08x", uint32(sym))
}

// KeysymToUTF32 is the character a keysym types, 0 when it types none.
func KeysymToUTF32(sym Keysym) rune {
	if sym >= keysymUnicode && sym <= keysymUnicodeMax {
		r := rune(sym - keysymUnicode)
		if r >= surrogateMin && r <= surrogateMax {
			return 0
		}
		return r
	}
	i := sort.Search(len(keysymToUCS), func(i int) bool { return keysymToUCS[i].sym >= sym })
	if i < len(keysymToUCS) && keysymToUCS[i].sym == sym {
		return keysymToUCS[i].r
	}
	return 0
}

// KeysymToUTF8 is the text a keysym types, "" when it types none.
func KeysymToUTF8(sym Keysym) string {
	r := KeysymToUTF32(sym)
	if r == 0 {
		return ""
	}
	return string(r)
}

// UTF32ToKeysym is the keysym that types a character, KeyNoSymbol for NUL
// and for what is not a Unicode scalar value.
func UTF32ToKeysym(r rune) Keysym {
	if r <= 0 || r > utf8.MaxRune || (r >= surrogateMin && r <= surrogateMax) {
		return KeyNoSymbol
	}
	i := sort.Search(len(ucsToKeysym), func(i int) bool { return ucsToKeysym[i].r >= r })
	if i < len(ucsToKeysym) && ucsToKeysym[i].r == r {
		return ucsToKeysym[i].sym
	}
	return Keysym(keysymUnicode + uint32(r))
}

func caseOf(sym Keysym) (caseMapping, bool) {
	i := sort.Search(len(keysymCase), func(i int) bool { return keysymCase[i].sym >= sym })
	if i < len(keysymCase) && keysymCase[i].sym == sym {
		return keysymCase[i], true
	}
	return caseMapping{}, false
}

// KeysymToUpper is a keysym's upper case form, itself when it has none.
func KeysymToUpper(sym Keysym) Keysym {
	if c, ok := caseOf(sym); ok {
		return c.upper
	}
	return sym
}

// KeysymToLower is a keysym's lower case form, itself when it has none.
func KeysymToLower(sym Keysym) Keysym {
	if c, ok := caseOf(sym); ok {
		return c.lower
	}
	return sym
}

func keysymIsLower(sym Keysym) bool {
	c, ok := caseOf(sym)
	return ok && c.flags&caseFlagLower != 0
}

func keysymIsUpperOrTitle(sym Keysym) bool {
	c, ok := caseOf(sym)
	return ok && c.flags&caseFlagUpper != 0
}

// KeysymIsKeypad reports whether a keysym is on the numeric keypad.
func KeysymIsKeypad(sym Keysym) bool { return sym >= KeyKPSpace && sym <= KeyKPEqual }

// KeysymIsModifier reports whether a keysym is a modifier key's.
func KeysymIsModifier(sym Keysym) bool {
	return (sym >= KeyShiftL && sym <= KeyHyperR) ||
		(sym >= KeyISOLock && sym <= KeyISOLevel5Lock) ||
		sym == KeyModeSwitch || sym == KeyNumLock
}

type deprecatedName struct{ name, reference string }

func checkDeprecatedName(name string) (deprecated bool, reference string) {
	i := sort.Search(len(keysymDeprecatedNames), func(i int) bool { return keysymDeprecatedNames[i].name >= name })
	if i < len(keysymDeprecatedNames) && keysymDeprecatedNames[i].name == name {
		return true, keysymDeprecatedNames[i].reference
	}
	return false, ""
}

func checkDeprecatedValue(sym Keysym) bool {
	_, ok := slices.BinarySearch(keysymDeprecatedValues, sym)
	return ok
}
