package xdg

import "testing"

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
	e := env(map[string]string{"XDG_STATE_HOME": "/x"})
	for _, app := range []string{"", ".", "..", "a/b"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("StateDir(%q) did not panic", app)
				}
			}()
			e.StateDir(app)
		}()
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

func TestOSReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/from-process")
	if got, ok := OS.ConfigDir("app"); !ok || got != "/from-process/app" {
		t.Errorf("OS.ConfigDir = %q, %v", got, ok)
	}
}
