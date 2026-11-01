package xkb

import (
	"encoding/binary"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const invalidComposeLocale = 679

// ComposeEntry is one compose sequence of a table.
type ComposeEntry struct {
	Sequence []Keysym
	Sym      Keysym
	UTF8     string
}

// Entries yields every sequence of the table, ordered by keysym at each step
// of the sequence. An entry's Sequence is reused between steps: clone it to
// keep it.
func (t *ComposeTable) Entries() iter.Seq[ComposeEntry] {
	return func(yield func(ComposeEntry) bool) {
		walkCompose(t.root, nil, yield)
	}
}

func walkCompose(n *composeNode, seq []Keysym, yield func(ComposeEntry) bool) bool {
	syms := make([]Keysym, 0, len(n.children))
	for sym := range n.children {
		syms = append(syms, sym)
	}
	slices.Sort(syms)
	for _, sym := range syms {
		child := n.children[sym]
		next := append(seq, sym)
		if child.leaf {
			if !yield(ComposeEntry{Sequence: next, Sym: child.sym, UTF8: child.text}) {
				return false
			}
			continue
		}
		if !walkCompose(child, next, yield) {
			return false
		}
	}
	return true
}

// Table is the table the state runs on.
func (s *ComposeState) Table() *ComposeTable { return s.table }

const (
	localeArchiveMagic = 0xde020109
	archiveHeaderSize  = 56
)

// localeInstalled reports what glibc's newlocale would: whether a locale's
// data is on the system, in $LOCPATH, the locale archive or a directory of
// its own. musl accepts every name.
func localeInstalled(getenv func(string) string, locale string) bool {
	if locale == "C" || locale == "POSIX" {
		return true
	}
	if musl, _ := filepath.Glob("/lib/ld-musl-*.so.1"); len(musl) > 0 {
		return true
	}
	names := localeNames(locale)
	dirs := filepath.SplitList(getenv("LOCPATH"))
	if len(dirs) == 0 {
		dirs = []string{"/usr/lib/locale"}
		archive := getenv("LOCALE_ARCHIVE")
		if archive == "" {
			archive = "/usr/lib/locale/locale-archive"
		}
		if archiveHasLocale(archive, names) {
			return true
		}
	}
	for _, dir := range dirs {
		for _, name := range names {
			if _, err := os.Stat(filepath.Join(dir, name, "LC_CTYPE")); err == nil {
				return true
			}
		}
	}
	return false
}

// localeNames is a locale's spelling as given and with the codeset normalized
// the way glibc does: lower case alphanumerics, with "iso" before a codeset of
// only digits.
func localeNames(locale string) []string {
	base, modifier, hasModifier := strings.Cut(locale, "@")
	lang, codeset, hasCodeset := strings.Cut(base, ".")
	if !hasCodeset {
		return []string{locale}
	}
	var b strings.Builder
	onlyDigits := true
	for _, c := range strings.ToLower(codeset) {
		switch {
		case c >= '0' && c <= '9':
			b.WriteRune(c)
		case c >= 'a' && c <= 'z':
			onlyDigits = false
			b.WriteRune(c)
		}
	}
	normal := b.String()
	if onlyDigits {
		normal = "iso" + normal
	}
	name := lang + "." + normal
	if hasModifier {
		name += "@" + modifier
	}
	return []string{locale, name}
}

func archiveHasLocale(path string, names []string) bool {
	src, err := os.ReadFile(path) //nolint:gosec // the system's locale archive
	if err != nil || len(src) < archiveHeaderSize || binary.NativeEndian.Uint32(src) != localeArchiveMagic {
		return false
	}
	u32 := func(off uint32) uint32 {
		if uint64(off)+4 > uint64(len(src)) {
			return 0
		}
		return binary.NativeEndian.Uint32(src[off:])
	}
	hashOff, hashSize := u32(24), u32(32)
	for i := range hashSize {
		entry := hashOff + i*12
		nameOff, rec := u32(entry+4), u32(entry+8)
		if rec == 0 || uint64(nameOff) >= uint64(len(src)) {
			continue
		}
		end := slices.Index(src[nameOff:], 0)
		if end >= 0 && slices.Contains(names, string(src[nameOff:int(nameOff)+end])) {
			return true
		}
	}
	return false
}
