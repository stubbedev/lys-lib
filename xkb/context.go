package xkb

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

// defaultRoot is the xkeyboard-config data directory a distribution build
// bakes in with -X; empty, the usual locations are searched.
var defaultRoot string

// defaultVersionedExtensions and defaultUnversionedExtensions are the XKB
// extension directories a distribution build bakes in with -X; empty, they
// follow the data directory.
var (
	defaultVersionedExtensions   string
	defaultUnversionedExtensions string
)

var rootCandidates = []string{
	"/usr/share/xkeyboard-config-2",
	"/usr/share/X11/xkb",
	"/run/current-system/sw/share/X11/xkb",
	"/usr/local/share/X11/xkb",
	"/etc/X11/xkb",
}

const (
	defaultExtraPath  = "/etc/xkb"
	defaultLegacyRoot = "/usr/share/X11/xkb"
)

// ErrNoData reports that no xkeyboard-config data directory was found, so
// nothing that includes a system file can compile.
var ErrNoData = errors.New("xkb: no xkeyboard-config data directory")

// Names selects a keymap by rules, model, layout, variant and options.
type Names struct {
	Rules, Model, Layout, Variant, Options string
	// OptionsSet uses Options even when it is empty, overriding
	// XKB_DEFAULT_OPTIONS on a context that reads the environment.
	OptionsSet bool
}

// ComponentNames is the keycodes, types, compatibility, symbols and geometry
// includes rules resolve names to.
type ComponentNames struct {
	Keycodes, Types, Compatibility, Symbols, Geometry string
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

// ContextFlags adjusts NewContextFlags.
type ContextFlags uint8

const (
	// ContextNoDefaultIncludes starts with an empty include path.
	ContextNoDefaultIncludes ContextFlags = 1 << iota
	// ContextNoEnvironmentNames ignores the XKB_DEFAULT_* variables when
	// filling in empty names.
	ContextNoEnvironmentNames
	// ContextNoSecureGetenv reads the environment even when the process runs
	// setuid or setgid.
	ContextNoSecureGetenv
)

// LogLevel is the severity of a log message; a lower value is more severe.
type LogLevel int

const (
	// LogCritical is a failure that cannot be recovered from.
	LogCritical LogLevel = 10
	// LogError is a failure of the requested operation.
	LogError LogLevel = 20
	// LogWarning is a problem the operation worked around.
	LogWarning LogLevel = 30
	// LogInfo is information of general interest.
	LogInfo LogLevel = 40
	// LogDebug is detail for debugging.
	LogDebug LogLevel = 50
)

// LogFunc receives log messages; message ends in a newline and starts with
// [XKB-NNN] when it has a registered code.
type LogFunc func(level LogLevel, message string)

// Context is where keymaps find their include files and report what they
// find wrong. Safe for concurrent use.
type Context struct {
	getenv     func(string) string
	flags      ContextFlags
	legacyRoot string

	mu        sync.RWMutex
	includes  []string
	failed    []string
	pending   bool
	logFn     LogFunc
	logLevel  LogLevel
	verbosity int
	userData  any
}

// NewContext is NewContextFlags with no flags.
func NewContext(getenv func(string) string) *Context { return NewContextFlags(getenv, 0) }

// NewContextFlags makes a context. The default include path, added on first
// use unless ContextNoDefaultIncludes, is $XDG_CONFIG_HOME/xkb or
// ~/.config/xkb, ~/.xkb, $XKB_CONFIG_EXTRA_PATH or /etc/xkb, the XKB extension
// directories, then $XKB_CONFIG_ROOT or the system's xkeyboard-config data.
// XKB_LOG_LEVEL and XKB_LOG_VERBOSITY set the logging.
func NewContextFlags(getenv func(string) string, flags ContextFlags) *Context {
	c := &Context{flags: flags, legacyRoot: defaultLegacyRoot, pending: flags&ContextNoDefaultIncludes == 0, logLevel: LogError}
	c.getenv = getenv
	if flags&ContextNoSecureGetenv == 0 && (os.Getuid() != os.Geteuid() || os.Getgid() != os.Getegid()) {
		c.getenv = func(string) string { return "" }
	}
	if env := c.getenv("XKB_LOG_LEVEL"); env != "" {
		c.logLevel = parseLogLevel(env)
	}
	if env := c.getenv("XKB_LOG_VERBOSITY"); env != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(env)); err == nil {
			c.verbosity = v
		}
	}
	return c
}

