package icons

import (
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/stubbedev/lys-lib/render"
)

// maxEntries caps the raster cache; the least recently used entry
// evicts first. A bar holds a few dozen icons at one or two sizes, so
// the default is generous without being unbounded, and a view that
// pages through a large icon set leaves the icons still in use cached.
var maxEntries = 256

// maskMode says which sources a tint applies to.
type maskMode uint8

const (
	maskNone     maskMode = iota // never recolor
	maskSymbolic                 // recolor symbolic sources only
	maskAlways                   // recolor whatever matched
)

type cacheKey struct {
	name string
	size int
	frac uint32
}

type cacheEntry struct {
	err error

	symbolic bool
	natural  *render.Icon

	tinted      *render.Icon
	tintedColor render.Color
}

// Cache resolves themed icons and caches the decoded rasters, one per
// (name, size, scale) key with the scale in
// 120ths (120 is 1x, 240 is 2x). It is safe for concurrent use.
//
// SetTheme, SetSearchPaths and InvalidateTheme drop every cached
// raster, bump Generation, and run the OnReset listeners; the
// application follows the desktop's icon theme through the settings
// portal and applies each switch with ApplyIconTheme. A consumer
// watches Generation so its next damage pass picks a change up.
//
// mu is only ever held to look and to store: reads, decodes and index
// builds run outside it, so a paint's Peek never waits for the disk.
type Cache struct {
	mu    sync.Mutex
	theme string
	paths []string
	gen   atomic.Uint64

	// themeListeners hear every followed icon-theme switch (follow.go).
	themeListeners []func(string)
	// resetListeners hear every reset, whatever caused it.
	resetListeners []func()

	entries map[cacheKey]*cacheEntry
	// order lists the keys from least to most recently used.
	order     []cacheKey
	fallbacks map[string]string

	// fsys is the file system lookups read.
	fsys themeFS
	// index is the theme chain's directories, read on the first lookup
	// after a reset, with its memoized resolutions. A reset drops it.
	index *themeIndex
	// namespace is an application's own icons (SetNamespace), and
	// namespaceIndex its directory under each base, read on the first
	// lookup of one of its names after a reset.
	namespace      iconNamespace
	namespaceIndex *themeIndex
	buildMu        sync.Mutex
}

// DefaultTheme is GTK's default icon theme, the one lookups start on
// until a setting names another.
const DefaultTheme = "Adwaita"

// New returns a cache for the theme called name; empty means the
// default chain alone (Adwaita, then hicolor).
// Lookups search the standard XDG paths until SetSearchPaths overrides
// them.
func New(name string) *Cache {
	return &Cache{theme: name, entries: map[cacheKey]*cacheEntry{}, fallbacks: map[string]string{}, fsys: osFS{}}
}

var (
	defaultCache *Cache
	defaultOnce  sync.Once
)

// Default returns the process-wide cache lys-ui's widget.NewThemeIcon resolves
// through, starting on DefaultTheme, as GTK does.
func Default() *Cache {
	defaultOnce.Do(func() { defaultCache = New(DefaultTheme) })
	return defaultCache
}

// Theme returns the theme this cache looks icons up in.
func (c *Cache) Theme() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.theme
}

// SetTheme switches the theme icons resolve against (empty means the
// default chain alone) and drops every cached raster.
func (c *Cache) SetTheme(name string) {
	c.mu.Lock()
	if name == c.theme {
		c.mu.Unlock()
		return
	}
	c.theme = name
	listeners := c.resetLocked()
	c.mu.Unlock()
	runAll(listeners)
}

// SetSearchPaths overrides the base directories lookups search (the
// slice is copied); nil restores the XDG default. Changing paths drops
// every cached raster.
func (c *Cache) SetSearchPaths(paths []string) {
	c.mu.Lock()
	if slices.Equal(c.paths, paths) && (c.paths == nil) == (paths == nil) {
		c.mu.Unlock()
		return
	}
	c.paths = slices.Clone(paths)
	listeners := c.resetLocked()
	c.mu.Unlock()
	runAll(listeners)
}

