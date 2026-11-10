// Package xdg resolves the XDG base directories and user directories:
// the base directories themselves, the search paths they form, and an
// app's own directory under each, all read from an [Env].
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

const listSeparator = string(filepath.ListSeparator)

type home struct{ env, fallback string }

type system struct {
	env      string
	fallback []string
}

var (
	stateHome  = home{"XDG_STATE_HOME", ".local/state"}
	cacheHome  = home{"XDG_CACHE_HOME", ".cache"}
	dataHome   = home{"XDG_DATA_HOME", ".local/share"}
	configHome = home{"XDG_CONFIG_HOME", ".config"}
	dataDirs   = system{"XDG_DATA_DIRS", []string{"/usr/local/share", "/usr/share"}}
	configDirs = system{"XDG_CONFIG_DIRS", []string{"/etc/xdg"}}
)

// Home is the user's home directory, $HOME; false when it holds no
// absolute path.
func (e Env) Home() (string, bool) {
	dir := e.abs("HOME")
	return dir, dir != ""
}

// StateDir is app's state directory, $XDG_STATE_HOME/app or
// ~/.local/state/app; false when neither variable holds an absolute
// path. It panics on an app that is not a single path element.
func (e Env) StateDir(app string) (string, bool) { return e.appDir(stateHome, app) }

// CacheDir is app's cache directory, $XDG_CACHE_HOME/app or
// ~/.cache/app; false when neither variable holds an absolute path. It
// panics on an app that is not a single path element.
func (e Env) CacheDir(app string) (string, bool) { return e.appDir(cacheHome, app) }

// DataDir is app's data directory, $XDG_DATA_HOME/app or
// ~/.local/share/app; false when neither variable holds an absolute
// path. It panics on an app that is not a single path element.
func (e Env) DataDir(app string) (string, bool) { return e.appDir(dataHome, app) }

// ConfigDir is app's configuration directory, $XDG_CONFIG_HOME/app or
// ~/.config/app; false when neither variable holds an absolute path.
// It panics on an app that is not a single path element.
func (e Env) ConfigDir(app string) (string, bool) { return e.appDir(configHome, app) }

// CacheHome is the user's cache directory itself, $XDG_CACHE_HOME or
// ~/.cache, where caches shared across apps live (the thumbnails);
// false when neither variable holds an absolute path.
func (e Env) CacheHome() (string, bool) { return e.base(cacheHome) }

// DataHome is the user's data directory itself, $XDG_DATA_HOME or
// ~/.local/share, where data shared across apps lives (the trash, the
// recently-used list); false when neither variable holds an absolute
// path.
func (e Env) DataHome() (string, bool) { return e.base(dataHome) }

// ConfigHome is the user's configuration directory itself,
// $XDG_CONFIG_HOME or ~/.config, where configuration shared across
// apps lives (user-dirs.dirs); false when neither variable holds an
// absolute path.
func (e Env) ConfigHome() (string, bool) { return e.base(configHome) }

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

// DataDirs are the system data directories, most important first: the
// absolute entries of $XDG_DATA_DIRS, or /usr/local/share and
// /usr/share when it names none.
func (e Env) DataDirs() []string { return e.systemDirs(dataDirs) }

// ConfigDirs are the system configuration directories, most important
// first: the absolute entries of $XDG_CONFIG_DIRS, or /etc/xdg when it
// names none.
func (e Env) ConfigDirs() []string { return e.systemDirs(configDirs) }

// DataSearchPath is every directory data files are looked for in, most
// important first: DataHome, when there is one, then DataDirs. A
// directory the environment names twice is listed twice.
func (e Env) DataSearchPath() []string { return e.searchPath(dataHome, dataDirs) }

// ConfigSearchPath is every directory configuration files are looked
// for in, most important first: ConfigHome, when there is one, then
// ConfigDirs. A directory the environment names twice is listed twice.
func (e Env) ConfigSearchPath() []string { return e.searchPath(configHome, configDirs) }

func (e Env) searchPath(h home, s system) []string {
	list := e(s.env)
	dirs := make([]string, 0, 1+s.capacity(list))
	if dir, ok := e.base(h); ok {
		dirs = append(dirs, dir)
	}
	return s.appendTo(dirs, list)
}

func (e Env) systemDirs(s system) []string {
	list := e(s.env)
	return s.appendTo(make([]string, 0, s.capacity(list)), list)
}

func (s system) capacity(list string) int {
	return max(strings.Count(list, listSeparator)+1, len(s.fallback))
}

func (s system) appendTo(dirs []string, list string) []string {
	before := len(dirs)
	for dir := range strings.SplitSeq(list, listSeparator) {
		if filepath.IsAbs(dir) {
			dirs = append(dirs, dir)
		}
	}
	if len(dirs) == before {
		return append(dirs, s.fallback...)
	}
	return dirs
}

func (e Env) appDir(h home, app string) (string, bool) {
	checkApp(app)
	dir, ok := e.base(h)
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

func (e Env) base(h home) (string, bool) {
	if dir := e.abs(h.env); dir != "" {
		return dir, true
	}
	dir, ok := e.Home()
	if !ok {
		return "", false
	}
	return filepath.Join(dir, h.fallback), true
}

func (e Env) abs(name string) string {
	if dir := e(name); filepath.IsAbs(dir) {
		return dir
	}
	return ""
}
