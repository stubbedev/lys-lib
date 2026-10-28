// Package xdg resolves the XDG base directories and user directories:
// the base directories themselves, and an app's own directory under
// each.
package xdg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StateDir is app's state directory, $XDG_STATE_HOME/app or
// ~/.local/state/app; false when neither variable holds an absolute
// path. It panics on an app that is not a single path element.
func StateDir(app string) (string, bool) { return appDir("XDG_STATE_HOME", ".local/state", app) }

// CacheDir is app's cache directory, $XDG_CACHE_HOME/app or
// ~/.cache/app; false when neither variable holds an absolute path. It
// panics on an app that is not a single path element.
func CacheDir(app string) (string, bool) { return appDir("XDG_CACHE_HOME", ".cache", app) }

// DataDir is app's data directory, $XDG_DATA_HOME/app or
// ~/.local/share/app; false when neither variable holds an absolute
// path. It panics on an app that is not a single path element.
func DataDir(app string) (string, bool) { return appDir("XDG_DATA_HOME", ".local/share", app) }

// CacheHome is the user's cache directory itself, $XDG_CACHE_HOME or
// ~/.cache, where caches shared across apps live (the thumbnails);
// false when neither variable holds an absolute path.
func CacheHome() (string, bool) { return base("XDG_CACHE_HOME", ".cache") }

// DataHome is the user's data directory itself, $XDG_DATA_HOME or
// ~/.local/share, where data shared across apps lives (the trash, the
// recently-used list); false when neither variable holds an absolute
// path.
func DataHome() (string, bool) { return base("XDG_DATA_HOME", ".local/share") }

func appDir(env, fallback, app string) (string, bool) {
	if app == "" || app == "." || app == ".." || strings.ContainsRune(app, filepath.Separator) {
		panic(fmt.Sprintf("xdg: app %q is not a single path element", app))
	}
	dir, ok := base(env, fallback)
	if !ok {
		return "", false
	}
	return filepath.Join(dir, app), true
}

func base(env, fallback string) (string, bool) {
	if dir := absEnv(env); dir != "" {
		return dir, true
	}
	home := absEnv("HOME")
	if home == "" {
		return "", false
	}
	return filepath.Join(home, fallback), true
}

func absEnv(name string) string {
	if dir := os.Getenv(name); filepath.IsAbs(dir) {
		return dir
	}
	return ""
}
