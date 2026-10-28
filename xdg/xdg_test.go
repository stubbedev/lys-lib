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
