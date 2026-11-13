package xkb

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	composeMaxLHS     = 10
	composeMaxInclude = 5
	composeMaxString  = 256
	composeMaxErrors  = 10
)

var xlocaleCandidates = []string{"/usr/share/X11/locale", "/run/current-system/sw/share/X11/locale", "/usr/local/share/X11/locale"}

// defaultXLocaleDir is the libX11 locale directory a distribution build
// bakes in with -X; empty, the usual locations are searched.
var defaultXLocaleDir string

type composeNode struct {
	children map[Keysym]*composeNode
	leaf     bool
	sym      Keysym
	text     string
}

// ComposeTable is an immutable set of compose sequences, safe for
// concurrent use.
type ComposeTable struct {
	root *composeNode
}

func xlocaleDir(getenv func(string) string) string {
	if dir := getenv("XLOCALEDIR"); dir != "" {
		return dir
	}
	if defaultXLocaleDir != "" {
		return defaultXLocaleDir
	}
	for _, dir := range xlocaleCandidates {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

func resolveLocaleFile(dir, file, name string, rightToLeft bool) string {
	src, err := os.ReadFile(filepath.Join(dir, file)) //nolint:gosec // a locale file under the X locale directory
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(src), "\n") {
		trimmed := strings.TrimLeft(line, " \t\r\f\v")
		if trimmed == "" || trimmed[0] == '#' {
			continue
		}
		end := strings.IndexAny(trimmed, " \t\r\f\v:")
		if end < 0 {
			continue
		}
		left := trimmed[:end]
		right := strings.TrimLeft(strings.TrimPrefix(trimmed[end:], ":"), " \t\r\f\v:")
		if i := strings.IndexAny(right, " \t\r\f\v"); i >= 0 {
			right = right[:i]
		}
		if rightToLeft && right == name {
			return left
		}
		if !rightToLeft && left == name {
			return right
		}
	}
	return ""
}

func (c *Context) localeComposePath(dir, locale string) string {
	const fallback = "en_US.UTF-8"
	if locale == "C" {
		locale = fallback
	}
	resolved := resolveLocaleFile(dir, "compose.dir", locale, true)
	if resolved == "" {
		installed, err := c.localeInstalled(locale)
		if err != nil {
			c.log(LogError, 0, invalidComposeLocale, "No Compose file for locale \"%s\": %v\n", locale, err)
			return ""
		}
		if !installed {
			c.log(LogError, 0, invalidComposeLocale, "No Compose file for locale \"%s\": locale is either invalid or not installed\n", locale)
			return ""
		}
		resolved = resolveLocaleFile(dir, "compose.dir", fallback, true)
		if resolved == "" {
			c.log(LogError, 0, invalidComposeLocale, "No Compose file for locale \"%s\": failed to use fallback \"%s\"\n", locale, fallback)
			return ""
		}
		c.log(LogWarning, 0, invalidComposeLocale, "No Compose file for locale \"%s\": using locale fallback \"%s\"\n", locale, fallback)
	}
	if resolved == "" || filepath.IsAbs(resolved) {
		return resolved
	}
	return filepath.Join(dir, resolved)
}

func composeLocale(getenv func(string) string, locale string) string {
	if locale != "" {
		return locale
	}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(name); v != "" {
			return v
		}
	}
	return "C"
}

