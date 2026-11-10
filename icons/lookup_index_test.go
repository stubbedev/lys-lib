package icons

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stubbedev/lys-lib/testbuild"
	"github.com/stubbedev/lys-lib/xdg"
)

var (
	equivSizes = []int{16, 24, 32, 48}
	equivFracs = []uint32{120, 240}
)

// equivTree extends the fixture tree with what the index must get
// right beyond it: a second base directory that overrides some files
// and adds a scale-2-only theme, links alive and dangling, a -symbolic
// name whose theme ships only the plain glyph or both side by side, PNG and SVG side by side,
// unthemed files, and two directories equally far from 24.
func equivTree(t *testing.T) []string {
	t.Helper()
	root := writeTree(t)
	second := t.TempDir()
	write := func(base, rel, data string) {
		t.Helper()
		path := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, path string) {
		t.Helper()
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	write(second, "testtheme/32x32/apps/flag.png", "png")
	write(second, "testtheme/24x24/apps/second.svg", "svg")
	write(second, "testtheme/24x24/apps/second.png", "png")
	write(second, "hicolor/16x16/apps/go-up.svg", "svg")
	write(second, "hicolor/22x22/apps/go-up-symbolic.svg", "svg")
	write(second, "hicolor/48x48/apps/big.png", "png")
	write(second, "pixmap.png", "png")
	write(second, "flag.svg", "svg")
	write(second, "hidpi/index.theme", "[Icon Theme]\nName=HiDPI\nInherits=testtheme\nDirectories=16x16@2/apps,32x32@2/apps\n\n[16x16@2/apps]\nSize=16\nScale=2\nType=Fixed\n\n[32x32@2/apps]\nSize=32\nScale=2\nType=Fixed\n")
	write(second, "ties/index.theme", "[Icon Theme]\nName=Ties\nInherits=hidpi\nDirectories=8x8/apps,40x40/apps\n\n[8x8/apps]\nSize=8\nType=Fixed\n\n[40x40/apps]\nSize=40\nType=Fixed\n")
	write(second, "ties/8x8/apps/tie.svg", "svg")
	write(second, "ties/40x40/apps/tie.svg", "svg")
	write(second, "hidpi/16x16@2/apps/flag.svg", "svg")
	write(second, "hidpi/32x32@2/apps/hidpionly.svg", "svg")
	apps := filepath.Join(root, "hicolor", "16x16", "apps")
	link("plain.svg", filepath.Join(apps, "linked.svg"))
	link("missing.svg", filepath.Join(apps, "dangling.svg"))
	link("missing.png", filepath.Join(apps, "half.png"))
	write(root, "hicolor/16x16/apps/half.svg", "svg")
	write(root, "hicolor/16x16/apps/both.png", "png")
	write(root, "hicolor/16x16/apps/both-symbolic.svg", "svg")
	if err := os.Mkdir(filepath.Join(apps, "dironly.svg"), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{second, root, second, filepath.Join(root, "absent")}
}

// iconNames lists every icon stem the theme chain and the unthemed base
// directories hold, the -symbolic form of each plain one, and names
// nothing holds or only the bundled set serves.
func iconNames(t *testing.T, chain []*Theme, paths []string) []string {
	t.Helper()
	seen := map[string]bool{"no-such-icon": true, "list-add": true, "list-add-symbolic": true, "": true, "-symbolic": true}
	var collect func(dir string, depth int)
	collect = func(dir string, depth int) {
		des, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, de := range des {
			path := filepath.Join(dir, de.Name())
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				if depth > 0 {
					collect(path, depth-1)
				}
				continue
			}
			if stem, _, ok := splitIconFile(de.Name()); ok {
				seen[stem] = true
			}
		}
	}
	for _, base := range paths {
		collect(base, 0)
		for _, th := range chain {
			collect(filepath.Join(base, th.DirName), maxThemeDepth)
		}
	}
	for name := range seen {
		if !strings.HasSuffix(name, symbolicSuffix) {
			seen[name+symbolicSuffix] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// maxThemeDepth is how deep below a theme root icon directories nest:
// "scalable/apps", "stock/chart" under a size.
const maxThemeDepth = 3

type equivCase struct {
	name string
	size int
	frac uint32
}

// assertEquivalent resolves every name at every size and scale through
// the index and through the reference walk, and fails on any
// difference. It returns how many lookups it compared.
func assertEquivalent(t *testing.T, theme string, paths []string) int {
	t.Helper()
	c := New(theme)
	c.SetSearchPaths(paths)
	chain := themeChain(osFS{}, theme, paths)
	names := iconNames(t, chain, paths)
	cases := make([]equivCase, 0, len(names)*len(equivSizes)*len(equivFracs))
	got := make([]string, 0, cap(cases))
	for _, name := range names {
		for _, size := range equivSizes {
			for _, frac := range equivFracs {
				cases = append(cases, equivCase{name, size, frac})
				path, err := c.Lookup(name, size, frac)
				if err != nil && !errors.Is(err, ErrNotFound) {
					t.Fatalf("Lookup(%q, %d, %d): %v", name, size, frac, err)
				}
				got = append(got, path)
			}
		}
	}
	want := make([]string, len(cases))
	var wg sync.WaitGroup
	workers := runtime.GOMAXPROCS(0)
	for w := range workers {
		wg.Go(func() {
			ix := refDirIndex{}
			for i := w; i < len(cases); i += workers {
				want[i] = refResolve(ix, chain, paths, cases[i].name, cases[i].size, cases[i].frac)
			}
		})
	}
	wg.Wait()
	for i, cs := range cases {
		if got[i] != want[i] {
			t.Errorf("theme %q: %q at %d scale %d = %q, the reference walk gives %q", theme, cs.name, cs.size, cs.frac/fracDenom, got[i], want[i])
		}
	}
	return len(cases)
}

// The indexed lookup returns exactly what the walk it replaced returns
// (#394), over the fixture tree in every theme it declares.
func TestIndexedLookupMatchesWalkOnFixtures(t *testing.T) {
	paths := equivTree(t)
	for _, theme := range []string{"testtheme", "parent", "hicolor", "hidpi", "ties", "", "missing"} {
		n := assertEquivalent(t, theme, paths)
		t.Logf("theme %q: %d lookups compared", theme, n)
	}
}

// The same over the icon themes installed under XDG_DATA_DIRS, in the
// default chain: every icon name they hold.
func TestIndexedLookupMatchesWalkOnInstalledThemes(t *testing.T) {
	if testbuild.Race {
		t.Skip("the reference walk over every installed icon takes minutes under -race; the fixtures cover the race build")
	}
	paths := defaultSearchPaths(xdg.OS)
	if len(themeChain(osFS{}, DefaultTheme, paths)) == 0 {
		t.Skipf("no icon theme installed under %v", paths)
	}
	n := assertEquivalent(t, DefaultTheme, paths)
	t.Logf("default chain: %d lookups compared over %d search paths", n, len(paths))
}

// countingFS counts the reads a lookup makes.
type countingFS struct {
	osFS
	mu    sync.Mutex
	reads int
}

func (f *countingFS) count() {
	f.mu.Lock()
	f.reads++
	f.mu.Unlock()
}

func (f *countingFS) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func (f *countingFS) ReadFile(name string) ([]byte, error) {
	f.count()
	return f.osFS.ReadFile(name)
}

func (f *countingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	f.count()
	return f.osFS.ReadDir(name)
}

func (f *countingFS) Stat(name string) (fs.FileInfo, error) {
	f.count()
	return f.osFS.Stat(name)
}

// A repeated lookup, hit or miss, answers from the memo without reading
// the file system; the first one reads it (#394).
func TestRepeatLookupReadsNothing(t *testing.T) {
	paths := equivTree(t)
	c := New("testtheme")
	c.SetSearchPaths(paths)
	fsys := &countingFS{}
	c.fsys = fsys
	names := []string{"flag", "linked", "dangling", "face-symbolic", "no-such-icon", "list-add"}
	for _, name := range names {
		_, _ = c.Lookup(name, 24, 240)
	}
	if fsys.n() == 0 {
		t.Fatal("the first lookups read nothing: the counter does not see the index's reads")
	}
	before := fsys.n()
	for _, name := range names {
		_, _ = c.Lookup(name, 24, 240)
	}
	if got := fsys.n() - before; got != 0 {
		t.Errorf("warm repeat lookups made %d reads, want 0", got)
	}
	_, _ = c.Lookup("flag", 32, 240)
	if fsys.n() != before {
		t.Errorf("a new size of an indexed name read the file system %d times, want 0", fsys.n()-before)
	}
	t.Run("allocations", func(t *testing.T) {
		testbuild.SkipAllocBudget(t)
		if allocs := testing.AllocsPerRun(100, func() { _, _ = c.Lookup("flag", 24, 240) }); allocs != 0 {
			t.Errorf("a warm hit allocates %v times, want 0: it did not come from the memo", allocs)
		}
	})
}

// Every reset drops memoized resolutions: a changed theme or search
// path, or an invalidation, never serves the previous answer (#394).
func TestResetDropsMemoizedLookups(t *testing.T) {
	root := writeTree(t)
	other := chainTree(t, map[string][]string{"hicolor": {"flag"}})
	lookup := func(c *Cache, name string) string {
		t.Helper()
		path, err := c.Lookup(name, 16, 120)
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		return path
	}
	for _, tc := range []struct {
		name  string
		reset func(c *Cache)
		icon  string
		want  string
	}{
		{"SetTheme", func(c *Cache) { c.SetTheme("hicolor") }, "flag", filepath.Join(root, "hicolor", "16x16", "apps", "flag.svg")},
		{"ApplyIconTheme", func(c *Cache) { c.ApplyIconTheme("hicolor") }, "flag", filepath.Join(root, "hicolor", "16x16", "apps", "flag.svg")},
		{"SetSearchPaths", func(c *Cache) { c.SetSearchPaths([]string{other}) }, "flag", filepath.Join(other, "hicolor", "scalable", "apps", "flag.svg")},
		{"InvalidateTheme", func(c *Cache) {
			if err := os.Remove(filepath.Join(root, "testtheme", "16x16", "apps", "flag.png")); err != nil {
				t.Fatal(err)
			}
			c.InvalidateTheme()
		}, "flag", filepath.Join(root, "testtheme", "scalable", "apps", "flag.svg")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCacheAt(t, "testtheme", root)
			before := lookup(c, tc.icon)
			if before == tc.want {
				t.Fatalf("%q resolved to %q before the reset already", tc.icon, before)
			}
			if again := lookup(c, tc.icon); again != before {
				t.Fatalf("repeat lookup = %q, want the memoized %q", again, before)
			}
			tc.reset(c)
			if got := lookup(c, tc.icon); got != tc.want {
				t.Errorf("after %s: %q = %q, want %q", tc.name, tc.icon, got, tc.want)
			}
		})
	}
	t.Run("a memoized miss", func(t *testing.T) {
		c := newTestCacheAt(t, "testtheme", root)
		if got := lookup(c, "latecomer"); got != "" {
			t.Fatalf("latecomer = %q before it was installed", got)
		}
		late := filepath.Join(root, "hicolor", "16x16", "apps", "latecomer.svg")
		if err := os.WriteFile(late, []byte("<svg/>"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := lookup(c, "latecomer"); got != "" {
			t.Fatalf("latecomer = %q before an invalidation: the miss was not memoized", got)
		}
		c.InvalidateTheme()
		if got := lookup(c, "latecomer"); got != late {
			t.Errorf("latecomer after InvalidateTheme = %q, want %q", got, late)
		}
	})
}

// The memo is bounded: names past maxResolved start it over rather than
// growing it.
func TestResolutionMemoIsBounded(t *testing.T) {
	c := newTestCacheAt(t, "testtheme", writeTree(t))
	for i := range maxResolved + 10 {
		_, _ = c.Lookup(fmt.Sprintf("absent%d", i), 16, 120)
	}
	if n := len(c.index.resolved); n > maxResolved {
		t.Errorf("memo holds %d resolutions, want at most %d", n, maxResolved)
	}
	if path := mustLookup(t, c, "flag", 16, 120); filepath.Base(path) != "flag.png" {
		t.Errorf("flag after the memo restarted = %q, want flag.png", path)
	}
}
