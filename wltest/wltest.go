// Package wltest helps tests run Wayland clients: a private runtime
// dir and a loopback display for wire-level tests with no compositor,
// and, against the headless compositor the test recipes boot, a client
// that puts solid-colored surfaces on screen for capture tests to find.
package wltest

import (
	"fmt"
	"image"
	"image/color"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// HeadlessEnv is set by the recipes that boot a headless compositor
// for a test run; Require skips without it.
const HeadlessEnv = "LYS_TEST_HEADLESS"

// Require skips a test that needs the headless compositor when the
// run has none.
func Require(t *testing.T) {
	t.Helper()
	if os.Getenv(HeadlessEnv) == "" {
		t.Skipf("%s is not set; compositor tests run under just headless", HeadlessEnv)
	}
}

// RuntimeDir creates a private XDG_RUNTIME_DIR at the temp root and
// points the process env at it. It sits outside t.TempDir because the
// kernel caps a socket path at 107 bytes and CI's nested temp dirs push
// a t.TempDir path past that, so bind fails with EINVAL. The dir goes
// away at cleanup.
func RuntimeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lys-rt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
	return dir
}

// LoopbackDisplay binds a real unix socket as a wayland display the
// test process owns: the far end for wire-level and dead-peer tests,
// no compositor involved. The socket lives in a fresh RuntimeDir and
// the process env is pointed at the display, so wl.Connect("")
// reaches the listener. The caller closes the listener.
func LoopbackDisplay(t *testing.T, display string) (*net.UnixListener, string) {
	t.Helper()
	sock := filepath.Join(RuntimeDir(t), display)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WAYLAND_DISPLAY", display)
	return ln, sock
}

// Eventually retries fn until it succeeds or eight seconds pass.
func Eventually(t *testing.T, what string, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if err = fn(); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: %v", what, err)
}

// WantColor checks one pixel of a captured frame.
func WantColor(img *image.RGBA, x, y int, want color.RGBA) error {
	if got := img.RGBAAt(x, y); got != want {
		return fmt.Errorf("pixel (%d,%d) = %v, want %v", x, y, got, want)
	}
	return nil
}