// NewComposeTableFromLocale finds and parses the active Compose file the
// way libxkbcommon does: $XCOMPOSEFILE, $XDG_CONFIG_HOME/XCompose or
// ~/.config/XCompose, ~/.XCompose, then the locale's system file, with
// en_US.UTF-8 standing in for a locale that has none. An empty locale
// reads LC_ALL, LC_CTYPE and LANG.
func (c *Context) NewComposeTableFromLocale(locale string) (*ComposeTable, error) {
	locale = composeLocale(c.getenv, locale)
	dir := xlocaleDir(c.getenv)
	if dir != "" {
		if alias := resolveLocaleFile(dir, "locale.alias", locale, false); alias != "" {
			locale = alias
		}
	}
	var candidates []string
	if p := c.getenv("XCOMPOSEFILE"); p != "" {
		candidates = append(candidates, p)
	}
	if xdg := c.getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		candidates = append(candidates, filepath.Join(xdg, "XCompose"))
	} else if c.getenv("HOME") != "" {
		candidates = append(candidates, filepath.Join(c.getenv("HOME"), ".config", "XCompose"))
	}
	if c.getenv("HOME") != "" {
		candidates = append(candidates, filepath.Join(c.getenv("HOME"), ".XCompose"))
	}
	if dir != "" {
		if p := c.localeComposePath(dir, locale); p != "" {
			candidates = append(candidates, p)
		}
	}
	for _, path := range candidates {
		src, err := os.ReadFile(path) //nolint:gosec // a Compose file the user or the locale names
		if err != nil {
			continue
		}
		return c.parseCompose(path, src, locale)
	}
	c.log(LogError, 0, invalidComposeLocale, "couldn't find a Compose file for locale \"%s\"\n", locale)
	return nil, fmt.Errorf("xkb: no Compose file for locale %q in %v", locale, candidates)
}

// NewComposeTableFromFile parses one Compose file.
func (c *Context) NewComposeTableFromFile(path, locale string) (*ComposeTable, error) {
	src, err := os.ReadFile(path) //nolint:gosec // the file the caller names
	if err != nil {
		return nil, fmt.Errorf("xkb: read Compose file: %w", err)
	}
	return c.parseCompose(path, src, composeLocale(c.getenv, locale))
}

// NewComposeTableFromReader parses Compose file text read from r.
func (c *Context) NewComposeTableFromReader(r io.Reader, locale string) (*ComposeTable, error) {
	src, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("xkb: read Compose file: %w", err)
	}
	return c.parseCompose("(unknown file)", src, composeLocale(c.getenv, locale))
}

// NewComposeTableFromBuffer parses Compose file text.
func (c *Context) NewComposeTableFromBuffer(text []byte, locale string) (*ComposeTable, error) {
	return c.parseCompose("(input string)", text, composeLocale(c.getenv, locale))
}

func (c *Context) parseCompose(name string, src []byte, locale string) (*ComposeTable, error) {
	t := &ComposeTable{root: &composeNode{}}
	p := &composeParser{ctx: c, table: t, locale: locale}
	if err := p.parse(name, src, 0); err != nil {
		return nil, fmt.Errorf("xkb: parse Compose file: %w", err)
	}
	return t, nil
}

type composeTok uint8

const (
	ctEOF composeTok = iota
	ctEOL
	ctKeysym
	ctColon
	ctBang
	ctTilde
	ctString
	ctIdent
	ctInclude
)

type composeLexer struct {
	name string
	src  string
	pos  int
	text string
}

func (l *composeLexer) next() (composeTok, error) {
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; c {
		case '\n':
			l.pos++
			return ctEOL, nil
		case ' ', '\t', '\r', '\f', '\v':
			l.pos++
			continue
		case '#':
			end := strings.IndexByte(l.src[l.pos:], '\n')
			if end < 0 {
				l.pos = len(l.src)
			} else {
				l.pos += end
			}
			continue
		}
		break
	}
	if l.pos >= len(l.src) {
		return ctEOF, nil
	}
	c := l.src[l.pos]
	switch {
	case c == '<':
		return l.keysymLit()
	case c == ':':
		l.pos++
		return ctColon, nil
	case c == '!':
		l.pos++
		return ctBang, nil
	case c == '~':
		l.pos++
		return ctTilde, nil
	case c == '"':
		return l.stringLit()
	case isAlpha(c) || c == '_':
		start := l.pos
		for l.pos < len(l.src) && (isAlpha(l.src[l.pos]) || isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
			l.pos++
		}
		l.text = l.src[start:l.pos]
		if l.text == "include" {
			return ctInclude, nil
		}
		return ctIdent, nil
	}
	if end := strings.IndexByte(l.src[l.pos:], '\n'); end >= 0 {
		l.pos += end
	} else {
		l.pos = len(l.src)
	}
	return 0, fmt.Errorf("%s: unrecognized token %q", l.name, string(rune(c)))
}

