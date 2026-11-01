package xkb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

// defaultRoot is the xkeyboard-config data directory a distribution build
// bakes in with -X; empty, the usual locations are searched.
var defaultRoot string

var rootCandidates = []string{
	"/usr/share/X11/xkb",
	"/run/current-system/sw/share/X11/xkb",
	"/usr/local/share/X11/xkb",
	"/etc/X11/xkb",
}

const defaultExtraPath = "/etc/xkb"

// ErrNoData reports that no xkeyboard-config data directory was found, so
// nothing that includes a system file can compile.
var ErrNoData = errors.New("xkb: no xkeyboard-config data directory")

// Names selects a keymap by rules, model, layout, variant and options.
type Names struct {
	Rules, Model, Layout, Variant, Options string
}

// NamesFromEnv reads the XKB_DEFAULT_* variables.
func NamesFromEnv(getenv func(string) string) Names {
	return Names{
		Rules:   getenv("XKB_DEFAULT_RULES"),
		Model:   getenv("XKB_DEFAULT_MODEL"),
		Layout:  getenv("XKB_DEFAULT_LAYOUT"),
		Variant: getenv("XKB_DEFAULT_VARIANT"),
		Options: getenv("XKB_DEFAULT_OPTIONS"),
	}
}

func (n Names) withDefaults() Names {
	if n.Rules == "" {
		n.Rules = "evdev"
	}
	if n.Model == "" {
		n.Model = "pc105"
	}
	if n.Layout == "" {
		n.Layout = "us"
		n.Variant = ""
	}
	return n
}

// Context is where keymaps find their include files. Immutable and safe
// for concurrent use.
type Context struct {
	paths     []string
	root      string
	extraPath string
	home      string
	getenv    func(string) string
}

// NewContext resolves the include path the way libxkbcommon does:
// $XDG_CONFIG_HOME/xkb or ~/.config/xkb, ~/.xkb, $XKB_CONFIG_EXTRA_PATH or
// /etc/xkb, then $XKB_CONFIG_ROOT or the system's xkeyboard-config data.
// Directories that do not exist are left out.
func NewContext(getenv func(string) string) *Context {
	c := &Context{home: getenv("HOME"), getenv: getenv}
	c.extraPath = getenv("XKB_CONFIG_EXTRA_PATH")
	if c.extraPath == "" {
		c.extraPath = defaultExtraPath
	}
	c.root = systemRoot(getenv)
	var candidates []string
	if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "xkb"))
	} else if c.home != "" {
		candidates = append(candidates, filepath.Join(c.home, ".config", "xkb"))
	}
	if c.home != "" {
		candidates = append(candidates, filepath.Join(c.home, ".xkb"))
	}
	candidates = append(candidates, c.extraPath, c.root)
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			c.paths = append(c.paths, dir)
		}
	}
	return c
}

