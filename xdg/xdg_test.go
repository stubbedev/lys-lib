package xdg

import (
	"slices"
	"testing"
)

func env(vars map[string]string) Env {
	return func(name string) string { return vars[name] }
}

func TestAppDirs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dir      func(Env, string) (string, bool)
		env      string
		fallback string
	}{
		{"state", Env.StateDir, "XDG_STATE_HOME", "/home/u/.local/state/app"},
		{"cache", Env.CacheDir, "XDG_CACHE_HOME", "/home/u/.cache/app"},
		{"data", Env.DataDir, "XDG_DATA_HOME", "/home/u/.local/share/app"},
		{"config", Env.ConfigDir, "XDG_CONFIG_HOME", "/home/u/.config/app"},
	} {
		if got, ok := tc.dir(env(map[string]string{tc.env: "/x", "HOME": "/home/u"}), "app"); !ok || got != "/x/app" {
			t.Errorf("%s with %s = %q, %v", tc.name, tc.env, got, ok)
		}
		if got, ok := tc.dir(env(map[string]string{"HOME": "/home/u"}), "app"); !ok || got != tc.fallback {
			t.Errorf("%s under HOME = %q, %v", tc.name, got, ok)
		}
		if got, ok := tc.dir(env(map[string]string{tc.env: "relative", "HOME": "/home/u"}), "app"); !ok || got != tc.fallback {
			t.Errorf("%s with a relative %s = %q, %v", tc.name, tc.env, got, ok)
		}
		if got, ok := tc.dir(env(nil), "app"); ok {
			t.Errorf("%s with neither = %q", tc.name, got)
		}
		if got, ok := tc.dir(env(map[string]string{"HOME": "home/u"}), "app"); ok {
			t.Errorf("%s with a relative HOME = %q", tc.name, got)
		}
	}
}

func TestAppDirRefusesAnAppThatIsNotOneElement(t *testing.T) {
	e := env(map[string]string{"XDG_STATE_HOME": "/x", "XDG_RUNTIME_DIR": "/run/x"})
	for name, dir := range map[string]func(string) (string, bool){"StateDir": e.StateDir, "RuntimeDir": e.RuntimeDir} {
		for _, app := range []string{"", ".", "..", "a/b"} {
			func() {
				defer func() {
					if recover() == nil {
						t.Errorf("%s(%q) did not panic", name, app)
					}
				}()
				dir(app)
			}()
		}
	}
}

func TestHomes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		home     func(Env) (string, bool)
		env      string
		fallback string
	}{
		{"cache", Env.CacheHome, "XDG_CACHE_HOME", "/home/u/.cache"},
		{"data", Env.DataHome, "XDG_DATA_HOME", "/home/u/.local/share"},
		{"config", Env.ConfigHome, "XDG_CONFIG_HOME", "/home/u/.config"},
	} {
		if got, ok := tc.home(env(map[string]string{tc.env: "/x", "HOME": "/home/u"})); !ok || got != "/x" {
			t.Errorf("%s home with %s = %q, %v", tc.name, tc.env, got, ok)
		}
		if got, ok := tc.home(env(map[string]string{"HOME": "/home/u"})); !ok || got != tc.fallback {
			t.Errorf("%s home under HOME = %q, %v", tc.name, got, ok)
		}
		if got, ok := tc.home(env(map[string]string{tc.env: "relative", "HOME": "/home/u"})); !ok || got != tc.fallback {
			t.Errorf("%s home with a relative %s = %q, %v", tc.name, tc.env, got, ok)
		}
		if got, ok := tc.home(env(nil)); ok {
			t.Errorf("%s home with neither = %q", tc.name, got)
		}
		if got, ok := tc.home(env(map[string]string{"HOME": "home/u"})); ok {
			t.Errorf("%s home with a relative HOME = %q", tc.name, got)
		}
	}
}

func TestConfigDirs(t *testing.T) {
	for _, c := range []struct {
		dirs string
		want []string
	}{
		{"/a:/b", []string{"/a", "/b"}},
		{"/a:relative::/b", []string{"/a", "/b"}},
		{"relative", []string{"/etc/xdg"}},
		{"", []string{"/etc/xdg"}},
	} {
		if got := env(map[string]string{"XDG_CONFIG_DIRS": c.dirs}).ConfigDirs(); !slices.Equal(got, c.want) {
			t.Errorf("XDG_CONFIG_DIRS=%q: ConfigDirs = %q, want %q", c.dirs, got, c.want)
		}
	}
}

