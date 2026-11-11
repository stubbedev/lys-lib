package icons

import (
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/stubbedev/lys-lib/assetfile"
)

// ErrNotFound reports that no theme in the chain, nor any unthemed
// fallback directory, holds the requested icon.
var ErrNotFound = errors.New("icons: icon not found")

// extensions are the file suffixes tried for one icon name, in spec
// order: a directory shipping both prefers the raster PNG for that
// fixed size.
var extensions = [...]string{".png", ".svg", ".svgz"}

// Lookup resolves name at logical size and 120-based device scale
// frac120 and returns the matched file's path. It walks the default
// cache's theme chain; on a Cache use its Lookup method.
func Lookup(name string, size int, frac120 uint32) (string, error) {
	return Default().Lookup(name, size, frac120)
}

// Lookup resolves name like the package function, against this cache's
// theme and search paths, per the icon theme spec's lookup algorithm:
// for each theme of the inheritance chain (hicolor last), first any
// directory whose size rule accepts the request, then the nearest
// existing one, then the unthemed fallback across the base directories,
// and last the bundled set - whose matches return a "builtin:" path
// rather than a file.
func (c *Cache) Lookup(name string, size int, frac120 uint32) (string, error) {
	return c.resolve(name, size, frac120)
}

// resolve is Lookup; the error wraps ErrNotFound. Results, misses
// included, are memoized in the theme index until a reset drops it. It
// reads the disk with the cache's lock released.
func (c *Cache) resolve(name string, size int, frac120 uint32) (string, error) {
	if filepath.IsAbs(name) {
		return name, nil
	}
	if path := c.namespaceFile(name); path != "" {
		return path, nil
	}
	ix := c.built(&c.index, func(fsys themeFS, theme string, _ iconNamespace, paths []string) *themeIndex {
		return buildThemeIndex(fsys, theme, paths)
	})
	scale := dirScale(frac120)
	path := ix.resolve(name, size, scale)
	if path == "" {
		return "", fmt.Errorf("%w: %q at size %d scale %d", ErrNotFound, name, size, scale)
	}
	return path, nil
}

func (c *Cache) built(slot **themeIndex, build func(themeFS, string, iconNamespace, []string) *themeIndex) *themeIndex {
	c.mu.Lock()
	ix := *slot
	c.mu.Unlock()
	if ix != nil {
		return ix
	}
	c.buildMu.Lock()
	defer c.buildMu.Unlock()
	for {
		c.mu.Lock()
		ix := *slot
		gen, theme, ns, paths := c.gen.Load(), c.theme, c.namespace, c.searchPathsLocked()
		c.mu.Unlock()
		if ix != nil {
			return ix
		}
		ix = build(c.fsys, theme, ns, paths)
		c.mu.Lock()
		current := c.gen.Load() == gen
		if current {
			*slot = ix
		}
		c.mu.Unlock()
		if current {
			return ix
		}
	}
}

// iconNamespace is a set of icon names one directory below each base
// directory holds, recognized by what precedes their first dash.
type iconNamespace struct {
	dir      string
	prefixes []string
}

func (n iconNamespace) owns(name string) bool {
	prefix, _, found := strings.Cut(name, "-")
	return found && slices.Contains(n.prefixes, prefix)
}

// SetNamespace declares names starting with one of prefixes and a dash
// to be the application's own, kept in dir (relative) below the base
// directories: a lookup of one finds it there, at any size and scale,
// without listing the theme chain, which is what costs a first lookup
// its thousand directories, so a theme cannot override it. One found
// nowhere there resolves as any other name. Changing it drops every
// cached raster.
func (c *Cache) SetNamespace(dir string, prefixes []string) {
	c.mu.Lock()
	if c.namespace.dir == dir && slices.Equal(c.namespace.prefixes, prefixes) {
		c.mu.Unlock()
		return
	}
	c.namespace = iconNamespace{dir: dir, prefixes: slices.Clone(prefixes)}
	listeners := c.resetLocked()
	c.mu.Unlock()
	runAll(listeners)
}

