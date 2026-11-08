// Package i18n is gettext in pure Go: GNU .mo and .po catalogs with
// plural forms and message contexts, locale resolution from the
// environment, and catalog lookup in a file system. A Catalog plugs
// into lys-ui's widget.SetMessageCatalog, so a toolkit's built-in strings
// and an app's own translate through one table.
//
//	cat, err := i18n.LoadSystem("myapp", i18n.Locales())
//	label := cat.Get("Open")
package i18n

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const contextSeparator = "\x04"

// Catalog is one language's translations. The zero value translates
// nothing: every lookup returns its English input.
type Catalog struct {
	messages map[string][]string
	nplurals int
	plural   pluralFunc
}

// Get translates id, or returns id untranslated.
func (c *Catalog) Get(id string) string { return c.lookup(id, id, id, 1, false) }

// GetCtx translates id within ctx (msgctxt), or returns id.
func (c *Catalog) GetCtx(ctx, id string) string {
	return c.lookup(ctx+contextSeparator+id, id, id, 1, false)
}

// GetN translates the plural pair for n, choosing the form by the
// catalog's Plural-Forms rule, or returns singular when n is 1 and
// plural otherwise.
func (c *Catalog) GetN(singular, plural string, n int) string {
	return c.lookup(singular, singular, plural, n, true)
}

// GetNCtx is GetN within ctx.
func (c *Catalog) GetNCtx(ctx, singular, plural string, n int) string {
	return c.lookup(ctx+contextSeparator+singular, singular, plural, n, true)
}

func (c *Catalog) lookup(key, singular, plural string, n int, plurals bool) string {
	fallback := singular
	if plurals && n != 1 {
		fallback = plural
	}
	if c == nil || c.messages == nil {
		return fallback
	}
	forms, ok := c.messages[key]
	if !ok {
		return fallback
	}
	i := uint64(0)
	if plurals {
		i = c.plural(uint64(max(n, 0)))
	}
	if i >= uint64(len(forms)) || forms[i] == "" {
		return fallback
	}
	return forms[i]
}

// FromMap is a catalog from a plain English-to-translation map, for
// apps that keep a handful of strings in code. It has no plural forms
// or contexts.
func FromMap(m map[string]string) *Catalog {
	entries := make(map[string][]string, len(m))
	for k, v := range m {
		entries[k] = []string{v}
	}
	return &Catalog{messages: entries, nplurals: 2, plural: germanic}
}

func newCatalog(entries map[string][]string) (*Catalog, error) {
	c := &Catalog{messages: entries}
	header := ""
	if h, ok := entries[""]; ok && len(h) > 0 {
		header = h[0]
		delete(entries, "")
	}
	forms := ""
	for line := range strings.SplitSeq(header, "\n") {
		if v, ok := strings.CutPrefix(line, "Plural-Forms:"); ok {
			forms = strings.TrimSpace(v)
		}
	}
	nplurals, plural, err := parsePluralForms(forms)
	if err != nil {
		return nil, err
	}
	c.nplurals, c.plural = nplurals, plural
	return c, nil
}

// ParseMO parses a GNU .mo catalog, either byte order.
func ParseMO(data []byte) (*Catalog, error) {
	if len(data) < 28 {
		return nil, errors.New("i18n: .mo file shorter than its header")
	}
	var order binary.ByteOrder
	switch binary.LittleEndian.Uint32(data) {
	case 0x950412de:
		order = binary.LittleEndian
	case 0xde120495:
		order = binary.BigEndian
	default:
		return nil, errors.New("i18n: not a .mo file (bad magic)")
	}
	count := int(order.Uint32(data[8:]))
	origins, translations := int(order.Uint32(data[12:])), int(order.Uint32(data[16:]))
	str := func(table, i int) (string, error) {
		at := table + 8*i
		if at < 0 || at+8 > len(data) {
			return "", fmt.Errorf("i18n: .mo string table entry %d out of range", i)
		}
		length, offset := int(order.Uint32(data[at:])), int(order.Uint32(data[at+4:]))
		if offset < 0 || length < 0 || offset+length > len(data) {
			return "", fmt.Errorf("i18n: .mo string %d out of range", i)
		}
		return string(data[offset : offset+length]), nil
	}
	entries := make(map[string][]string, count)
	for i := range count {
		id, err := str(origins, i)
		if err != nil {
			return nil, err
		}
		tr, err := str(translations, i)
		if err != nil {
			return nil, err
		}
		singular, _, _ := strings.Cut(id, "\x00")
		entries[singular] = strings.Split(tr, "\x00")
	}
	return newCatalog(entries)
}