func (l *composeLexer) keysymLit() (composeTok, error) {
	start := l.pos + 1
	end := start
	for end < len(l.src) && l.src[end] != '>' && l.src[end] != '\n' {
		end++
	}
	if end >= len(l.src) || l.src[end] != '>' {
		l.pos = end
		return 0, fmt.Errorf("%s: unterminated keysym literal", l.name)
	}
	l.text = l.src[start:end]
	l.pos = end + 1
	return ctKeysym, nil
}

func (l *composeLexer) stringLit() (composeTok, error) {
	l.pos++
	var b strings.Builder
	for l.pos < len(l.src) && l.src[l.pos] != '\n' && l.src[l.pos] != '"' {
		c := l.src[l.pos]
		l.pos++
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		l.composeEscape(&b)
	}
	if l.pos >= len(l.src) || l.src[l.pos] != '"' {
		return 0, fmt.Errorf("%s: unterminated string literal", l.name)
	}
	l.pos++
	l.text = b.String()
	if !utf8.ValidString(l.text) {
		return 0, fmt.Errorf("%s: a string literal is not valid UTF-8", l.name)
	}
	return ctString, nil
}

func (l *composeLexer) composeEscape(b *strings.Builder) {
	if l.pos >= len(l.src) {
		return
	}
	switch c := l.src[l.pos]; c {
	case '\\', '"':
		l.pos++
		b.WriteByte(c)
		return
	case 'x', 'X':
		l.pos++
		end := l.pos
		for end < len(l.src) && end < l.pos+2 && isHex(l.src[end]) {
			end++
		}
		if end > l.pos {
			v, err := strconv.ParseUint(l.src[l.pos:end], 16, 8)
			if err == nil && v != 0 {
				b.WriteByte(byte(v))
			}
		}
		l.pos = end
		return
	}
	if v, n := octal(l.src[l.pos:]); n > 0 {
		l.pos += n
		if v > 0 {
			b.WriteByte(v)
		}
	}
}

func (l *composeLexer) includePath(p *composeParser) (string, error) {
	for l.pos < len(l.src) && (l.src[l.pos] == ' ' || l.src[l.pos] == '\t' || l.src[l.pos] == '\r') {
		l.pos++
	}
	if l.pos >= len(l.src) || l.src[l.pos] != '"' {
		return "", fmt.Errorf("%s: include must be followed by a path", l.name)
	}
	l.pos++
	var b strings.Builder
	for l.pos < len(l.src) && l.src[l.pos] != '\n' && l.src[l.pos] != '"' {
		c := l.src[l.pos]
		l.pos++
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		if l.pos >= len(l.src) {
			break
		}
		f := l.src[l.pos]
		l.pos++
		switch f {
		case '%':
			b.WriteByte('%')
		case 'H':
			if p.ctx.getenv("HOME") == "" {
				return "", fmt.Errorf("%s: %%H in an include, but HOME is not set", l.name)
			}
			b.WriteString(p.ctx.getenv("HOME"))
		case 'L':
			dir := xlocaleDir(p.ctx.getenv)
			path := ""
			if dir != "" {
				path = p.ctx.localeComposePath(dir, p.locale)
			}
			if path == "" {
				return "", fmt.Errorf("%s: cannot expand %%L for locale %q", l.name, p.locale)
			}
			b.WriteString(path)
		case 'S':
			b.WriteString(xlocaleDir(p.ctx.getenv))
		default:
			return "", fmt.Errorf("%s: unknown %%%c in an include", l.name, f)
		}
	}
	if l.pos >= len(l.src) || l.src[l.pos] != '"' {
		return "", fmt.Errorf("%s: unterminated include statement", l.name)
	}
	l.pos++
	return b.String(), nil
}

type composeParser struct {
	ctx    *Context
	table  *ComposeTable
	locale string
}

type production struct {
	lhs       []Keysym
	sym       Keysym
	text      string
	hasSym    bool
	hasString bool
}

