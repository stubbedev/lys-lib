package icons

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chainTree is a search path holding a configured theme that does not
// inherit Adwaita, Adwaita, and hicolor, each with a scalable
// directory and the named icons.
func chainTree(t *testing.T, icons map[string][]string) string {
	t.Helper()
	root := t.TempDir()
	for theme, names := range icons {
		dir := filepath.Join(root, theme, "scalable", "apps")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		index := "[Icon Theme]\nName=" + theme + "\nDirectories=scalable/apps\n\n[scalable/apps]\nSize=16\nType=Scalable\nMinSize=8\nMaxSize=512\n"
		if theme == "Mine" {
			index = strings.Replace(index, "Directories", "Inherits=hicolor\nDirectories", 1)
		}
		if err := os.WriteFile(filepath.Join(root, theme, "index.theme"), []byte(index), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(dir, n+".svg"), []byte("<svg/>"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// Lookups run the configured theme's chain, then Adwaita, GTK's
// default, then hicolor; with no theme set they start at Adwaita
// (#258).
func TestThemeChainFallsBackToAdwaitaThenHicolor(t *testing.T) {
	root := chainTree(t, map[string][]string{
		"Mine":    {"both"},
		"Adwaita": {"both", "user-desktop", "adw-and-hicolor"},
		"hicolor": {"adw-and-hicolor", "hicolor-only"},
	})
	from := func(theme, name string) string {
		c := New(theme)
		c.SetSearchPaths([]string{root})
		path, err := c.Lookup(name, 16, 120)
		if err != nil {
			t.Fatalf("theme %q: %s: %v", theme, name, err)
		}
		rel, _ := filepath.Rel(root, path)
		return strings.SplitN(rel, string(filepath.Separator), 2)[0]
	}
	for _, tc := range []struct{ theme, name, want string }{
		{"Mine", "both", "Mine"},
		{"Mine", "user-desktop", "Adwaita"},
		{"Mine", "adw-and-hicolor", "Adwaita"},
		{"Mine", "hicolor-only", "hicolor"},
		{"", "user-desktop", "Adwaita"},
	} {
		if got := from(tc.theme, tc.name); got != tc.want {
			t.Errorf("theme %q: %s from %s, want %s", tc.theme, tc.name, got, tc.want)
		}
	}
	if Default().Theme() != DefaultTheme {
		t.Errorf("the default cache starts on %q, want %q", Default().Theme(), DefaultTheme)
	}
}

// FallbackName shortens a missing name at its dashes until one
// resolves, keeping a -symbolic suffix; the exact lookup stays missing.
func TestFallbackName(t *testing.T) {
	root := chainTree(t, map[string][]string{"hicolor": {"preferences-system", "frob-symbolic"}})
	c := New("")
	c.SetSearchPaths([]string{root})
	if got := c.FallbackName("preferences-system-network"); got != "preferences-system" {
		t.Errorf("fallback for preferences-system-network = %q", got)
	}
	if got := c.FallbackName("frob-widget-large-symbolic"); got != "frob-symbolic" {
		t.Errorf("symbolic fallback = %q, want frob-symbolic", got)
	}
	if got := c.FallbackName("nothing-here"); got != "nothing-here" {
		t.Errorf("unresolvable fallback = %q, want the name itself", got)
	}
	if _, err := c.Lookup("preferences-system-network", 16, 120); err == nil {
		t.Error("the exact lookup of a missing name resolved")
	}
}