func parseLogLevel(s string) LogLevel {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return LogLevel(v)
	}
	lower := strings.ToLower(s)
	for _, l := range []struct {
		prefix string
		level  LogLevel
	}{{"crit", LogCritical}, {"err", LogError}, {"warn", LogWarning}, {"info", LogInfo}, {"debug", LogDebug}, {"dbg", LogDebug}} {
		if strings.HasPrefix(lower, l.prefix) {
			return l.level
		}
	}
	return LogError
}

func logPrefix(level LogLevel) string {
	switch level {
	case LogDebug:
		return "xkb: DEBUG: "
	case LogInfo:
		return "xkb: INFO: "
	case LogWarning:
		return "xkb: WARNING: "
	case LogError:
		return "xkb: ERROR: "
	case LogCritical:
		return "xkb: CRITICAL: "
	}
	return ""
}

// SetLogFunc routes log messages to fn; nil restores the default, stderr.
func (c *Context) SetLogFunc(fn LogFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logFn = fn
}

// SetLogLevel sets the least severe level logged; LogError by default.
func (c *Context) SetLogLevel(level LogLevel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logLevel = level
}

// LogLevel is the least severe level logged.
func (c *Context) LogLevel() LogLevel {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.logLevel
}

// SetLogVerbosity sets how many of the wordier warnings are logged, from 0
// to 10.
func (c *Context) SetLogVerbosity(verbosity int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.verbosity = verbosity
}

// LogVerbosity is how many of the wordier warnings are logged.
func (c *Context) LogVerbosity() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.verbosity
}

// SetUserData keeps a value on the context for a LogFunc or other caller.
func (c *Context) SetUserData(v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.userData = v
}

// UserData is the value SetUserData kept.
func (c *Context) UserData() any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.userData
}

func (c *Context) log(level LogLevel, verbosity, code int, format string, args ...any) {
	if c == nil {
		return
	}
	c.mu.RLock()
	fn, wanted := c.logFn, c.logLevel >= level && c.verbosity >= verbosity
	c.mu.RUnlock()
	if !wanted {
		return
	}
	text := fmt.Sprintf(format, args...)
	if code != 0 {
		text = fmt.Sprintf("[XKB-%03d] %s", code, text)
	}
	if fn != nil {
		fn(level, text)
		return
	}
	fmt.Fprint(os.Stderr, logPrefix(level), text)
}

func (c *Context) ensureIncludes() {
	c.mu.Lock()
	pending := c.pending && len(c.failed) == 0
	if pending {
		c.pending = false
	}
	c.mu.Unlock()
	if pending && !c.AppendDefaultIncludePaths() {
		c.log(LogError, 0, 632, "Failed to add any default include path (system path: %s)\n", c.systemPath())
	}
}

// IncludePaths is the directories searched for include files, first match
// wins.
func (c *Context) IncludePaths() []string {
	c.ensureIncludes()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.includes)
}

// NumIncludePaths is how many directories are searched.
func (c *Context) NumIncludePaths() uint32 {
	return uint32(len(c.IncludePaths())) //nolint:gosec // a short list
}

// IncludePath is directory i of the search path, false when out of range.
func (c *Context) IncludePath(i uint32) (string, bool) {
	paths := c.IncludePaths()
	if uint64(i) >= uint64(len(paths)) {
		return "", false
	}
	return paths[i], true
}

// AppendIncludePath adds a directory to the search path, false when it is
// not a readable directory.
func (c *Context) AppendIncludePath(path string) bool {
	c.ensureIncludes()
	return c.appendPath(path)
}

