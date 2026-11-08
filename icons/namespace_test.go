package icons

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const namespaceDir = "hicolor/scalable/actions"

func namespaceTree(t *testing.T) (first, second string) {
	t.Helper()
	first, second = t.TempDir(), t.TempDir()
	for _, file := range []string{
		filepath.Join(first, namespaceDir, "ld-both-symbolic.svg"),
		filepath.Join(second, namespaceDir, "ld-both-symbolic.svg"),
		filepath.Join(second, namespaceDir, "ld-second-symbolic.svg"),
		filepath.Join(second, namespaceDir, "unowned-icon.svg"),
		filepath.Join(second, "testtheme", "24x24", "apps", "ld-themed-symbolic.svg"),
	} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("<svg/>"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index := "[Icon Theme]\nName=testtheme\nDirectories=24x24/apps\n\n[24x24/apps]\nSize=24\nType=Fixed\n"
	if err := os.WriteFile(filepath.Join(second, "testtheme", "index.theme"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gone", filepath.Join(first, namespaceDir, "ld-dangling-symbolic.svg")); err != nil {
		t.Fatal(err)
	}
	return first, second
}

func namespaced(t *testing.T) (c *Cache, fsys *countingFS, first, second string) {
	t.Helper()
	first, second = namespaceTree(t)
	c = New("testtheme")
	c.SetSearchPaths([]string{first, second})
	c.SetNamespace(namespaceDir, []string{"ld", "md"})
	fsys = &countingFS{}
	c.fsys = fsys
	return c, fsys, first, second
}

func TestANamespacedIconResolvesFromItsDirectoryAlone(t *testing.T) {
	c, fsys, first, second := namespaced(t)
	for name, want := range map[string]string{
		"ld-both-symbolic":   filepath.Join(first, namespaceDir, "ld-both-symbolic.svg"),
		"ld-second-symbolic": filepath.Join(second, namespaceDir, "ld-second-symbolic.svg"),
	} {
		got, err := c.Lookup(name, 24, 120)
		if err != nil || got != want {
			t.Errorf("Lookup(%s) = %q, %v, want %q", name, got, err, want)
		}
	}
	if fsys.n() != 2 {
		t.Errorf("two namespaced lookups made %d reads, want one listing per base directory", fsys.n())
	}
	if c.index != nil {
		t.Error("a namespaced lookup built the theme chain's index")
	}
}

func TestANamespacedNameItsDirectoryLacksResolvesAsUsual(t *testing.T) {
	c, _, _, second := namespaced(t)
	want := filepath.Join(second, "testtheme", "24x24", "apps", "ld-themed-symbolic.svg")
	if got, err := c.Lookup("ld-themed-symbolic", 24, 120); err != nil || got != want {
		t.Errorf("Lookup = %q, %v, want the theme's %q", got, err, want)
	}
	if _, err := c.Lookup("ld-nowhere-symbolic", 24, 120); !errors.Is(err, ErrNotFound) {
		t.Errorf("a name nothing holds: %v, want ErrNotFound", err)
	}
	if _, err := c.Lookup("ld-dangling-symbolic", 24, 120); !errors.Is(err, ErrNotFound) {
		t.Errorf("a dangling link: %v, want ErrNotFound", err)
	}
}

func TestANameOutsideTheNamespaceBuildsTheIndex(t *testing.T) {
	c, _, _, _ := namespaced(t)
	if _, err := c.Lookup("unowned-icon", 24, 120); !errors.Is(err, ErrNotFound) {
		t.Errorf("a name outside the namespace resolved from its directory: %v", err)
	}
	if c.index == nil {
		t.Error("a name outside the namespace did not build the index")
	}
	if c.namespaceIndex != nil {
		t.Error("a name outside the namespace listed the namespace's directories")
	}
}

func TestAPrefixIsTheWholeFirstComponent(t *testing.T) {
	n := iconNamespace{dir: namespaceDir, prefixes: []string{"ld"}}
	for name, want := range map[string]bool{
		"ld-x-symbolic":  true,
		"ldx-y-symbolic": false,
		"ld":             false,
		"":               false,
	} {
		if got := n.owns(name); got != want {
			t.Errorf("owns(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestANewlyInstalledNamespacedIconIsFoundAfterAReset(t *testing.T) {
	c, _, first, _ := namespaced(t)
	if _, err := c.Lookup("ld-late-symbolic", 24, 120); !errors.Is(err, ErrNotFound) {
		t.Fatalf("before installing: %v", err)
	}
	path := filepath.Join(first, namespaceDir, "ld-late-symbolic.svg")
	if err := os.WriteFile(path, []byte("<svg/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.InvalidateTheme()
	if got, err := c.Lookup("ld-late-symbolic", 24, 120); err != nil || got != path {
		t.Errorf("after installing: %q, %v, want %q", got, err, path)
	}
}

func TestChangingTheNamespaceDropsWhatItResolved(t *testing.T) {
	c, _, first, _ := namespaced(t)
	if _, err := c.Lookup("ld-both-symbolic", 24, 120); err != nil {
		t.Fatal(err)
	}
	gen := c.Generation()
	c.SetNamespace(namespaceDir, []string{"ld", "md"})
	if c.Generation() != gen {
		t.Error("setting the same namespace reset the cache")
	}
	c.SetNamespace("elsewhere", []string{"ld"})
	if c.Generation() == gen || c.namespaceIndex != nil {
		t.Error("changing the namespace kept what it resolved")
	}
	if err := os.MkdirAll(filepath.Join(first, "elsewhere"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Lookup("ld-both-symbolic", 24, 120); !errors.Is(err, ErrNotFound) {
		t.Errorf("under a directory without it: %v, want ErrNotFound", err)
	}
}

func BenchmarkFirstNamespacedLookup(b *testing.B) {
	paths, _ := benchTree(b)
	file := filepath.Join(paths[0], namespaceDir, "ld-bench-symbolic.svg")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("<svg/>"), 0o600); err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		prefixes []string
	}{{"chain", nil}, {"namespace", []string{"ld"}}} {
		b.Run(tc.name, func(b *testing.B) {
			c := New("hicolor")
			c.SetSearchPaths(paths)
			c.SetNamespace(namespaceDir, tc.prefixes)
			b.ReportAllocs()
			for b.Loop() {
				c.InvalidateTheme()
				if _, err := c.Lookup("ld-bench-symbolic", 16, 120); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
