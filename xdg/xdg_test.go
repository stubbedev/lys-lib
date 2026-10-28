package xdg

import "testing"

func TestAppDirs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dir      func(string) (string, bool)
		env      string
		fallback string
	}{
		{"state", StateDir, "XDG_STATE_HOME", "/home/u/.local/state/app"},
		{"cache", CacheDir, "XDG_CACHE_HOME", "/home/u/.cache/app"},
		{"data", DataDir, "XDG_DATA_HOME", "/home/u/.local/share/app"},
	} {
		t.Setenv(tc.env, "/x")
		t.Setenv("HOME", "/home/u")
		if got, ok := tc.dir("app"); !ok || got != "/x/app" {
			t.Errorf("%s with %s = %q, %v", tc.name, tc.env, got, ok)
		}
		t.Setenv(tc.env, "")
		if got, ok := tc.dir("app"); !ok || got != tc.fallback {
			t.Errorf("%s under HOME = %q, %v", tc.name, got, ok)
		}
		t.Setenv("HOME", "")
		if got, ok := tc.dir("app"); ok {
			t.Errorf("%s with neither = %q", tc.name, got)
		}
	}
}

func TestAppDirRefusesAnAppThatIsNotOneElement(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/x")
	for _, app := range []string{"", ".", "..", "a/b"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("StateDir(%q) did not panic", app)
				}
			}()
			StateDir(app)
		}()
	}
}

func TestRelativeVariablesAreIgnored(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "relative")
	t.Setenv("XDG_DATA_HOME", "relative")
	t.Setenv("HOME", "/home/u")
	if got, ok := StateDir("app"); !ok || got != "/home/u/.local/state/app" {
		t.Errorf("StateDir with a relative XDG_STATE_HOME = %q, %v", got, ok)
	}
	if got, ok := DataHome(); !ok || got != "/home/u/.local/share" {
		t.Errorf("DataHome with a relative XDG_DATA_HOME = %q, %v", got, ok)
	}
	t.Setenv("HOME", "home/u")
	if got, ok := DataHome(); ok {
		t.Errorf("DataHome with a relative HOME = %q", got)
	}
}

func TestCacheHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/c")
	if got, ok := CacheHome(); !ok || got != "/c" {
		t.Errorf("CacheHome = %q, %v", got, ok)
	}
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got, ok := CacheHome(); !ok || got != "/home/u/.cache" {
		t.Errorf("CacheHome under HOME = %q, %v", got, ok)
	}
	t.Setenv("HOME", "")
	if _, ok := CacheHome(); ok {
		t.Error("a cache home with neither")
	}
}

func TestDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/d")
	if got, ok := DataHome(); !ok || got != "/d" {
		t.Errorf("DataHome = %q, %v", got, ok)
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got, ok := DataHome(); !ok || got != "/home/u/.local/share" {
		t.Errorf("DataHome under HOME = %q, %v", got, ok)
	}
	t.Setenv("HOME", "")
	if _, ok := DataHome(); ok {
		t.Error("a data home with neither")
	}
}
