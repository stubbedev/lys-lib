package xdg

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// UserDir is a user directory from $XDG_CONFIG_HOME/user-dirs.dirs
// (glib's g_get_user_special_dir): key is the name between XDG_ and
// _DIR (DOCUMENTS, PICTURES, ...), the value's leading $HOME expanded.
// It is not set, false with a nil error, when there is no config
// directory, no user-dirs.dirs or no line for key. Every other failure
// is an error: the file could not be read, or key's value is not a
// quoted absolute or $HOME-relative path.
func (e Env) UserDir(key string) (string, bool, error) {
	config, ok := e.ConfigHome()
	if !ok {
		return "", false, nil
	}
	path := filepath.Join(config, "user-dirs.dirs")
	f, err := os.Open(path) //nolint:gosec // the user's own config file
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("xdg: user dir %s: %w", key, err)
	}
	defer func() { _ = f.Close() }()
	want := "XDG_" + key + "_DIR"
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		name, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.TrimSpace(name) != want {
			continue
		}
		dir, err := e.userDirValue(strings.TrimSpace(value))
		if err != nil {
			return "", false, fmt.Errorf("xdg: user dir %s: %s:%d: %w", key, path, line, err)
		}
		return dir, true, nil
	}
	if err := sc.Err(); err != nil {
		return "", false, fmt.Errorf("xdg: user dir %s: reading %s: %w", key, path, err)
	}
	return "", false, nil
}

func (e Env) userDirValue(value string) (string, error) {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", fmt.Errorf("value %s is not quoted", value)
	}
	dir := value[1 : len(value)-1]
	if dir == "$HOME" || strings.HasPrefix(dir, "$HOME/") {
		home := e.abs("HOME")
		if home == "" {
			return "", fmt.Errorf("value %s is under $HOME, which is not an absolute path", value)
		}
		dir = home + strings.TrimPrefix(dir, "$HOME")
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("value %s is not an absolute path", value)
	}
	return filepath.Clean(dir), nil
}