// namespaceFile returns the file of a name in the namespace, "" for any
// other name and for one its directories do not hold.
func (c *Cache) namespaceFile(name string) string {
	c.mu.Lock()
	owned := c.namespace.owns(name)
	c.mu.Unlock()
	if !owned {
		return ""
	}
	ix := c.built(&c.namespaceIndex, func(fsys themeFS, _ string, ns iconNamespace, paths []string) *themeIndex {
		return buildNamespaceIndex(fsys, ns.dir, paths)
	})
	ix.mu.Lock()
	defer ix.mu.Unlock()
	st := stems{name: name, nameHits: ix.stems[name]}
	for sh := range st.slots() {
		if path := ix.file(sh); path != "" {
			return path
		}
	}
	return ""
}

func buildNamespaceIndex(fsys themeFS, dir string, baseDirs []string) *themeIndex {
	ix := &themeIndex{fsys: fsys, stems: map[string][]hit{}}
	for _, base := range baseDirs {
		path := filepath.Join(base, dir)
		des, err := fsys.ReadDir(path)
		if err != nil {
			continue
		}
		ix.add(slot{path: path}, des)
	}
	return ix
}

// themeFS is the file system a theme index reads: the OS's, or a
// counting wrapper in tests.
type themeFS interface {
	ReadFile(name string) ([]byte, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	Stat(name string) (fs.FileInfo, error)
}

// osFS reads the real file system.
type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error) {
	return assetfile.Read(name, FileLimit)
}

// ReadDir lists name unsorted: the index groups entries by stem, so
// the sort os.ReadDir does would be wasted.
func (osFS) ReadDir(name string) ([]fs.DirEntry, error) { return listDir(name) }

func (osFS) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(name)
}

// fileKind is what a directory entry is, as far as a lookup needs.
type fileKind uint8

const (
	// fileLink is a symlink not yet followed: themes link their sizes
	// and aliases, and a lookup stats one only when it asks for it.
	fileLink fileKind = iota + 1
	fileRegular
	fileOther
)

// slot is one directory a lookup may match in: a theme directory under
// one base directory, or a bare base directory for the unthemed
// fallback (dir nil). Slots are numbered in lookup order: theme of the
// chain, then base directory, then the theme's declaration order, the
// unthemed base directories last.
type slot struct {
	path  string
	dir   *Dir
	theme int
}

// hit is one slot's files for one icon stem, by extension.
type hit struct {
	kinds [len(extensions)]fileKind
	slot  int32
}

// resolveKey is one memoized resolution; the theme and search paths are
// the index's own.
type resolveKey struct {
	name  string
	size  int
	scale int
}

// maxResolved bounds the resolution memo: names come from applications
// and windows, so a long-running process could otherwise grow it
// without limit. Reaching it starts the memo over.
const maxResolved = 4096

// themeIndex is a theme chain's directories read once, as GTK's
// icon-theme.cache holds them: each icon stem maps to the slots that
// hold it, in lookup order, so a lookup visits only the directories
// that have the name instead of every directory of every theme under
// every base directory. A cache reset drops it, so icons installed
// since are found.
type themeIndex struct {
	mu    sync.Mutex
	fsys  themeFS
	chain []*Theme
	// scales lists, per theme of the chain, the directory scales it
	// declares.
	scales [][]int
	// themeEnd is, per theme of the chain, the first slot past it.
	themeEnd []int32
	slots    []slot
	stems    map[string][]hit
	resolved map[resolveKey]string
}

// listConcurrency bounds the directory reads an index build has in
// flight. A build is a thousand listings of directories that hold an
// icon or two, latency-bound on the file system, which serves a slow
// mount's round trips (a VM's shared store) in parallel.
const listConcurrency = 16

// forEachConcurrently runs f for every index below n, at most
// listConcurrency at once, and returns once all have.
func forEachConcurrently(n int, f func(i int)) {
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(n, listConcurrency) {
		wg.Go(func() {
			for i := int(next.Add(1) - 1); i < n; i = int(next.Add(1) - 1) {
				f(i)
			}
		})
	}
	wg.Wait()
}

// plannedSlot is a slot of an index being built, and which of the
// build's listings holds its entries.
type plannedSlot struct {
	slot    slot
	listing int
}