func (t *ComposeTable) add(prod *production) {
	node := t.root
	for i, sym := range prod.lhs {
		last := i == len(prod.lhs)-1
		if node.children == nil {
			node.children = map[Keysym]*composeNode{}
		}
		child := node.children[sym]
		if child == nil {
			child = &composeNode{}
			node.children[sym] = child
		}
		if !last {
			if child.leaf {
				child.leaf, child.sym, child.text = false, KeyNoSymbol, ""
			}
			node = child
			continue
		}
		child.children = nil
		child.leaf = true
		child.sym, child.text = KeyNoSymbol, ""
		if prod.hasSym {
			child.sym = prod.sym
		}
		if prod.hasString {
			child.text = prod.text
		}
	}
}

func (p *composeParser) parse(name string, src []byte, depth int) error {
	if len(src) > 0 && src[0] >= 0x80 && !strings.HasPrefix(string(src), "\xef\xbb\xbf") {
		return fmt.Errorf("%s: the file encoding is not backward compatible with ASCII", name)
	}
	l := &composeLexer{name: name, src: strings.TrimPrefix(string(src), "\xef\xbb\xbf")}
	numErrors := 0
	for {
		eof, err := p.parseLine(l, depth)
		if err != nil {
			if fatal, ok := errors.AsType[*composeFatal](err); ok {
				return fatal.err
			}
			numErrors++
			if numErrors > composeMaxErrors {
				return fmt.Errorf("too many errors, the last: %w", err)
			}
			skipLine(l)
			continue
		}
		if eof {
			return nil
		}
	}
}

type composeFatal struct{ err error }

func (e *composeFatal) Error() string { return e.err.Error() }

func skipLine(l *composeLexer) {
	for {
		tok, err := l.next()
		if err == nil && (tok == ctEOL || tok == ctEOF) {
			return
		}
	}
}

func (p *composeParser) parseLine(l *composeLexer, depth int) (bool, error) {
	var tok composeTok
	var err error
	for {
		if tok, err = l.next(); err != nil {
			return false, err
		}
		if tok != ctEOL {
			break
		}
	}
	switch tok {
	case ctEOF:
		return true, nil
	case ctInclude:
		return false, p.parseIncludeLine(l, depth)
	}
	prod := production{}
	tok, skip, err := parseLHS(l, tok, &prod)
	if err != nil {
		return false, err
	}
	if skip {
		if tok != ctEOL && tok != ctEOF {
			skipLine(l)
		}
		return false, nil
	}
	return false, p.parseRHS(l, &prod)
}

func (p *composeParser) parseIncludeLine(l *composeLexer, depth int) error {
	path, err := l.includePath(p)
	if err != nil {
		return err
	}
	tok, err := l.next()
	if err != nil {
		return err
	}
	if tok != ctEOL {
		return fmt.Errorf("%s: an unexpected token after include", l.name)
	}
	if depth >= composeMaxInclude {
		return &composeFatal{fmt.Errorf("%s: exceeded the include depth of %d", l.name, composeMaxInclude)}
	}
	src, err := os.ReadFile(path) //nolint:gosec // a path from the user's Compose file, as libxkbcommon follows it
	if err != nil {
		return &composeFatal{fmt.Errorf("%s: open included file: %w", l.name, err)}
	}
	if err := p.parse(path, src, depth+1); err != nil {
		return &composeFatal{err}
	}
	return nil
}

func resolveComposeModifier(name string) bool {
	switch name {
	case "Shift", "Ctrl", "Alt", "Meta", "Lock", "Caps":
		return true
	}
	return false
}

func parseLHS(l *composeLexer, tok composeTok, prod *production) (composeTok, bool, error) {
	for {
		switch tok {
		case ctColon:
			if len(prod.lhs) == 0 {
				return tok, true, nil
			}
			return tok, false, nil
		case ctKeysym:
			sym := KeysymFromName(l.text, KeysymNoFlags)
			if sym == KeyNoSymbol {
				return tok, false, fmt.Errorf("%s: unrecognized keysym %q on the left-hand side", l.name, l.text)
			}
			if len(prod.lhs)+1 > composeMaxLHS {
				return tok, true, nil
			}
			prod.lhs = append(prod.lhs, sym)
		case ctIdent:
			if l.text != "None" && !resolveComposeModifier(l.text) {
				return tok, false, fmt.Errorf("%s: unrecognized modifier %q", l.name, l.text)
			}
		case ctTilde:
			next, err := l.next()
			if err != nil {
				return tok, false, err
			}
			if next != ctIdent {
				return next, false, fmt.Errorf("%s: an unexpected token after '~'", l.name)
			}
			if !resolveComposeModifier(l.text) {
				return next, false, fmt.Errorf("%s: unrecognized modifier %q", l.name, l.text)
			}
		case ctBang:
		default:
			return tok, false, fmt.Errorf("%s: an unexpected token on the left-hand side", l.name)
		}
		var err error
		if tok, err = l.next(); err != nil {
			return tok, false, err
		}
	}
}