// ParsePO parses a .po catalog. Fuzzy entries are skipped, as msgfmt
// skips them; the header is kept for its Plural-Forms.
func ParsePO(data []byte) (*Catalog, error) {
	entries := map[string][]string{}
	var (
		ctx, id    string
		hasCtx     bool
		ctxPending bool
		forms      []string
		fuzzy      bool
		target     *string
		inEntry    bool
		lineNo     int
		nextFuzzy  bool
	)
	flush := func() {
		if inEntry && (!fuzzy || id == "") {
			key := id
			if hasCtx {
				key = ctx + contextSeparator + id
			}
			entries[key] = forms
		}
		ctx, id, hasCtx, forms, fuzzy, target, inEntry = "", "", false, nil, false, nil, false
	}
	for line := range strings.SplitSeq(string(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))), "\n") {
		lineNo++
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#,"):
			if strings.Contains(line, "fuzzy") {
				nextFuzzy = true
			}
			continue
		case strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, `"`):
			if target == nil {
				return nil, fmt.Errorf("i18n: .po line %d: a string continues nothing", lineNo)
			}
			s, err := unquotePO(line)
			if err != nil {
				return nil, fmt.Errorf("i18n: .po line %d: %w", lineNo, err)
			}
			*target += s
			continue
		}
		keyword, rest, _ := strings.Cut(line, " ")
		value, err := unquotePO(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("i18n: .po line %d: %w", lineNo, err)
		}
		switch {
		case keyword == "msgctxt":
			flush()
			ctx, hasCtx, ctxPending, inEntry, fuzzy, nextFuzzy = value, true, true, true, nextFuzzy, false
			target = &ctx
		case keyword == "msgid":
			if !ctxPending {
				flush()
				fuzzy, nextFuzzy = nextFuzzy, false
			}
			ctxPending = false
			inEntry = true
			id = value
			target = &id
		case keyword == "msgid_plural":
			plural := value
			target = &plural
		case keyword == "msgstr":
			forms = []string{value}
			target = &forms[0]
		case strings.HasPrefix(keyword, "msgstr["):
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(keyword, "msgstr["), "]"))
			if err != nil || n != len(forms) {
				return nil, fmt.Errorf("i18n: .po line %d: bad plural index %s", lineNo, keyword)
			}
			forms = append(forms, value)
			target = &forms[n]
		default:
			return nil, fmt.Errorf("i18n: .po line %d: unknown keyword %q", lineNo, keyword)
		}
	}
	flush()
	return newCatalog(entries)
}

func unquotePO(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", fmt.Errorf("expected a quoted string, got %q", s)
	}
	return strconv.Unquote(s)
}

// Locales returns the locale candidates gettext would try, most
// specific first, from LANGUAGE, LC_ALL, LC_MESSAGES and LANG: for
// "nb_NO.UTF-8@euro" that is nb_NO@euro, nb_NO, nb@euro, nb. The C and
// POSIX locales mean no translation.
func Locales() []string {
	var names []string
	if lang := os.Getenv("LANGUAGE"); lang != "" {
		names = strings.Split(lang, ":")
	}
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if s := os.Getenv(v); s != "" {
			names = append(names, s)
			break
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, name := range names {
		for _, c := range expand(name) {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

func expand(name string) []string {
	if name == "" || name == "C" || name == "POSIX" || strings.HasPrefix(name, "C.") {
		return nil
	}
	rest, modifier, _ := strings.Cut(name, "@")
	rest, _, _ = strings.Cut(rest, ".")
	lang, territory, hasTerritory := strings.Cut(rest, "_")
	var out []string
	add := func(s string) {
		if modifier != "" {
			out = append(out, s+"@"+modifier)
		}
		out = append(out, s)
	}
	if hasTerritory {
		add(lang + "_" + territory)
	}
	add(lang)
	return out
}

// Load finds domain's catalog in fsys for the first locale that has
// one, as <locale>/LC_MESSAGES/<domain>.mo or .po. An empty catalog
// (the identity) and no error mean no locale has one; a catalog that
// exists but does not parse is an error.
func Load(fsys fs.FS, domain string, locales []string) (*Catalog, error) {
	for _, locale := range locales {
		for _, ext := range []string{".mo", ".po"} {
			name := locale + "/LC_MESSAGES/" + domain + ext
			data, err := fs.ReadFile(fsys, name)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("i18n: reading %s: %w", name, err)
			}
			parse := ParseMO
			if ext == ".po" {
				parse = ParsePO
			}
			c, err := parse(data)
			if err != nil {
				return nil, fmt.Errorf("i18n: %s: %w", name, err)
			}
			return c, nil
		}
	}
	return &Catalog{}, nil
}

// LoadSystem is Load over the system's locale directories: each
// XDG data dir's locale directory (XDG_DATA_HOME, then XDG_DATA_DIRS,
// defaulting to /usr/local/share and /usr/share), first match wins.
func LoadSystem(domain string, locales []string) (*Catalog, error) {
	for _, dir := range dataDirs() {
		root := filepath.Join(dir, "locale")
		c, err := Load(os.DirFS(root), domain, locales)
		if err != nil {
			return nil, err
		}
		if c.messages != nil {
			return c, nil
		}
	}
	return &Catalog{}, nil
}

func dataDirs() []string {
	var dirs []string
	if home := os.Getenv("XDG_DATA_HOME"); home != "" {
		dirs = append(dirs, home)
	} else if h, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(h, ".local", "share"))
	}
	system := os.Getenv("XDG_DATA_DIRS")
	if system == "" {
		system = "/usr/local/share:/usr/share"
	}
	return append(dirs, filepath.SplitList(system)...)
}