// buildThemeIndex parses the chain of theme under baseDirs and lists
// each of its directories once. A theme root missing from a base
// directory skips all its directories there, and a directory whose top
// component the root does not list is never opened. The roots, and
// then the directories, are read concurrently; the index holds them in
// lookup order whatever order the reads end in.
func buildThemeIndex(fsys themeFS, theme string, baseDirs []string) *themeIndex {
	ix := &themeIndex{
		fsys:     fsys,
		chain:    themeChain(fsys, theme, baseDirs),
		stems:    map[string][]hit{},
		resolved: map[resolveKey]string{},
	}
	roots := make([]themeRootListing, len(ix.chain)*len(baseDirs))
	forEachConcurrently(len(roots), func(i int) {
		t, base := ix.chain[i/len(baseDirs)], baseDirs[i%len(baseDirs)]
		roots[i] = themeRoot(fsys, filepath.Join(base, t.DirName))
	})
	var (
		planned []plannedSlot
		paths   []string
	)
	listing := map[string]int{}
	plan := func(s slot) {
		n, ok := listing[s.path]
		if !ok {
			n = len(paths)
			listing[s.path] = n
			paths = append(paths, s.path)
		}
		planned = append(planned, plannedSlot{slot: s, listing: n})
	}
	for ti, t := range ix.chain {
		var scales []int
		for i := range t.Dirs {
			if !slices.Contains(scales, t.Dirs[i].Scale) {
				scales = append(scales, t.Dirs[i].Scale)
			}
		}
		ix.scales = append(ix.scales, scales)
		for bi, base := range baseDirs {
			root := roots[ti*len(baseDirs)+bi]
			if root.skip {
				continue
			}
			for i := range t.Dirs {
				d := &t.Dirs[i]
				if first, ok := topComponent(d.Path); root.prune && ok && !root.top[first] {
					continue
				}
				plan(slot{path: filepath.Join(base, t.DirName, d.Path), dir: d, theme: ti})
			}
		}
		ix.themeEnd = append(ix.themeEnd, int32(len(planned))) //nolint:gosec // slots are directories, far below 2^31
	}
	for _, base := range baseDirs {
		plan(slot{path: filepath.Join(base, "", ""), theme: len(ix.chain)})
	}
	listings := make([][]fs.DirEntry, len(paths))
	forEachConcurrently(len(paths), func(i int) {
		listings[i], _ = fsys.ReadDir(paths[i])
	})
	ix.slots = make([]slot, 0, len(planned))
	for _, p := range planned {
		ix.add(p.slot, listings[p.listing])
	}
	return ix
}

// themeRootListing is a theme's root directory under one base
// directory: skip reports that it does not exist there; prune that top
// holds its entries, so a directory whose top component is missing need
// not be opened. An unreadable root prunes nothing.
type themeRootListing struct {
	top         map[string]bool
	prune, skip bool
}

func themeRoot(fsys themeFS, root string) themeRootListing {
	des, err := fsys.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return themeRootListing{skip: true}
	}
	if err != nil {
		return themeRootListing{}
	}
	top := make(map[string]bool, len(des))
	for _, de := range des {
		top[de.Name()] = true
	}
	return themeRootListing{top: top, prune: true}
}

// topComponent returns the first path component of a theme directory;
// ok is false when the directory is not below the theme root, so its
// root listing cannot rule it out.
func topComponent(dir string) (string, bool) {
	first, _, _ := strings.Cut(filepath.Clean(dir), string(filepath.Separator))
	return first, first != "" && first != "." && first != ".."
}

// add appends slot s and indexes its entries: regular files and links
// with an icon extension, by stem.
func (ix *themeIndex) add(s slot, des []fs.DirEntry) {
	n := int32(len(ix.slots)) //nolint:gosec // slots are directories, far below 2^31
	ix.slots = append(ix.slots, s)
	for _, de := range des {
		var kind fileKind
		switch t := de.Type(); {
		case t.IsRegular():
			kind = fileRegular
		case t&fs.ModeSymlink != 0:
			kind = fileLink
		default:
			continue
		}
		stem, ext, ok := splitIconFile(de.Name())
		if !ok {
			continue
		}
		hits := ix.stems[stem]
		if len(hits) == 0 || hits[len(hits)-1].slot != n {
			hits = append(hits, hit{slot: n})
		}
		hits[len(hits)-1].kinds[ext] = kind
		ix.stems[stem] = hits
	}
}