func TestDataDirs(t *testing.T) {
	for _, c := range []struct {
		dirs string
		want []string
	}{
		{"/a:/b", []string{"/a", "/b"}},
		{"/a:relative::/b:/a", []string{"/a", "/b", "/a"}},
		{"relative", []string{"/usr/local/share", "/usr/share"}},
		{"", []string{"/usr/local/share", "/usr/share"}},
	} {
		if got := env(map[string]string{"XDG_DATA_DIRS": c.dirs}).DataDirs(); !slices.Equal(got, c.want) {
			t.Errorf("XDG_DATA_DIRS=%q: DataDirs = %q, want %q", c.dirs, got, c.want)
		}
	}
}

func TestSearchPaths(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     func(Env) []string
		home     string
		dirs     string
		fallback []string
	}{
		{"data", Env.DataSearchPath, "XDG_DATA_HOME", "XDG_DATA_DIRS", []string{"/home/u/.local/share", "/usr/local/share", "/usr/share"}},
		{"config", Env.ConfigSearchPath, "XDG_CONFIG_HOME", "XDG_CONFIG_DIRS", []string{"/home/u/.config", "/etc/xdg"}},
	} {
		for _, c := range []struct {
			vars map[string]string
			want []string
		}{
			{map[string]string{tc.home: "/x", tc.dirs: "/a:/b", "HOME": "/home/u"}, []string{"/x", "/a", "/b"}},
			{map[string]string{tc.home: "/x", tc.dirs: "/a:relative::/x:/a"}, []string{"/x", "/a", "/x", "/a"}},
			{map[string]string{tc.home: "relative", tc.dirs: "relative", "HOME": "/home/u"}, tc.fallback},
			{map[string]string{"HOME": "/home/u"}, tc.fallback},
			{map[string]string{tc.dirs: "/a"}, []string{"/a"}},
			{map[string]string{"HOME": "home/u"}, tc.fallback[1:]},
		} {
			if got := tc.path(env(c.vars)); !slices.Equal(got, c.want) {
				t.Errorf("%s search path of %v = %q, want %q", tc.name, c.vars, got, c.want)
			}
		}
	}
}

func TestDirListsAreTheCallersToChange(t *testing.T) {
	e := env(nil)
	for name, list := range map[string]func() []string{
		"DataDirs": e.DataDirs, "ConfigDirs": e.ConfigDirs, "DataSearchPath": e.DataSearchPath, "ConfigSearchPath": e.ConfigSearchPath,
	} {
		want := slices.Clone(list())
		got := list()
		for i := range got {
			got[i] = "/changed"
		}
		if again := list(); !slices.Equal(again, want) {
			t.Errorf("%s after a caller changed its result = %q, want %q", name, again, want)
		}
	}
}

func TestSearchPathAllocatesItsListOnce(t *testing.T) {
	vars := map[string]string{"XDG_DATA_HOME": "/x", "XDG_DATA_DIRS": "/a:/b:/c:/d"}
	e := Env(func(name string) string { return vars[name] })
	if allocs := testing.AllocsPerRun(100, func() { _ = e.DataSearchPath() }); allocs != 1 {
		t.Errorf("DataSearchPath made %v allocations, want 1", allocs)
	}
}

func TestHome(t *testing.T) {
	if got, ok := env(map[string]string{"HOME": "/home/u"}).Home(); !ok || got != "/home/u" {
		t.Errorf("Home = %q, %v", got, ok)
	}
	for _, home := range []string{"", "home/u"} {
		if got, ok := env(map[string]string{"HOME": home}).Home(); ok {
			t.Errorf("Home with HOME=%q = %q", home, got)
		}
	}
}

func TestRuntimeDir(t *testing.T) {
	for _, c := range []struct {
		vars     map[string]string
		dir      string
		home     string
		resolved bool
	}{
		{map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000", "HOME": "/home/u"}, "/run/user/1000/app", "/run/user/1000", true},
		{map[string]string{"XDG_RUNTIME_DIR": "run/user/1000", "HOME": "/home/u"}, "", "", false},
		{map[string]string{"HOME": "/home/u"}, "", "", false},
	} {
		e := env(c.vars)
		if dir, ok := e.RuntimeDir("app"); dir != c.dir || ok != c.resolved {
			t.Errorf("%v: RuntimeDir = %q, %v; want %q, %v", c.vars, dir, ok, c.dir, c.resolved)
		}
		if home, ok := e.RuntimeHome(); home != c.home || ok != c.resolved {
			t.Errorf("%v: RuntimeHome = %q, %v; want %q, %v", c.vars, home, ok, c.home, c.resolved)
		}
	}
}

func TestOSReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/from-process")
	if got, ok := OS.ConfigDir("app"); !ok || got != "/from-process/app" {
		t.Errorf("OS.ConfigDir = %q, %v", got, ok)
	}
}
