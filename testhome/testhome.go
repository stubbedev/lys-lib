// Package testhome runs a test binary against a scratch home and no
// session but its own, so no test, and no application or daemon it
// starts, reads or writes the real user's files or reaches the session
// of whoever runs it: the session bus, the system bus, the compositor,
// the sound server. A TestMain calls Run (or Main, around work of its
// own), and one test of the package calls RequireNoHostSession, which
// fails where a host endpoint is still within reach.
package testhome

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbusx"
	"github.com/stubbedev/lys-lib/pulse/native"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/third_party/wayland/wl"
	"github.com/stubbedev/lys-lib/wltest"
	"github.com/stubbedev/lys-lib/wmipc"
)

// SessionEnv is how a recipe that boots a session for a test run (a
// headless compositor, a VM's desktop) names it to the tests: the
// session's runtime directory. Main then leaves that session in place,
// and RequireNoHostSession holds every endpoint to it, wherever the
// directory lies: in a VM it is /run/user/<uid>, which on a developer's
// machine is the desktop's.
const SessionEnv = "LYS_TEST_SESSION"

const (
	isolatedEnv   = "LYS_TESTHOME"
	runtimeEnv    = "XDG_RUNTIME_DIR"
	sessionBusEnv = "DBUS_SESSION_BUS_ADDRESS"
	systemBusEnv  = "DBUS_SYSTEM_BUS_ADDRESS"
	pulseEnv      = "PULSE_SERVER"
	displayEnv    = "WAYLAND_DISPLAY"

	probeTimeout     = 2 * time.Second
	toolchainTimeout = 30 * time.Second
)

// sessionEnv are the variables that advertise a running compositor,
// display, desktop or media server.
var sessionEnv = [...]string{
	wmipc.SocketEnv,
	displayEnv,
	"WAYLAND_SOCKET",
	"DISPLAY",
	"HYPRLAND_INSTANCE_SIGNATURE",
	"NIRI_SOCKET",
	"MANGO_INSTANCE_SIGNATURE",
	"SWAYSOCK",
	"I3SOCK",
	"XDG_CURRENT_DESKTOP",
	"XDG_ACTIVATION_TOKEN",
	"PIPEWIRE_REMOTE",
	"PIPEWIRE_RUNTIME_DIR",
	"PULSE_RUNTIME_PATH",
	"PULSE_COOKIE",
}

// toolchainEnv are the go command's settings whose defaults lie under
// the user's home and configuration directory.
var toolchainEnv = [...]string{"GOENV", "GOCACHE", "GOMODCACHE", "GOPATH"}

// Run isolates the test binary in a fresh temp directory, runs the
// tests, removes the directory, and exits with their code. Call it
// from TestMain. See Main for what isolating does.
func Run(m *testing.M) { os.Exit(Main(m.Run)) }

// Main is Run for a TestMain with work of its own around the tests: it
// isolates the process, calls run, and returns its code.
//
// Isolating points HOME and the XDG per-user directories under a fresh
// temp directory, and the system bus at a socket there that nothing
// serves: a test that needs a system bus starts its own and points the
// process at it (dbustest's UseAsSystemBus). Outside a headless run it
// also unsets every variable that advertises a running compositor,
// display, desktop or media server, and points the runtime directory,
// which it creates with mode 0700, the session bus and the sound
// server at sockets under the temp directory, where only a test's own
// fake can be. In a headless run (wltest.HeadlessEnv), or with a session
// a recipe named (SessionEnv), the session in the environment is the
// one the recipe booted for the tests, and stays: only the system bus
// and the sound server are pinned, the latter to the recipe's runtime
// directory, and a session bus the recipe did not name, to a socket
// under the temp directory, so that no client guesses one.
// The go command's caches and configuration, whose defaults lie under
// the home that moved, stay where they were, so a go build a test runs
// starts warm.
//
// A test binary started by an isolated test (a helper process, the
// binary run again as the program under test) runs as it is: its
// environment is the one its parent made for it.
func Main(run func() int) int {
	if os.Getenv(isolatedEnv) != "" {
		return run()
	}
	home, err := os.MkdirTemp("", "lys-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testhome:", err)
		return 1
	}
	code := 1
	if err := isolate(home); err != nil {
		fmt.Fprintln(os.Stderr, "testhome:", err)
	} else {
		code = run()
	}
	if err := os.RemoveAll(home); err != nil {
		fmt.Fprintln(os.Stderr, "testhome:", err)
		return 1
	}
	return code
}

