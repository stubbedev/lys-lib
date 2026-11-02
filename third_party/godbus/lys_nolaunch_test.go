//go:build !darwin

package dbus

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Finding no session bus is an error and starts nothing: a dbus-launch on
// PATH leaves a marker if it runs, as each run used to leak a daemon.
func TestNoSessionBusStartsNoDaemon(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(dir, "dbus-launch"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if _, err := getSessionBusPlatformAddress(); !errors.Is(err, ErrNoSessionBus) {
		t.Errorf("error %v, want ErrNoSessionBus", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("dbus-launch ran")
	}
}
