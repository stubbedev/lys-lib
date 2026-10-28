package xdg

import (
	"os"
	"path/filepath"
	"testing"
)

func writeUserDirs(t *testing.T, config, body string) {
	t.Helper()
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "user-dirs.dirs"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUserDir(t *testing.T) {
	cfg := t.TempDir()
	e := env(map[string]string{"HOME": "/home/u", "XDG_CONFIG_HOME": cfg})
	writeUserDirs(t, cfg, "# written by xdg-user-dirs-update\n"+
		"XDG_DOCUMENTS_DIR=\"$HOME/Docs\"\n"+
		"XDG_DESKTOP_DIR=\"$HOME\"\n"+
		"XDG_PICTURES_DIR=\"/srv/pics/\"\n")
	for key, want := range map[string]string{"DOCUMENTS": "/home/u/Docs", "DESKTOP": "/home/u", "PICTURES": "/srv/pics"} {
		if got, ok, err := e.UserDir(key); err != nil || !ok || got != want {
			t.Errorf("UserDir(%s) = %q %v %v, want %q", key, got, ok, err, want)
		}
	}
}

func TestUserDirNotSet(t *testing.T) {
	cfg := t.TempDir()
	for _, tc := range []struct {
		name string
		env  Env
	}{
		{"no config home", env(nil)},
		{"no user-dirs.dirs", env(map[string]string{"XDG_CONFIG_HOME": cfg})},
	} {
		if got, ok, err := tc.env.UserDir("DOCUMENTS"); err != nil || ok {
			t.Errorf("%s: UserDir = %q %v %v, want not set", tc.name, got, ok, err)
		}
	}
	writeUserDirs(t, cfg, "XDG_DOCUMENTS_DIR=\"/docs\"\n")
	if got, ok, err := env(map[string]string{"XDG_CONFIG_HOME": cfg}).UserDir("DOWNLOAD"); err != nil || ok {
		t.Errorf("an absent key: UserDir = %q %v %v, want not set", got, ok, err)
	}
}

func TestUserDirReportsAnInvalidValue(t *testing.T) {
	cfg := t.TempDir()
	writeUserDirs(t, cfg, "XDG_MUSIC_DIR=relative-unquoted\n"+
		"XDG_VIDEOS_DIR=\"Videos\"\n"+
		"XDG_TEMPLATES_DIR=\"$HOMEtemplates\"\n"+
		"XDG_DOCUMENTS_DIR=\"$HOME/Docs\"\n")
	e := env(map[string]string{"HOME": "/home/u", "XDG_CONFIG_HOME": cfg})
	for _, key := range []string{"MUSIC", "VIDEOS", "TEMPLATES"} {
		if got, ok, err := e.UserDir(key); err == nil || ok {
			t.Errorf("UserDir(%s) = %q %v %v, want an error", key, got, ok, err)
		}
	}
	noHome := env(map[string]string{"XDG_CONFIG_HOME": cfg})
	if got, ok, err := noHome.UserDir("DOCUMENTS"); err == nil || ok {
		t.Errorf("UserDir(DOCUMENTS) without HOME = %q %v %v, want an error", got, ok, err)
	}
}

func TestUserDirReportsAReadError(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := env(map[string]string{"XDG_CONFIG_HOME": notADir}).UserDir("DOCUMENTS"); err == nil || ok {
		t.Errorf("a config home that is a file: UserDir = %q %v %v, want an error", got, ok, err)
	}
	cfg := t.TempDir()
	if err := os.Mkdir(filepath.Join(cfg, "user-dirs.dirs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := env(map[string]string{"XDG_CONFIG_HOME": cfg}).UserDir("DOCUMENTS"); err == nil || ok {
		t.Errorf("a user-dirs.dirs that is a directory: UserDir = %q %v %v, want an error", got, ok, err)
	}
}

func TestUserDirIgnoresARelativeConfigHome(t *testing.T) {
	home := t.TempDir()
	writeUserDirs(t, filepath.Join(home, ".config"), "XDG_DOCUMENTS_DIR=\"$HOME/Docs\"\n")
	e := env(map[string]string{"HOME": home, "XDG_CONFIG_HOME": "relative"})
	if got, ok, err := e.UserDir("DOCUMENTS"); err != nil || !ok || got != filepath.Join(home, "Docs") {
		t.Errorf("UserDir(DOCUMENTS) = %q %v %v, want %q", got, ok, err, filepath.Join(home, "Docs"))
	}
}