// splitIconFile splits a file name into its stem and the index of its
// icon extension; ok is false for any other file.
func splitIconFile(name string) (string, int, bool) {
	for i, ext := range extensions {
		if stem, ok := strings.CutSuffix(name, ext); ok {
			return stem, i, true
		}
	}
	return "", 0, false
}

// resolve returns the path name resolves to at size and directory
// scale, "" when nothing holds it, memoized.
func (ix *themeIndex) resolve(name string, size, scale int) string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	key := resolveKey{name: name, size: size, scale: scale}
	if path, ok := ix.resolved[key]; ok {
		return path
	}
	path := ix.find(name, size, scale)
	if len(ix.resolved) >= maxResolved {
		clear(ix.resolved)
	}
	ix.resolved[key] = path
	return path
}

// find runs the spec lookup over the slots holding name or, for a
// -symbolic name, its unsuffixed base, which themes often ship alone:
// each theme of the chain, then the unthemed base directories, then the
// bundled set. A plain name nothing holds then tries its -symbolic form,
// the only one newer themes ship of many names, as Adwaita does
// audio-volume-medium; GTK falls back the same way.
func (ix *themeIndex) find(name string, size, scale int) string {
	alt, symbolic := strings.CutSuffix(name, symbolicSuffix)
	st := stems{name: name, nameHits: ix.stems[name]}
	if symbolic {
		st.alt, st.altHits = alt, ix.stems[alt]
	}
	for ti := range ix.chain {
		theme, rest := st.split(ix.themeEnd[ti])
		st = rest
		if theme.empty() {
			continue
		}
		if path := ix.lookupTheme(ti, theme, size, scale); path != "" {
			return path
		}
	}
	for sh := range st.slots() {
		if path := ix.file(sh); path != "" {
			return path
		}
	}
	if path := lookupBuiltin(name); path != "" || symbolic || name == "" {
		return path
	}
	return ix.find(name+symbolicSuffix, size, scale)
}

// lookupTheme runs both spec passes for one theme. A theme that has no
// directory at the requested scale falls back to its scale-1
// directories (the raster then upscales into the larger device box),
// matching how GTK serves scale-1-only themes on hidpi. A pass at a
// scale the theme does not declare cannot match and is skipped.
func (ix *themeIndex) lookupTheme(ti int, st stems, size, scale int) string {
	if slices.Contains(ix.scales[ti], scale) {
		if path := ix.lookupThemeScale(st, size, scale); path != "" {
			return path
		}
	}
	if scale != 1 && slices.Contains(ix.scales[ti], 1) {
		return ix.lookupThemeScale(st, size, 1)
	}
	return ""
}

// lookupThemeScale runs the exact pass then the nearest pass at one
// integer directory scale over one theme's slots.
func (ix *themeIndex) lookupThemeScale(st stems, size, scale int) string {
	for sh := range st.slots() {
		if !ix.slots[sh.slot].dir.matches(size, scale) {
			continue
		}
		if path := ix.file(sh); path != "" {
			return path
		}
	}
	best := ""
	bestDist := 0
	for sh := range st.slots() {
		d := ix.slots[sh.slot].dir
		if d.Scale != scale {
			continue
		}
		dist := d.distance(size)
		if best != "" && dist >= bestDist {
			continue
		}
		if path := ix.file(sh); path != "" {
			best, bestDist = path, dist
		}
	}
	return best
}