func (p *composeParser) parseRHS(l *composeLexer, prod *production) error {
	for {
		tok, err := l.next()
		if err != nil {
			return err
		}
		switch tok {
		case ctString:
			if prod.hasString || l.text == "" || len(l.text) >= composeMaxString {
				skipLine(l)
				return nil
			}
			prod.text, prod.hasString = l.text, true
		case ctIdent:
			sym := KeysymFromName(l.text, KeysymNoFlags)
			if sym == KeyNoSymbol {
				return fmt.Errorf("%s: unrecognized keysym %q on the right-hand side", l.name, l.text)
			}
			if prod.hasSym {
				skipLine(l)
				return nil
			}
			prod.sym, prod.hasSym = sym, true
			p.table.add(prod)
			return nil
		case ctEOL:
			if !prod.hasString && !prod.hasSym {
				return nil
			}
			p.table.add(prod)
			return nil
		default:
			return fmt.Errorf("%s: an unexpected token on the right-hand side", l.name)
		}
	}
}

// ComposeFeedResult says whether Feed looked at a keysym.
type ComposeFeedResult uint8

const (
	// ComposeFeedIgnored means the keysym is a modifier, which takes no
	// part in composing and changes nothing.
	ComposeFeedIgnored ComposeFeedResult = iota
	// ComposeFeedAccepted means the keysym advanced the state.
	ComposeFeedAccepted
)

// ComposeStatus is where a ComposeState stands.
type ComposeStatus uint8

const (
	// ComposeNothing means no sequence is in flight.
	ComposeNothing ComposeStatus = iota
	// ComposeComposing means a sequence is waiting for more keysyms.
	ComposeComposing
	// ComposeComposed means a sequence just finished.
	ComposeComposed
	// ComposeCancelled means the last keysym ended a sequence unfinished.
	ComposeCancelled
)

// ComposeState is one input context's progress through a ComposeTable.
// Not safe for concurrent use.
type ComposeState struct {
	table      *ComposeTable
	prev, node *composeNode
}

// NewState opens a state with no sequence in flight.
func (t *ComposeTable) NewState() *ComposeState {
	return &ComposeState{table: t, prev: t.root, node: t.root}
}

// Feed advances the state by one keysym.
func (s *ComposeState) Feed(sym Keysym) ComposeFeedResult {
	if KeysymIsModifier(sym) {
		return ComposeFeedIgnored
	}
	from := s.node
	if from.leaf {
		from = s.table.root
	}
	next := from.children[sym]
	if next == nil {
		next = s.table.root
	}
	s.prev, s.node = s.node, next
	return ComposeFeedAccepted
}

// Reset drops any sequence in flight.
func (s *ComposeState) Reset() { s.prev, s.node = s.table.root, s.table.root }

// Status is where the state stands.
func (s *ComposeState) Status() ComposeStatus {
	root := s.table.root
	switch {
	case s.node == root && s.prev != root && !s.prev.leaf:
		return ComposeCancelled
	case s.node == root:
		return ComposeNothing
	case !s.node.leaf:
		return ComposeComposing
	}
	return ComposeComposed
}

// UTF8 is the text the finished sequence commits, "" otherwise.
func (s *ComposeState) UTF8() string {
	if !s.node.leaf {
		return ""
	}
	if s.node.text == "" && s.node.sym != KeyNoSymbol {
		return KeysymToUTF8(s.node.sym)
	}
	return s.node.text
}

// OneSym is the keysym the finished sequence commits, KeyNoSymbol
// otherwise.
func (s *ComposeState) OneSym() Keysym {
	if !s.node.leaf {
		return KeyNoSymbol
	}
	return s.node.sym
}