func (c *Context) appendPath(path string) bool {
	info, err := os.Stat(path)
	if err == nil && !info.IsDir() {
		err = errors.New("not a directory")
	}
	if err == nil {
		var dir *os.File
		if dir, err = os.Open(path); err == nil { //nolint:gosec // a directory the caller or the environment named, only opened to test access
			err = dir.Close()
		}
	}
	c.mu.Lock()
	if err != nil {
		c.failed = append(c.failed, path)
	} else {
		c.includes = append(c.includes, path)
	}
	c.mu.Unlock()
	if err != nil {
		c.log(LogInfo, 0, 0, "Include path failed: %q (%v)\n", path, err)
		return false
	}
	c.log(LogInfo, 0, 0, "Include path added: %s\n", path)
	return true
}

func (c *Context) extraPath() string {
	if p := c.getenv("XKB_CONFIG_EXTRA_PATH"); p != "" {
		return p
	}
	return defaultExtraPath
}

func (c *Context) systemPath() string {
	if root := c.getenv("XKB_CONFIG_ROOT"); root != "" {
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

func (c *Context) versionedExtensionsPath() string {
	if p := c.getenv("XKB_CONFIG_VERSIONED_EXTENSIONS_PATH"); p != "" {
		return p
	}
	if defaultVersionedExtensions != "" {
		return defaultVersionedExtensions
	}
	if root := c.systemPath(); root != "" && root != c.legacyRoot {
		return root + ".d"
	}
	return ""
}

func (c *Context) unversionedExtensionsPath() string {
	if p := c.getenv("XKB_CONFIG_UNVERSIONED_EXTENSIONS_PATH"); p != "" {
		return p
	}
	if defaultUnversionedExtensions != "" {
		return defaultUnversionedExtensions
	}
	root := c.systemPath()
	if i := strings.LastIndexByte(root, '-'); i > 0 && root != c.legacyRoot && strings.HasPrefix(root[i+1:], "2") {
		return root[:i] + ".d"
	}
	return ""
}

func (c *Context) appendExtensions(path string, skip map[string]bool) bool {
	if path == "" {
		return false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		c.log(LogDebug, 0, 0, "Include extensions path failed: %s (%v)\n", path, err)
		return false
	}
	ok := false
	for _, e := range entries {
		full := filepath.Join(path, e.Name())
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() || skip[e.Name()] {
			continue
		}
		skip[e.Name()] = true
		ok = c.appendPath(full) || ok
	}
	return ok
}

// AppendDefaultIncludePaths adds the default search path, false when none of
// it exists.
func (c *Context) AppendDefaultIncludePaths() bool {
	ok := false
	home := c.getenv("HOME")
	if xdg := c.getenv("XDG_CONFIG_HOME"); xdg != "" {
		ok = c.appendPath(filepath.Join(xdg, "xkb")) || ok
	} else if home != "" {
		ok = c.appendPath(filepath.Join(home, ".config", "xkb")) || ok
	}
	if home != "" {
		ok = c.appendPath(filepath.Join(home, ".xkb")) || ok
	}
	ok = c.appendPath(c.extraPath()) || ok
	seen := map[string]bool{}
	ok = c.appendExtensions(c.versionedExtensionsPath(), seen) || ok
	ok = c.appendExtensions(c.unversionedExtensionsPath(), seen) || ok
	root := c.systemPath()
	hasRoot := root != "" && c.appendPath(root)
	ok = hasRoot || ok
	if !hasRoot && root != "" {
		c.log(LogWarning, 0, 0, "Root include path failed; fallback to %q. The setup is probably misconfigured. Please ensure that %q is available in the environment.\n", c.legacyRoot, root)
		ok = c.appendPath(c.legacyRoot) || ok
	}
	return ok
}

// ClearIncludePaths empties the search path.
func (c *Context) ClearIncludePaths() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.includes, c.failed, c.pending = nil, nil, false
}

// ResetDefaultIncludePaths replaces the search path with the default one,
// false when none of it exists.
func (c *Context) ResetDefaultIncludePaths() bool {
	c.ClearIncludePaths()
	return c.AppendDefaultIncludePaths()
}

func (c *Context) sanitize(n Names) Names {
	env := func(name string) string {
		if c.flags&ContextNoEnvironmentNames != 0 {
			return ""
		}
		return c.getenv(name)
	}
	if n.Rules == "" {
		if n.Rules = env("XKB_DEFAULT_RULES"); n.Rules == "" {
			n.Rules = "evdev"
		}
	}
	if n.Model == "" {
		if n.Model = env("XKB_DEFAULT_MODEL"); n.Model == "" {
			n.Model = "pc105"
		}
	}
	if n.Layout == "" {
		layout := env("XKB_DEFAULT_LAYOUT")
		variant := ""
		if layout != "" {
			variant = env("XKB_DEFAULT_VARIANT")
		} else {
			layout = "us"
		}
		if n.Variant != "" {
			c.log(LogWarning, 0, 0, "Layout not provided, but variant set to %q: ignoring variant and using defaults for both: layout=%q, variant=%q.\n", n.Variant, layout, variant)
		}
		n.Layout, n.Variant = layout, variant
	}
	if n.Options == "" && !n.OptionsSet {
		n.Options = env("XKB_DEFAULT_OPTIONS")
	}
	return n
}

// ComponentsFromNames fills in the defaults of names and resolves them
// through the rules to the components they select.
func (c *Context) ComponentsFromNames(names Names) (Names, ComponentNames, error) {
	names = c.sanitize(names)
	s := c.newSession(FormatTextV2)
	if len(s.paths) == 0 {
		return names, ComponentNames{}, fmt.Errorf("resolve %+v: %w", names, ErrNoData)
	}
	kccgst, _, err := s.resolveRules(names)
	if err != nil {
		return names, ComponentNames{}, fmt.Errorf("xkb: resolve keymap %+v: %w", names, err)
	}
	return names, ComponentNames{Keycodes: kccgst.keycodes, Types: kccgst.types, Compatibility: kccgst.compat, Symbols: kccgst.symbols, Geometry: kccgst.geometry}, nil
}

// NewKeymapFromNames compiles a keymap from rules, model, layout, variant
// and options in FormatTextV2, as xkb_keymap_new_from_names does; empty
// fields take the XKB_DEFAULT_* variables, then evdev, pc105 and us.
func (c *Context) NewKeymapFromNames(names Names) (*Keymap, error) {
	return c.NewKeymapFromNamesFormat(names, FormatTextV2)
}

// NewKeymapFromNamesFormat is NewKeymapFromNames in a chosen format.
func (c *Context) NewKeymapFromNamesFormat(names Names, format KeymapFormat) (*Keymap, error) {
	if !format.valid() {
		return nil, fmt.Errorf("xkb: compile keymap: unsupported format %d", format)
	}
	names = c.sanitize(names)
	s := c.newSession(format)
	if len(s.paths) == 0 {
		return nil, fmt.Errorf("compile keymap %+v: %w", names, ErrNoData)
	}
	kccgst, layouts, err := s.resolveRules(names)
	if err != nil {
		c.log(LogError, 0, keymapCompilationFailed, "Couldn't look up rules '%s', model '%s', layout '%s', variant '%s', options '%s'\n", names.Rules, names.Model, names.Layout, names.Variant, names.Options)
		return nil, fmt.Errorf("xkb: resolve keymap %+v: %w", names, err)
	}
	m, err := c.compileComponents(kccgst, layouts, s, format)
	if err != nil {
		return nil, fmt.Errorf("xkb: compile keymap %+v: %w", names, err)
	}
	return m, nil
}

func (c *Context) compileComponents(kccgst components4, layouts uint32, s *session, format KeymapFormat) (*Keymap, error) {
	file := &xkbFile{typ: fileKeymap}
	for _, sec := range []struct {
		typ  fileType
		text string
	}{{fileKeycodes, kccgst.keycodes}, {fileTypes, kccgst.types}, {fileCompat, kccgst.compat}, {fileSymbols, kccgst.symbols}} {
		inc, err := parseInclude(sec.text, mergeDefault)
		if err != nil {
			return nil, err
		}
		if len(inc.stmts) == 0 {
			return nil, fmt.Errorf("an empty %s component", sec.typ.dir())
		}
		file.sections = append(file.sections, &xkbFile{typ: sec.typ, decls: []decl{inc}})
	}
	m := newKeymap(c, format)
	m.numGroups = min(layouts, format.maxGroups())
	if err := m.finish(file, s); err != nil {
		c.log(LogError, 0, keymapCompilationFailed, "Failed to compile keymap\n")
		return nil, err
	}
	return m, nil
}

// NewKeymapFromString compiles keymap text in FormatTextV1, the format a
// compositor or a virtual keyboard sends over wl_keyboard.keymap, resolving
// any include statements it holds against the context's data. A failure
// leaves no keymap: a caller must not go on reading keys through an older
// one.
func (c *Context) NewKeymapFromString(text []byte) (*Keymap, error) {
	return c.NewKeymapFromBuffer(text, FormatTextV1)
}

// NewKeymapFromBuffer is NewKeymapFromString in a chosen format.
func (c *Context) NewKeymapFromBuffer(text []byte, format KeymapFormat) (*Keymap, error) {
	if !format.valid() {
		return nil, fmt.Errorf("xkb: compile keymap: unsupported format %d", format)
	}
	text = trimNUL(text)
	file, err := newParsedFile(c, "(input string)", string(text)).defaultMap()
	if err != nil {
		c.log(LogError, 0, keymapCompilationFailed, "Failed to parse input xkb string\n")
		return nil, fmt.Errorf("xkb: parse keymap: %w", err)
	}
	if file.typ != fileKeymap {
		c.log(LogError, 0, keymapCompilationFailed, "Cannot compile a %s file alone into a keymap\n", file.typ.dir())
		return nil, fmt.Errorf("xkb: parse keymap: a %s file cannot compile alone into a keymap", file.typ.dir())
	}
	m := newKeymap(c, format)
	if err := m.finish(file, c.newSession(format)); err != nil {
		c.log(LogError, 0, keymapCompilationFailed, "Failed to compile keymap\n")
		return nil, fmt.Errorf("xkb: compile keymap: %w", err)
	}
	return m, nil
}

// NewKeymapFromFile compiles keymap text read from r.
func (c *Context) NewKeymapFromFile(r io.Reader, format KeymapFormat) (*Keymap, error) {
	text, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("xkb: read keymap: %w", err)
	}
	return c.NewKeymapFromBuffer(text, format)
}

