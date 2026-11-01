package fontscan

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFontconfigPathIsSearchedBeforeTheDefault(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(second, "fonts.conf"), []byte("<fontconfig/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FONTCONFIG_FILE", "")
	if err := os.Unsetenv("FONTCONFIG_FILE"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FONTCONFIG_PATH", first+string(os.PathListSeparator)+second)
	vars := fcVarsFromEnv()
	vars.deps = &fcDeps{seen: map[string]bool{}}
	if want := []string{first, second, defaultFontconfigDir}; !slices.Equal(vars.paths, want) {
		t.Errorf("paths %q, want %q", vars.paths, want)
	}
	if got, want := vars.resolveRoot(discardLogger{}), filepath.Join(second, "fonts.conf"); got != want {
		t.Errorf("root %q, want %q", got, want)
	}
}

func TestFontconfigSearchPath(t *testing.T) {
	sep := string(os.PathListSeparator)
	for env, want := range map[string][]string{
		"":                      {defaultFontconfigDir},
		"/a":                    {"/a", defaultFontconfigDir},
		sep + "/a" + sep:        {"/a", defaultFontconfigDir},
		"/a" + sep + sep + "/b": {"/a", "/b", defaultFontconfigDir},
	} {
		if got := fontconfigSearchPath(env); !slices.Equal(got, want) {
			t.Errorf("%q: %q, want %q", env, got, want)
		}
	}
}

func TestAChangedFontconfigPathIsNotServedTheCachedConfiguration(t *testing.T) {
	t.Setenv("FONTCONFIG_PATH", "/a")
	before := fcVarsFromEnv().environmentKey()
	t.Setenv("FONTCONFIG_PATH", "/b")
	if fcVarsFromEnv().environmentKey() == before {
		t.Error("two values of FONTCONFIG_PATH share a cached configuration")
	}
}