func systemRoot(getenv func(string) string) string {
	if root := getenv("XKB_CONFIG_ROOT"); root != "" {
		return root
	}
	if defaultRoot != "" {
		return defaultRoot
	}
	for _, dir := range rootCandidates {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

// IncludePaths is the directories searched for include files, first
// match wins.
func (c *Context) IncludePaths() []string { return append([]string(nil), c.paths...) }

// NewKeymapFromNames compiles a keymap from rules, model, layout, variant
// and options; empty fields take evdev, pc105 and us.
func (c *Context) NewKeymapFromNames(names Names) (*Keymap, error) {
	names = names.withDefaults()
	if len(c.paths) == 0 {
		return nil, fmt.Errorf("compile keymap %+v: %w", names, ErrNoData)
	}
	s := c.newSession()
	kccgst, layouts, err := s.resolveRules(names)
	if err != nil {
		return nil, fmt.Errorf("xkb: resolve keymap %+v: %w", names, err)
	}
	file := &xkbFile{typ: fileKeymap}
	for _, sec := range []struct {
		typ  fileType
		text string
	}{{fileKeycodes, kccgst.keycodes}, {fileTypes, kccgst.types}, {fileCompat, kccgst.compat}, {fileSymbols, kccgst.symbols}} {
		inc, err := parseInclude(sec.text, mergeDefault)
		if err != nil {
			return nil, fmt.Errorf("xkb: compile keymap %+v: %w", names, err)
		}
		if len(inc.stmts) == 0 {
			return nil, fmt.Errorf("xkb: compile keymap %+v: an empty %s component", names, includeDirs[sec.typ])
		}
		file.sections = append(file.sections, &xkbFile{typ: sec.typ, decls: []decl{inc}})
	}
	m := newKeymap()
	m.numGroups = min(layouts, maxGroupsV1)
	if err := m.finish(file, s); err != nil {
		return nil, fmt.Errorf("xkb: compile keymap %+v: %w", names, err)
	}
	return m, nil
}

// NewKeymapFromString compiles keymap text, such as what a compositor or
// a virtual keyboard sends over wl_keyboard.keymap, resolving any include
// statements it holds against the context's data. A failure leaves no
// keymap: a caller must not go on reading keys through an older one.
func (c *Context) NewKeymapFromString(text []byte) (*Keymap, error) {
	text = trimNUL(text)
	file, err := newParsedFile("(input string)", string(text)).defaultMap()
	if err != nil {
		return nil, fmt.Errorf("xkb: parse keymap: %w", err)
	}
	if file.typ != fileKeymap {
		return nil, fmt.Errorf("xkb: parse keymap: a %s file cannot compile alone into a keymap", includeDirs[file.typ])
	}
	m := newKeymap()
	if err := m.finish(file, c.newSession()); err != nil {
		return nil, fmt.Errorf("xkb: compile keymap: %w", err)
	}
	return m, nil
}

func trimNUL(text []byte) []byte {
	if n := len(text); n > 0 && text[n-1] == 0 {
		return text[:n-1]
	}
	return text
}

func (c *Context) expandPath(name string, typ fileType) (string, bool, error) {
	if !strings.Contains(name, "%") {
		return name, false, nil
	}
	dir := includeDirs[typ]
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		if name[i] != '%' {
			b.WriteByte(name[i])
			continue
		}
		i++
		if i >= len(name) {
			return "", true, fmt.Errorf("a trailing %% in include path %q", name)
		}
		switch name[i] {
		case '%':
			b.WriteByte('%')
		case 'H':
			if c.home == "" {
				return "", true, fmt.Errorf("%%H in %q, but HOME is not set", name)
			}
			b.WriteString(c.home)
		case 'S':
			b.WriteString(c.root + "/" + dir)
		case 'E':
			b.WriteString(c.extraPath + "/" + dir)
		default:
			return "", true, fmt.Errorf("unknown %%%c in include path %q", name[i], name)
		}
	}
	return b.String(), true, nil
}

type session struct {
	*Context
	raw    map[string]rawFile
	parsed map[string]*parsedFile
}

type rawFile struct {
	src []byte
	err error
}

func (c *Context) newSession() *session {
	return &session{Context: c, raw: map[string]rawFile{}, parsed: map[string]*parsedFile{}}
}

func (s *session) read(path string) ([]byte, error) {
	if f, ok := s.raw[path]; ok {
		return f.src, f.err
	}
	src, err := os.ReadFile(path) //nolint:gosec // an include path from the keymap or rules, as libxkbcommon follows it
	s.raw[path] = rawFile{src, err}
	return src, err
}

func (s *session) parse(path string, src []byte) *parsedFile {
	pf, ok := s.parsed[path]
	if !ok {
		pf = newParsedFile(path, unsafe.String(unsafe.SliceData(src), len(src))) //nolint:gosec // the session keeps src and never writes to it
		s.parsed[path] = pf
	}
	return pf
}

func (s *session) processInclude(stmt includeStmt, typ fileType) (*xkbFile, error) {
	c := s.Context
	target, expanded, err := c.expandPath(stmt.file, typ)
	if err != nil {
		return nil, err
	}
	var candidates []string
	switch {
	case filepath.IsAbs(target):
		candidates = []string{target}
	case expanded:
	default:
		for _, dir := range c.paths {
			candidates = append(candidates, filepath.Join(dir, includeDirs[typ], target))
		}
	}
	var fallback *xkbFile
	var errs []error
	found := false
	for _, path := range candidates {
		src, err := s.read(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		found = true
		pf := s.parse(path, src)
		var f *xkbFile
		if stmt.mapName != "" {
			f, err = pf.named(stmt.mapName)
		} else {
			f, err = pf.defaultMap()
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if f.typ != typ {
			errs = append(errs, fmt.Errorf("%s: include of the wrong type", path))
			continue
		}
		if stmt.mapName != "" || f.flags&mapIsDefault != 0 {
			return f, nil
		}
		if fallback == nil {
			fallback = f
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	what := stmt.file
	if stmt.mapName != "" {
		what = stmt.file + "(" + stmt.mapName + ")"
	}
	if !found && len(errs) == 0 {
		if len(c.paths) == 0 {
			return nil, fmt.Errorf("include %q: %w", what, ErrNoData)
		}
		return nil, fmt.Errorf("include %q: no %s file in %v", what, includeDirs[typ], c.paths)
	}
	return nil, fmt.Errorf("include %q: %w", what, errors.Join(errs...))
}