const keymapCompilationFailed = 822

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
	dir := typ.dir()
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
			if c.getenv("HOME") == "" {
				return "", true, fmt.Errorf("%%H in %q, but HOME is not set", name)
			}
			b.WriteString(c.getenv("HOME"))
		case 'S':
			b.WriteString(c.systemPath() + "/" + dir)
		case 'E':
			b.WriteString(c.extraPath() + "/" + dir)
		default:
			return "", true, fmt.Errorf("unknown %%%c in include path %q", name[i], name)
		}
	}
	return b.String(), true, nil
}

type session struct {
	*Context
	format KeymapFormat
	paths  []string
	raw    map[string]rawFile
	parsed map[string]*parsedFile
}

type rawFile struct {
	src []byte
	err error
}

func (c *Context) newSession(format KeymapFormat) *session {
	return &session{Context: c, format: format, paths: c.IncludePaths(), raw: map[string]rawFile{}, parsed: map[string]*parsedFile{}}
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
		pf = newParsedFile(s.Context, path, unsafe.String(unsafe.SliceData(src), len(src))) //nolint:gosec // the session keeps src and never writes to it
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
		for _, dir := range s.paths {
			candidates = append(candidates, filepath.Join(dir, typ.dir(), target))
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
		if len(s.paths) == 0 {
			return nil, fmt.Errorf("include %q: %w", what, ErrNoData)
		}
		return nil, coded(codeInvalidIncluded, codedf(codeIncludedNotFound, "include %q: no %s file in %v", what, typ.dir(), s.paths))
	}
	return nil, coded(codeInvalidIncluded, fmt.Errorf("include %q: %w", what, errors.Join(errs...)))
}