func isolate(home string) error {
	env, err := toolchain()
	if err != nil {
		return err
	}
	env = append(env,
		[2]string{"HOME", home},
		[2]string{"XDG_CONFIG_HOME", filepath.Join(home, "config")},
		[2]string{"XDG_DATA_HOME", filepath.Join(home, "data")},
		[2]string{"XDG_STATE_HOME", filepath.Join(home, "state")},
		[2]string{"XDG_CACHE_HOME", filepath.Join(home, "cache")},
		[2]string{systemBusEnv, busAddress(filepath.Join(home, "system-bus"))},
		[2]string{isolatedEnv, home},
	)
	runtime := os.Getenv(runtimeEnv)
	switch {
	case !sessionProvided():
		for _, key := range sessionEnv {
			if err := os.Unsetenv(key); err != nil {
				return fmt.Errorf("unset %s: %w", key, err)
			}
		}
		runtime = filepath.Join(home, "run")
		if err := os.MkdirAll(runtime, 0o700); err != nil {
			return fmt.Errorf("create the runtime dir: %w", err)
		}
		env = append(env,
			[2]string{runtimeEnv, runtime},
			[2]string{sessionBusEnv, busAddress(filepath.Join(runtime, "bus"))},
		)
	case os.Getenv(sessionBusEnv) == "":
		env = append(env, [2]string{sessionBusEnv, busAddress(filepath.Join(home, "session-bus"))})
	}
	env = append(env, [2]string{pulseEnv, "unix:" + filepath.Join(runtime, "pulse", "native")})
	for _, kv := range env {
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			return fmt.Errorf("set %s: %w", kv[0], err)
		}
	}
	return nil
}

func sessionProvided() bool {
	return os.Getenv(wltest.HeadlessEnv) != "" || os.Getenv(SessionEnv) != ""
}

func busAddress(socket string) string {
	return "unix:path=" + dbus.EscapeBusAddressValue(socket)
}

// toolchain is what the go command on PATH resolves toolchainEnv to
// now, for the settings the environment does not already name: once
// the home has moved it would resolve them to empty caches under the
// scratch one. With no go command on PATH there is none to keep warm.
func toolchain() ([][2]string, error) {
	unset := slices.DeleteFunc(slices.Clone(toolchainEnv[:]), func(key string) bool { return os.Getenv(key) != "" })
	if len(unset) == 0 {
		return nil, nil
	}
	goTool, err := exec.LookPath("go")
	if errors.Is(err, exec.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find the go command: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolchainTimeout)
	defer cancel()
	asked := "go env " + strings.Join(unset, " ")
	cmd := exec.CommandContext(ctx, goTool, append([]string{"env"}, unset...)...) //nolint:gosec // the go command on PATH, with a fixed subcommand and setting names
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			stderr = exit.Stderr
		}
		return nil, fmt.Errorf("%s: %w\n%s", asked, err, stderr)
	}
	values := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(values) != len(unset) {
		return nil, fmt.Errorf("%s printed %d lines, want one per setting: %q", asked, len(values), out)
	}
	env := make([][2]string, len(unset), len(unset)+10)
	for i, key := range unset {
		env[i] = [2]string{key, values[i]}
	}
	return env, nil
}

// WithoutSession returns environ, in os.Environ's KEY=value form, minus
// every variable that advertises a running compositor, display, desktop
// or media server, for a child a test starts as a session of its own.
func WithoutSession(environ []string) []string {
	return slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		key, _, _ := strings.Cut(kv, "=")
		return slices.Contains(sessionEnv[:], key)
	})
}