// file returns the first existing file a slot holds for the name, the
// name's own stem before its unsuffixed base, each in extension order.
// A link is followed once, on first ask.
func (ix *themeIndex) file(sh slotHits) string {
	dir := ix.slots[sh.slot].path
	for _, c := range [...]struct {
		h    *hit
		stem string
	}{{sh.name, sh.st.name}, {sh.alt, sh.st.alt}} {
		if c.h == nil {
			continue
		}
		for i, ext := range extensions {
			if ix.isFile(&c.h.kinds[i], dir, c.stem+ext) {
				return filepath.Join(dir, c.stem+ext)
			}
		}
	}
	return ""
}

// isFile reports whether the entry of kind *k, dir/file, is (or links
// to) a regular file, recording a followed link's target kind.
func (ix *themeIndex) isFile(k *fileKind, dir, file string) bool {
	switch *k {
	case fileRegular:
		return true
	case fileLink:
		*k = fileOther
		if info, err := ix.fsys.Stat(filepath.Join(dir, file)); err == nil && info.Mode().IsRegular() {
			*k = fileRegular
		}
		return *k == fileRegular
	}
	return false
}

// stems is a lookup's two candidate stems with their hits, each in
// slot order.
type stems struct {
	name, alt         string
	nameHits, altHits []hit
}

func (st stems) empty() bool {
	return len(st.nameHits) == 0 && len(st.altHits) == 0
}

// split cuts st at slot end: the hits before it, and the rest.
func (st stems) split(end int32) (stems, stems) {
	before := func(hits []hit) int {
		return sort.Search(len(hits), func(i int) bool { return hits[i].slot >= end })
	}
	head, tail := st, st
	n, a := before(st.nameHits), before(st.altHits)
	head.nameHits, tail.nameHits = st.nameHits[:n], st.nameHits[n:]
	head.altHits, tail.altHits = st.altHits[:a], st.altHits[a:]
	return head, tail
}

// slotHits is one slot holding either candidate stem.
type slotHits struct {
	st        *stems
	name, alt *hit
	slot      int32
}

// slots yields the slots holding either stem, in slot order.
func (st *stems) slots() iter.Seq[slotHits] {
	return func(yield func(slotHits) bool) {
		a, b := st.nameHits, st.altHits
		for len(a) > 0 || len(b) > 0 {
			sh := slotHits{st: st}
			switch {
			case len(b) == 0 || (len(a) > 0 && a[0].slot < b[0].slot):
				sh.name, sh.slot, a = &a[0], a[0].slot, a[1:]
			case len(a) == 0 || b[0].slot < a[0].slot:
				sh.alt, sh.slot, b = &b[0], b[0].slot, b[1:]
			default:
				sh.name, sh.alt, sh.slot, a, b = &a[0], &b[0], a[0].slot, a[1:], b[1:]
			}
			if !yield(sh) {
				return
			}
		}
	}
}

// symbolicSuffix marks a symbolic icon name.
const symbolicSuffix = "-symbolic"

// FallbackName is the first of name and its dash-shortened forms that
// resolves, GThemedIcon's default fallbacks: a missing
// preferences-system-network tries preferences-system, then
// preferences. A -symbolic suffix stays on every form. It returns name
// itself when none resolves. It may read the disk; the answer is kept
// for PeekFallback until a reset.
func (c *Cache) FallbackName(name string) string {
	c.mu.Lock()
	if known, ok := c.fallbacks[name]; ok {
		c.mu.Unlock()
		return known
	}
	gen := c.gen.Load()
	c.mu.Unlock()
	found := c.fallbackOf(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen.Load() == gen {
		if len(c.fallbacks) >= maxResolved {
			clear(c.fallbacks)
		}
		c.fallbacks[name] = found
	}
	return found
}

// PeekFallback returns FallbackName's answer if it is known, reading
// nothing.
func (c *Cache) PeekFallback(name string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	known, ok := c.fallbacks[name]
	return known, ok
}

func (c *Cache) fallbackOf(name string) string {
	base, suffix := strings.CutSuffix(name, symbolicSuffix)
	tail := ""
	if suffix {
		tail = symbolicSuffix
	}
	for n := base; n != ""; {
		if _, err := c.resolve(n+tail, 16, 120); err == nil {
			return n + tail
		}
		i := strings.LastIndexByte(n, '-')
		if i < 0 {
			break
		}
		n = n[:i]
	}
	return name
}