// SearchPaths returns the base directories lookups search: what
// SetSearchPaths set, else the XDG default.
func (c *Cache) SearchPaths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.searchPathsLocked())
}

// Generation counts cache-wide resets: every SetTheme, SetSearchPaths,
// or InvalidateTheme call bumps it. Widgets compare their resolved
// generation against it to notice a theme switch. It never waits.
func (c *Cache) Generation() uint64 { return c.gen.Load() }

// InvalidateTheme drops every cached raster and bumps Generation: the
// hook for a theme switch or for icon files installed or removed under
// the search paths, since a miss is cached as firmly as a hit.
func (c *Cache) InvalidateTheme() {
	c.mu.Lock()
	listeners := c.resetLocked()
	c.mu.Unlock()
	runAll(listeners)
}

// OnReset registers fn to run after every reset (SetTheme,
// SetSearchPaths, InvalidateTheme, an applied ApplyIconTheme), once
// Generation has moved. Callbacks run on the caller's goroutine; bridge
// into the loop with Application.Invoke, where the usual reaction is
// requesting a repaint.
func (c *Cache) OnReset(fn func()) {
	if fn == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetListeners = append(c.resetListeners, fn)
}

// resetLocked drops every cached raster and bumps Generation; it
// returns the reset listeners for the caller to run once unlocked.
func (c *Cache) resetLocked() []func() {
	c.entries = map[cacheKey]*cacheEntry{}
	c.order = nil
	clear(c.fallbacks)
	c.index = nil
	c.namespaceIndex = nil
	c.gen.Add(1)
	return slices.Clone(c.resetListeners)
}

func runAll(fns []func()) {
	for _, fn := range fns {
		fn()
	}
}

func (c *Cache) searchPathsLocked() []string {
	if c.paths != nil {
		return c.paths
	}
	return defaultSearchPaths()
}

// Icon returns the icon called name at logical size in its natural
// colors, rasterized into a DeviceBox(size, frac120) pixel box. The
// returned icon is shared between callers and cache entries; treat it
// as read-only.
func (c *Cache) Icon(name string, size int, frac120 uint32) (*render.Icon, error) {
	return c.load(name, size, frac120, 0, maskNone)
}

// SymbolicIcon returns the icon called name, mask-recolored to tint
// when the matched source is symbolic - a -symbolic file or an SVG
// painting with currentColor - so symbolic glyphs follow the theme
// accent while ordinary app icons keep their colors.
func (c *Cache) SymbolicIcon(name string, size int, frac120 uint32, tint render.Color) (*render.Icon, error) {
	return c.load(name, size, frac120, tint, maskSymbolic)
}

// Tinted returns the icon called name mask-recolored to tint whatever
// it matched: every pixel keeps its alpha and takes tint's color.
func (c *Cache) Tinted(name string, size int, frac120 uint32, tint render.Color) (*render.Icon, error) {
	return c.load(name, size, frac120, tint, maskAlways)
}

// load resolves and decodes once per key, then applies the tint policy.
// It reads the disk on a miss, so it belongs off the UI loop; a paint
// goes through Peek.
func (c *Cache) load(name string, size int, frac120 uint32, tint render.Color, mode maskMode) (*render.Icon, error) {
	if tint == 0 {
		mode = maskNone
	}
	e := c.ensure(cacheKey{name: name, size: size, frac: frac120})
	if e.err != nil {
		return nil, e.err
	}
	if mode == maskNone || (mode == maskSymbolic && !e.symbolic) {
		return e.natural, nil
	}
	return c.tintOf(e, tint), nil
}

func (c *Cache) tintOf(e *cacheEntry, tint render.Color) *render.Icon {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.tinted == nil || e.tintedColor != tint {
		e.tinted = e.natural.Tint(tint)
		e.tintedColor = tint
	}
	return e.tinted
}

