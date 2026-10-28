// Package xdg resolves the XDG base directories and user directories:
// the base directories themselves, and an app's own directory under
// each, all read from an [Env].
package xdg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Env is the environment the directories are resolved from: it maps a
// variable's name to its value, "" when unset, as [os.Getenv] does.
// Tests pass their own to leave the process environment alone.
type Env func(name string) string

// OS is the process environment.
var OS Env = os.Getenv

// StateDir is app's state directory, $XDG_STATE_HOME/app or
// ~/.local/state/app; false when neither variable holds an absolute
// path. It panics on an app that is not a single path element.
func (e Env) StateDir(app string) (string, bool) {
	return e.appDir("XDG_STATE_HOME", ".local/state", app)
}

// CacheDir is app's cache directory, $XDG_CACHE_HOME/app or
// ~/.cache/app; false when neither variable holds an absolute path. It
// panics on an app that is not a single path element.
func (e Env) CacheDir(app string) (string, bool) {
	return e.appDir("XDG_CACHE_HOME", ".cache", app)
}

// DataDir is app's data directory, $XDG_DATA_HOME/app or
// ~/.local/share/app; false when neither variable holds an absolute
// path. It panics on an app that is not a single path element.
func (e Env) DataDir(app string) (string, bool) {
	return e.appDir("XDG_DATA_HOME", ".local/share", app)
}

// ConfigDir is app's configuration directory, $XDG_CONFIG_HOME/app or
// ~/.config/app; false when neither variable holds an absolute path.
// It panics on an app that is not a single path element.
func (e Env) ConfigDir(app string) (string, bool) {
	return e.appDir("XDG_CONFIG_HOME", ".config", app)
}

// CacheHome is the user's cache directory itself, $XDG_CACHE_HOME or
// ~/.cache, where caches shared across apps live (the thumbnails);
// false when neither variable holds an absolute path.
func (e Env) CacheHome() (string, bool) { return e.base("XDG_CACHE_HOME", ".cache") }

// DataHome is the user's data directory itself, $XDG_DATA_HOME or
// ~/.local/share, where data shared across apps lives (the trash, the
// recently-used list); false when neither variable holds an absolute
// path.
func (e Env) DataHome() (string, bool) { return e.base("XDG_DATA_HOME", ".local/share") }

// ConfigHome is the user's configuration directory itself,
// $XDG_CONFIG_HOME or ~/.config, where configuration shared across
// apps lives (user-dirs.dirs); false when neither variable holds an
// absolute path.
func (e Env) ConfigHome() (string, bool) { return e.base("XDG_CONFIG_HOME", ".config") }

// RuntimeDir is app's runtime directory, $XDG_RUNTIME_DIR/app, for
// sockets and markers that live as long as the login; false when the
// variable holds no absolute path, as the spec defines no fallback. It
// panics on an app that is not a single path element.
func (e Env) RuntimeDir(app string) (string, bool) {
	checkApp(app)
	dir, ok := e.RuntimeHome()
	if !ok {
		return "", false
	}
	return filepath.Join(dir, app), true
}

// RuntimeHome is the user's runtime directory itself, $XDG_RUNTIME_DIR;
// false when it holds no absolute path.
func (e Env) RuntimeHome() (string, bool) {
	dir := e.abs("XDG_RUNTIME_DIR")
	return dir, dir != ""
}

// ConfigDirs are the system configuration directories, most important
// first: the absolute entries of $XDG_CONFIG_DIRS, or /etc/xdg when it
// names none.
func (e Env) ConfigDirs() []string {
	var dirs []string
	for _, d := range filepath.SplitList(e("XDG_CONFIG_DIRS")) {
		if filepath.IsAbs(d) {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) == 0 {
		return []string{"/etc/xdg"}
	}
	return dirs
}

func (e Env) appDir(env, fallback, app string) (string, bool) {
	checkApp(app)
	dir, ok := e.base(env, fallback)
	if !ok {
		return "", false
	}
	return filepath.Join(dir, app), true
}

func checkApp(app string) {
	if app == "" || app == "." || app == ".." || strings.ContainsRune(app, filepath.Separator) {
		panic(fmt.Sprintf("xdg: app %q is not a single path element", app))
	}
}

func (e Env) base(env, fallback string) (string, bool) {
	if dir := e.abs(env); dir != "" {
		return dir, true
	}
	home := e.abs("HOME")
	if home == "" {
		return "", false
	}
	return filepath.Join(home, fallback), true
}

func (e Env) abs(name string) string {
	if dir := e(name); filepath.IsAbs(dir) {
		return dir
	}
	return ""
}