const (
	verbosityBrief    = 1
	verbosityDetailed = 5
	verbosityVerbose  = 10
)

const (
	warnConflictingPreserve     = 43
	warnIllegalPreserveResult   = 195
	warnConflictingLevelNames   = 239
	warnConflictingMapEntry     = 266
	warnUndefinedKeyType        = 286
	warnNonBaseGroupName        = 305
	warnUnsupportedLegacyAction = 362
	warnDuplicateEntry          = 378
	warnConflictingTypeDefs     = 407
	warnConflictingKeySymbol    = 461
	warnExtraSymbolsIgnored     = 516
	warnConflictingKeyName      = 523
	warnMultipleGroupsAtOnce    = 700
	warnUnsupportedSymbolsField = 711
	warnUndefinedKeycode        = 770
	warnConflictingModmap       = 800
	warnConflictingKeyAction    = 883
	warnConflictingGroupMerge   = 893
	warnMissingGroupNameIndex   = 903
	warnConflictingKeyFields    = 935
	warnCannotInferKeyType      = 183
	warnUnresolvedKeymapSymbol  = 965
	warnUndeclaredTypeModifiers = 971
	warnGeometrySection         = 172
)

func (c *Context) warn(code int, format string, args ...any) {
	c.log(LogWarning, 0, code, format, args...)
}

