package i18n

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
)

func catalogs(t *testing.T) map[string]*Catalog {
	t.Helper()
	out := map[string]*Catalog{}
	for name, parse := range map[string]func([]byte) (*Catalog, error){
		"pl.po": ParsePO, "pl.le.mo": ParseMO, "pl.be.mo": ParseMO,
	} {
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		c, err := parse(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = c
	}
	return out
}

func TestCatalogsTranslatePluralsContextsAndContinuations(t *testing.T) {
	for name, c := range catalogs(t) {
		if got := c.Get("Open"); got != "Otwórz" {
			t.Errorf("%s: Get(Open) = %q", name, got)
		}
		if got := c.GetCtx("menu", "Open"); got != "Otwórz…" {
			t.Errorf("%s: GetCtx(menu, Open) = %q", name, got)
		}
		for n, want := range map[int]string{1: "%d plik", 2: "%d pliki", 4: "%d pliki", 5: "%d plików", 12: "%d plików", 22: "%d pliki", 0: "%d plików"} {
			if got := c.GetN("%d file", "%d files", n); got != want {
				t.Errorf("%s: GetN(%d) = %q, want %q", name, n, got, want)
			}
		}
		if got := c.Get("Close"); got != "Close" {
			t.Errorf("%s: a fuzzy entry translated: %q", name, got)
		}
		if got := c.Get("Line one"); got != "Linia pierwsza\ndruga \"cytat\"" {
			t.Errorf("%s: continuation = %q", name, got)
		}
		if got := c.Get("Missing"); got != "Missing" {
			t.Errorf("%s: an unknown id = %q", name, got)
		}
		if got := c.GetN("%d apple", "%d apples", 3); got != "%d apples" {
			t.Errorf("%s: an unknown plural = %q", name, got)
		}
	}
}

func TestZeroCatalogIsTheIdentity(t *testing.T) {
	var c Catalog
	if c.Get("x") != "x" || c.GetN("a", "as", 2) != "as" || c.GetCtx("k", "y") != "y" {
		t.Error("the zero catalog translated")
	}
}

func TestPluralExpressions(t *testing.T) {
	for src, cases := range map[string]map[uint64]uint64{
		"(n != 1)": {0: 1, 1: 0, 2: 1},
		"n>1":      {0: 0, 1: 0, 2: 1},
		"0":        {5: 0},
		"n%10==1 && n%100!=11 ? 0 : n != 0 ? 1 : 2": {1: 0, 11: 1, 21: 0, 0: 2},
		"!(n==1)": {1: 0, 3: 1},
		"n/0":     {7: 0},
	} {
		f, err := compilePlural(src)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		for n, want := range cases {
			if got := f(n); got != want {
				t.Errorf("%q at n=%d = %d, want %d", src, n, got, want)
			}
		}
	}
	for _, bad := range []string{"", "n ==", "(n", "n ? 1", "x", "n == 1;"} {
		if _, err := compilePlural(bad); err == nil {
			t.Errorf("%q compiled", bad)
		}
	}
}

func TestLocalesExpandFromTheEnvironment(t *testing.T) {
	t.Setenv("LANGUAGE", "nb_NO:de")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "fr_FR.UTF-8@euro")
	t.Setenv("LANG", "en_US.UTF-8")
	want := []string{"nb_NO", "nb", "de", "fr_FR@euro", "fr_FR", "fr@euro", "fr"}
	if got := Locales(); !slices.Equal(got, want) {
		t.Errorf("Locales() = %v, want %v", got, want)
	}
	t.Setenv("LANGUAGE", "")
	t.Setenv("LC_MESSAGES", "C.UTF-8")
	if got := Locales(); len(got) != 0 {
		t.Errorf("the C locale gave %v", got)
	}
}

func TestLoadPicksTheFirstLocaleWithACatalog(t *testing.T) {
	po, err := os.ReadFile("testdata/pl.po")
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{"pl/LC_MESSAGES/app.po": {Data: po}, "xx/LC_MESSAGES/app.mo": {Data: []byte("junk")}}
	c, err := Load(fsys, "app", []string{"de", "pl_PL", "pl"})
	if err != nil || c.Get("Open") != "Otwórz" {
		t.Fatalf("Load = %v, %v", c, err)
	}
	if c, err := Load(fsys, "app", []string{"de"}); err != nil || c.Get("Open") != "Open" {
		t.Errorf("no catalog: %v, %v", c, err)
	}
	if _, err := Load(fsys, "app", []string{"xx"}); err == nil {
		t.Error("a corrupt catalog loaded")
	}
}

func TestLoadSystemSearchesTheDataHomeBeforeTheSystemDataDirs(t *testing.T) {
	user, err := os.ReadFile("testdata/pl.po")
	if err != nil {
		t.Fatal(err)
	}
	write := func(dataDir string, po []byte) {
		t.Helper()
		dir := filepath.Join(dataDir, "locale", "pl", "LC_MESSAGES")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "app.po"), po, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	home, empty, system := t.TempDir(), t.TempDir(), t.TempDir()
	write(home, user)
	write(system, []byte("msgid \"Open\"\nmsgstr \"System\"\n"))
	t.Setenv("XDG_DATA_DIRS", "relative:"+empty+":"+system)
	for _, c := range []struct{ dataHome, want string }{
		{home, "Otwórz"},
		{empty, "System"},
		{"relative", "System"},
	} {
		t.Setenv("XDG_DATA_HOME", c.dataHome)
		t.Setenv("HOME", empty)
		cat, err := LoadSystem("app", []string{"pl"})
		if err != nil {
			t.Fatalf("XDG_DATA_HOME=%s: %v", c.dataHome, err)
		}
		if got := cat.Get("Open"); got != c.want {
			t.Errorf("XDG_DATA_HOME=%s: Get(Open) = %q, want %q", c.dataHome, got, c.want)
		}
	}
}