// RequireNoHostSession fails t when the test process could reach the
// session or the system bus of the host it runs on: when the runtime
// directory, the session bus, the system bus or the sound server it
// would dial is unset, leaving a client to guess the host's; when one
// of them, the Wayland display or a media server's socket lies in the
// host's runtime directory; when a system bus answers at all, which
// only a bus the calling test itself started may; and, outside a
// headless run, when a display is named or a session bus, a compositor
// or a sound server answers. Every package whose tests build
// applications or dial a bus calls it from a test of its own, under a
// TestMain that calls Run.
//
// Where a recipe named the session it booted (SessionEnv), the process
// is held to exactly that session instead: the runtime directory is the
// one named, and the session bus, the system bus, the sound server, an
// absolute Wayland display and a media server's socket each lie in it
// or in the test home Main made. The system bus still answers to none.
func RequireNoHostSession(t testing.TB) {
	t.Helper()
	if session := os.Getenv(SessionEnv); session != "" {
		requireSession(t, filepath.Clean(session))
		return
	}
	host := filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	reachable := false
	for _, key := range []string{runtimeEnv, sessionBusEnv, systemBusEnv, pulseEnv} {
		if os.Getenv(key) == "" {
			reachable = true
			t.Errorf("%s is unset: a client guesses the host's", key)
		}
	}
	for _, key := range []string{runtimeEnv, sessionBusEnv, systemBusEnv, pulseEnv, displayEnv, "PIPEWIRE_REMOTE", "PIPEWIRE_RUNTIME_DIR", "PULSE_RUNTIME_PATH"} {
		if v := os.Getenv(key); v == host || strings.Contains(v, host+"/") {
			reachable = true
			t.Errorf("%s=%q is in the host's runtime directory %s", key, v, host)
		}
	}
	if reachable {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), probeTimeout)
	defer cancel()
	if conn, err := dbusx.DialSystem(ctx, probeTimeout); err == nil {
		_ = conn.Close()
		t.Errorf("a system bus answers at %s", os.Getenv(systemBusEnv))
	}
	if os.Getenv(wltest.HeadlessEnv) != "" {
		return
	}
	for _, key := range []string{displayEnv, "WAYLAND_SOCKET", "DISPLAY"} {
		if v, set := os.LookupEnv(key); set {
			t.Errorf("%s=%q names a display the tests did not start", key, v)
		}
	}
	if conn, err := dbusx.DialSession(ctx, probeTimeout); err == nil {
		_ = conn.Close()
		t.Errorf("a session bus answers at %s", os.Getenv(sessionBusEnv))
	}
	if display, err := wl.Connect(""); err == nil {
		_ = display.Context().Close()
		t.Errorf("a compositor answers at %s in %s", cmp.Or(os.Getenv(displayEnv), "wayland-0"), os.Getenv(runtimeEnv))
	}
	if conn, err := native.Dial(ctx, native.Options{ClientName: "lys testhome", Timeout: probeTimeout}); err == nil {
		_ = conn.Close()
		t.Errorf("a sound server answers at %s", os.Getenv(pulseEnv))
	}
}

func requireSession(t testing.TB, session string) {
	t.Helper()
	owned := []string{session, dbus.EscapeBusAddressValue(session)}
	if home := os.Getenv(isolatedEnv); home != "" {
		owned = append(owned, home, dbus.EscapeBusAddressValue(home))
	}
	ok := true
	fail := func(format string, args ...any) {
		ok = false
		t.Errorf(format, args...)
	}
	if runtime := os.Getenv(runtimeEnv); filepath.Clean(runtime) != session {
		fail("%s=%q is not the session %s names, %s", runtimeEnv, runtime, SessionEnv, session)
	}
	for _, key := range []string{sessionBusEnv, systemBusEnv, pulseEnv} {
		if os.Getenv(key) == "" {
			fail("%s is unset: a client guesses the host's", key)
		}
	}
	for _, key := range []string{sessionBusEnv, systemBusEnv, pulseEnv, "PIPEWIRE_REMOTE", "PIPEWIRE_RUNTIME_DIR", "PULSE_RUNTIME_PATH"} {
		if v := os.Getenv(key); v != "" && !slices.ContainsFunc(owned, func(dir string) bool { return liesIn(v, dir) }) {
			fail("%s=%q lies outside the session %s names (%s) and the test home", key, v, SessionEnv, session)
		}
	}
	if display := os.Getenv(displayEnv); filepath.IsAbs(display) && !liesIn(display, session) {
		fail("%s=%q lies outside the session %s names, %s", displayEnv, display, SessionEnv, session)
	}
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), probeTimeout)
	defer cancel()
	if conn, err := dbusx.DialSystem(ctx, probeTimeout); err == nil {
		_ = conn.Close()
		t.Errorf("a system bus answers at %s", os.Getenv(systemBusEnv))
	}
}

func liesIn(endpoint, dir string) bool {
	for rest := endpoint; ; {
		i := strings.Index(rest, dir)
		if i < 0 {
			return false
		}
		start := len(endpoint) - len(rest) + i
		end := start + len(dir)
		atPathStart := start == 0 || endpoint[start-1] == '=' || endpoint[start-1] == ':'
		atPathEnd := end == len(endpoint) || endpoint[end] == '/' || endpoint[end] == ','
		if atPathStart && atPathEnd {
			return true
		}
		rest = rest[i+1:]
	}
}