func (c *Context) verbose(verbosity, code int, format string, args ...any) {
	c.log(LogWarning, verbosity, code, format, args...)
}

func (c *Context) report(sameFile bool) bool {
	v := c.LogVerbosity()
	return (sameFile && v > 0) || v > 9
}

func useFirstOrLast(clobber bool) string {
	if clobber {
		return "last"
	}
	return "first"
}

// sectionErrors collects a section's errors the way libxkbcommon does: a bad
// statement does not stop the ones after it, until there are more than ten.
type sectionErrors struct {
	errs []error
}

func (e *sectionErrors) add(c *Context, err error, what, name string) (abandon bool) {
	c.logError(err)
	e.errs = append(e.errs, err)
	if len(e.errs) > 10 {
		c.log(LogError, 0, invalidSyntaxCode, "Abandoning %s %q\n", what, name)
		return true
	}
	return false
}

func (e *sectionErrors) err() error { return errors.Join(e.errs...) }

const invalidSyntaxCode = 769

// codedError carries a libxkbcommon message code along an error chain, so that
// a failure logs the same codes the library's does.
type codedError struct {
	code int
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }

func (e *codedError) Unwrap() error { return e.err }

func coded(code int, err error) error {
	if err == nil {
		return nil
	}
	return &codedError{code: code, err: err}
}

func codedf(code int, format string, args ...any) error {
	return &codedError{code: code, err: fmt.Errorf(format, args...)}
}

func errorCodes(err error) []int {
	var codes []int
	var walk func(error)
	walk = func(e error) {
		for e != nil {
			if c, ok := e.(*codedError); ok { //nolint:errorlint // walking the chain by hand, branch by branch
				codes = append(codes, c.code)
			}
			if j, ok := e.(interface{ Unwrap() []error }); ok {
				for _, child := range j.Unwrap() {
					walk(child)
				}
				return
			}
			e = errors.Unwrap(e)
		}
	}
	walk(err)
	return codes
}

func (c *Context) logError(err error) {
	codes := errorCodes(err)
	if len(codes) == 0 {
		c.log(LogError, 0, 0, "%v\n", err)
		return
	}
	for _, code := range codes {
		c.log(LogError, 0, code, "%v\n", err)
	}
}

const (
	codeUnsupportedModMask = 60
	codeUndeclaredVmod     = 123
	codeInvalidReal        = 623
	codeGroupIndex         = 237
	codeDefaultsScope      = 428
	codeIntegerOverflow    = 52
	codeInvalidOperation   = 478
	codeIncludedNotFound   = 338
	codeInvalidIncluded    = 661
	codeActionField        = 563
	codeWrongFieldType     = 578
	codeUnknownDefault     = 639
	codeInvalidValue       = 796
	codeUnknownField       = 812
	codeUnknownAction      = 844
	codeInvalidIdentifier  = 949
	codeShiftLevel         = 312
)