// Raster is one decoded icon: its natural raster, or the failure that
// stuck in its place. It is shared between callers: read-only.
type Raster struct {
	Icon     *render.Icon
	Symbolic bool
	Err      error

	entry *cacheEntry
}

// Peek returns the icon if it is decoded, reading and decoding nothing:
// what a paint calls.
func (c *Cache) Peek(name string, size int, frac120 uint32) (Raster, bool) {
	key := cacheKey{name: name, size: size, frac: frac120}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return Raster{}, false
	}
	c.touchLocked(key)
	return Raster{Icon: e.natural, Symbolic: e.symbolic, Err: e.err, entry: e}, true
}

// PeekNearest returns the decoded raster of name at size whose scale is
// nearest frac120, reading nothing: what a paint shows while the raster
// for its own scale decodes. Failures are not offered.
func (c *Cache) PeekNearest(name string, size int, frac120 uint32) (Raster, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var best *cacheEntry
	bestDist := uint32(0)
	for _, key := range c.order {
		if key.name != name || key.size != size {
			continue
		}
		e := c.entries[key]
		if e.err != nil {
			continue
		}
		dist := max(key.frac, frac120) - min(key.frac, frac120)
		if best == nil || dist < bestDist {
			best, bestDist = e, dist
		}
	}
	if best == nil {
		return Raster{}, false
	}
	return Raster{Icon: best.natural, Symbolic: best.symbolic, entry: best}, true
}

// Tint is r, which Peek returned without failure, recolored to tint: a
// raster every caller with this tint shares.
func (c *Cache) Tint(r Raster, tint render.Color) *render.Icon { return c.tintOf(r.entry, tint) }

func (c *Cache) touchLocked(key cacheKey) {
	i := slices.Index(c.order, key)
	c.order = append(slices.Delete(c.order, i, i+1), key)
}

func (c *Cache) ensure(key cacheKey) *cacheEntry {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.touchLocked(key)
		c.mu.Unlock()
		return e
	}
	gen := c.gen.Load()
	c.mu.Unlock()
	e := &cacheEntry{}
	path, err := c.resolve(key.name, key.size, key.frac)
	if err == nil {
		e.decode(c.fsys, path, key.size, key.frac)
	} else {
		e.err = err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if prior, ok := c.entries[key]; ok {
		return prior
	}
	if c.gen.Load() == gen {
		c.insertLocked(key, e)
	}
	return e
}

// insertLocked remembers an entry, the least recently used going past
// the bound.
func (c *Cache) insertLocked(key cacheKey, e *cacheEntry) {
	c.entries[key] = e
	c.order = append(c.order, key)
	if len(c.order) > maxEntries {
		delete(c.entries, c.order[0])
		c.order = slices.Delete(c.order, 0, 1)
	}
}

// Preload resolves and decodes an icon into the cache for a later
// paint. It holds the lock only to look and to store, never across the
// read and the decode, so a UI loop painting other icons meanwhile does
// not wait for it. A failure is remembered as a paint's would be, and
// returned, as it is to a later Preload of the same icon.
func (c *Cache) Preload(name string, size int, frac120 uint32) error {
	return c.ensure(cacheKey{name: name, size: size, frac: frac120}).err
}

// Cached reports whether a paint of this icon would find it decoded. It
// never waits: with the lock held elsewhere the answer is no, and the
// caller asks again.
func (c *Cache) Cached(name string, size int, frac120 uint32) bool {
	if !c.mu.TryLock() {
		return false
	}
	defer c.mu.Unlock()
	_, ok := c.entries[cacheKey{name: name, size: size, frac: frac120}]
	return ok
}

// decode reads and rasterizes the matched file at the key's device box.
func (e *cacheEntry) decode(fsys themeFS, path string, size int, frac120 uint32) {
	data, err := readIcon(fsys, path)
	if err != nil {
		e.err = err
		return
	}
	e.symbolic = IsSymbolic(filepath.Base(path), data)
	e.natural, e.err = DecodeIcon(data, filepath.Base(path), size, frac120)
}
