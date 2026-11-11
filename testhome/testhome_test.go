package testhome

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/pulse/pulsetest"
	"github.com/stubbedev/lys-lib/sockdir"
	"github.com/stubbedev/lys-lib/wltest"
	"github.com/stubbedev/lys-lib/wmipc"
)

func TestMain(m *testing.M) { Run(m) }

var isolatedKeys = []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", runtimeEnv, sessionBusEnv, systemBusEnv, pulseEnv, isolatedEnv}

func restoreAfter(t *testing.T) {
	t.Helper()
	for _, k := range slices.Concat(isolatedKeys, sessionEnv[:], toolchainEnv[:], []string{wltest.HeadlessEnv, SessionEnv}) {
		v, set := os.LookupEnv(k)
		t.Setenv(k, v)
		if !set {
			if err := os.Unsetenv(k); err != nil {
				t.Fatal(err)
			}
		}
	}
}

type verdict struct {
	testing.TB
	errs []string
}

func (v *verdict) Helper() {}

func (v *verdict) Errorf(format string, args ...any) {
	v.errs = append(v.errs, fmt.Sprintf(format, args...))
}

func guard(t *testing.T) []string {
	t.Helper()
	t.Setenv(wltest.HeadlessEnv, "")
	t.Setenv(SessionEnv, "")
	return verdictOf(t)
}

func verdictOf(t *testing.T) []string {
	t.Helper()
	v := &verdict{TB: t}
	RequireNoHostSession(v)
	return v.errs
}

func wantOnly(t *testing.T, errs []string, about string) {
	t.Helper()
	if len(errs) != 1 || !strings.Contains(errs[0], about) {
		t.Errorf("the guard said %q, want one failure about %q", errs, about)
	}
}

func hostRuntimeDir() string { return filepath.Join("/run/user", strconv.Itoa(os.Getuid())) }

func TestTheIsolatedTestBinaryReachesNoHostSession(t *testing.T) {
	RequireNoHostSession(t)
	if errs := guard(t); len(errs) != 0 {
		t.Errorf("outside a headless run the guard says %q", errs)
	}
}

func TestIsolatePointsEveryUserDirAndBusUnderHome(t *testing.T) {
	restoreAfter(t)
	t.Setenv(wltest.HeadlessEnv, "")
	for _, k := range isolatedKeys {
		t.Setenv(k, "/real/"+k)
	}
	home := t.TempDir()
	if err := isolate(home); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": home + "/config", "XDG_DATA_HOME": home + "/data",
		"XDG_STATE_HOME": home + "/state", "XDG_CACHE_HOME": home + "/cache", runtimeEnv: home + "/run",
		sessionBusEnv: busAddress(home + "/run/bus"), systemBusEnv: busAddress(home + "/system-bus"),
		pulseEnv: "unix:" + home + "/run/pulse/native", isolatedEnv: home,
	}
	for _, k := range isolatedKeys {
		if got := os.Getenv(k); got != want[k] {
			t.Errorf("%s = %q, want %q", k, got, want[k])
		}
	}
	if info, err := os.Stat(filepath.Join(home, "run")); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("runtime dir = %v, %v; want a 0700 directory", info, err)
	}
	if errs := guard(t); len(errs) != 0 {
		t.Errorf("the guard fails an isolated process: %q", errs)
	}
}

func TestIsolateFailsWhereTheRuntimeDirCannotBeMade(t *testing.T) {
	restoreAfter(t)
	t.Setenv(wltest.HeadlessEnv, "")
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := isolate(file); err == nil {
		t.Error("a home that is a file isolated without error")
	}
}

func TestIsolateUnsetsEverySessionVariable(t *testing.T) {
	restoreAfter(t)
	t.Setenv(wltest.HeadlessEnv, "")
	for _, k := range sessionEnv {
		t.Setenv(k, "host-"+k)
	}
	if err := isolate(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, k := range sessionEnv {
		if v, set := os.LookupEnv(k); set {
			t.Errorf("%s = %q survived isolation", k, v)
		}
	}
}

func TestIsolateKeepsTheSessionAHeadlessRecipeBooted(t *testing.T) {
	restoreAfter(t)
	recipe := t.TempDir()
	t.Setenv(wltest.HeadlessEnv, "1")
	t.Setenv(runtimeEnv, recipe)
	t.Setenv(sessionBusEnv, "unix:path="+recipe+"/bus")
	t.Setenv(systemBusEnv, "unix:path=/run/dbus/system_bus_socket")
	for _, k := range sessionEnv {
		t.Setenv(k, "recipe-"+k)
	}
	home := t.TempDir()
	if err := isolate(home); err != nil {
		t.Fatal(err)
	}
	for _, k := range sessionEnv {
		if got := os.Getenv(k); got != "recipe-"+k {
			t.Errorf("%s = %q, want the recipe's %q", k, got, "recipe-"+k)
		}
	}
	for k, want := range map[string]string{
		runtimeEnv: recipe, sessionBusEnv: "unix:path=" + recipe + "/bus", pulseEnv: "unix:" + recipe + "/pulse/native",
		systemBusEnv: busAddress(home + "/system-bus"), "HOME": home, "XDG_CONFIG_HOME": home + "/config",
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if errs := verdictOf(t); len(errs) != 0 {
		t.Errorf("the guard fails a headless run on the recipe's session: %q", errs)
	}
}

func fakeGo(t *testing.T, script string) (marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "ran")
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\n: > "+marker+"\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return marker
}

func TestIsolateKeepsTheGoCommandsCachesWhereTheyWere(t *testing.T) {
	restoreAfter(t)
	real := t.TempDir()
	telemetry := filepath.Join(real, "config", "go", "telemetry")
	if err := os.MkdirAll(telemetry, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(telemetry, "mode"), []byte("off"), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"HOME": real, "XDG_CONFIG_HOME": real + "/config", "XDG_CACHE_HOME": real + "/cache", "GOENV": "", "GOCACHE": "", "GOMODCACHE": "", "GOPATH": real + "/gopath"} {
		t.Setenv(k, v)
	}
	if err := isolate(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"GOENV": real + "/config/go/env", "GOCACHE": real + "/cache/go-build", "GOMODCACHE": real + "/gopath/pkg/mod", "GOPATH": real + "/gopath"} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q after the home moved, want %q: what the go command resolved it to before", k, got, want)
		}
	}
}

func TestIsolateAsksTheGoCommandOnlyForWhatTheEnvironmentLeavesOpen(t *testing.T) {
	restoreAfter(t)
	for _, k := range toolchainEnv {
		t.Setenv(k, "/pinned/"+k)
	}
	marker := fakeGo(t, "exit 1")
	if err := isolate(t.TempDir()); err != nil {
		t.Fatalf("isolate with every go setting already in the environment: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the go command ran with nothing to resolve")
	}
	for _, k := range toolchainEnv {
		if got := os.Getenv(k); got != "/pinned/"+k {
			t.Errorf("%s = %q, want it left as it was", k, got)
		}
	}

	t.Setenv("GOCACHE", "")
	t.Setenv("GOPATH", "")
	fakeGo(t, `[ "$*" = "env GOCACHE GOPATH" ] && printf '/asked/cache\n/asked/path\n'`)
	if err := isolate(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if cache, path := os.Getenv("GOCACHE"), os.Getenv("GOPATH"); cache != "/asked/cache" || path != "/asked/path" {
		t.Errorf("GOCACHE = %q and GOPATH = %q, want what the go command printed for the two that were open", cache, path)
	}
}

func TestIsolateWithoutAGoCommandHasNoCachesToKeep(t *testing.T) {
	restoreAfter(t)
	t.Setenv("GOCACHE", "")
	t.Setenv("PATH", t.TempDir())
	home := t.TempDir()
	if err := isolate(home); err != nil {
		t.Fatalf("isolate with no go command on PATH: %v", err)
	}
	if got := os.Getenv("GOCACHE"); got != "" {
		t.Errorf("GOCACHE = %q with no go command to ask", got)
	}
	if got := os.Getenv("HOME"); got != home {
		t.Errorf("HOME = %q, want %q", got, home)
	}
}

func TestIsolateFailsWhenTheGoCommandCannotSay(t *testing.T) {
	restoreAfter(t)
	t.Setenv("GOCACHE", "")
	before := os.Getenv("HOME")
	fakeGo(t, "echo broken >&2; exit 3")
	err := isolate(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "go env") || !strings.Contains(err.Error(), "broken") {
		t.Errorf("isolate = %v, want the go command's failure with what it wrote", err)
	}
	if got := os.Getenv("HOME"); got != before {
		t.Errorf("HOME moved to %q although isolation failed", got)
	}
	fakeGo(t, "echo one-line")
	t.Setenv("GOPATH", "")
	if err := isolate(t.TempDir()); err == nil || !strings.Contains(err.Error(), "printed 1 lines") {
		t.Errorf("isolate = %v, want it to refuse an answer of the wrong length", err)
	}
}

func TestMainIsolatesRunsAndRemovesTheHome(t *testing.T) {
	restoreAfter(t)
	t.Setenv(isolatedEnv, "")
	t.Setenv(wltest.HeadlessEnv, "")
	outer := os.Getenv("HOME")
	var home string
	code := Main(func() int {
		home = os.Getenv("HOME")
		if info, err := os.Stat(filepath.Join(home, "run")); err != nil || !info.IsDir() {
			t.Errorf("the runtime dir during the run: %v, %v", info, err)
		}
		if got := os.Getenv(isolatedEnv); got != home {
			t.Errorf("%s = %q during the run, want the home %q", isolatedEnv, got, home)
		}
		return 7
	})
	if code != 7 {
		t.Errorf("Main = %d, want the run's 7", code)
	}
	if home == "" || home == outer {
		t.Fatalf("the run saw HOME=%q, want a fresh one (outside it is %q)", home, outer)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("the home %s after Main: %v, want it removed", home, err)
	}
}

func TestMainRunsTheChildOfAnIsolatedTestAsItIs(t *testing.T) {
	restoreAfter(t)
	parent := t.TempDir()
	t.Setenv(isolatedEnv, parent)
	t.Setenv("HOME", parent)
	t.Setenv(displayEnv, "wayland-of-the-parent")
	before := os.Environ()
	code := Main(func() int {
		if got := os.Environ(); !slices.Equal(got, before) {
			t.Errorf("the environment of an isolated test's child changed:\n%q\nwant\n%q", got, before)
		}
		return 3
	})
	if code != 3 {
		t.Errorf("Main = %d, want the run's 3", code)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Errorf("the parent's home after the child ran: %v", err)
	}
}

func TestWithoutSessionKeepsOnlyTheRest(t *testing.T) {
	environ := []string{"PATH=/bin", "WAYLAND_DISPLAY=wayland-1", wmipc.SocketEnv + "=/run/lys.sock", "WAYLAND_DISPLAYS=x", "SWAYSOCK", "PIPEWIRE_REMOTE=pipewire-0", "HOME=/home/u"}
	got := WithoutSession(environ)
	if want := []string{"PATH=/bin", "WAYLAND_DISPLAYS=x", "HOME=/home/u"}; !slices.Equal(got, want) {
		t.Errorf("WithoutSession = %q, want %q", got, want)
	}
	if environ[1] != "WAYLAND_DISPLAY=wayland-1" {
		t.Errorf("WithoutSession changed its argument: %q", environ)
	}
}

func TestTheGuardFailsASessionBusThatAnswers(t *testing.T) {
	dbustest.Session(t)
	wantOnly(t, guard(t), "a session bus answers")
}

func TestTheGuardFailsASystemBusThatAnswers(t *testing.T) {
	dbustest.Start(t).UseAsSystemBus(t)
	wantOnly(t, guard(t), "a system bus answers")
	t.Setenv(wltest.HeadlessEnv, "1")
	wantOnly(t, verdictOf(t), "a system bus answers")
}

func TestTheGuardFailsACompositorThatAnswers(t *testing.T) {
	ln, _ := wltest.LoopbackDisplay(t, "wayland-host")
	defer func() { _ = ln.Close() }()
	errs := guard(t)
	if len(errs) != 2 || !strings.Contains(errs[0], "names a display") || !strings.Contains(errs[1], "a compositor answers at wayland-host") {
		t.Errorf("the guard said %q, want the display named and answering", errs)
	}
}

func TestTheGuardFailsASoundServerThatAnswers(t *testing.T) {
	t.Setenv(pulseEnv, pulsetest.New(t).Addr())
	wantOnly(t, guard(t), "a sound server answers")
}

func TestTheGuardFailsAnEndpointLeftToGuess(t *testing.T) {
	for _, k := range []string{runtimeEnv, sessionBusEnv, systemBusEnv, pulseEnv} {
		t.Run(k, func(t *testing.T) {
			t.Setenv(k, "")
			wantOnly(t, guard(t), k+" is unset")
		})
	}
}

func TestTheGuardFailsAnEndpointInTheHostsRuntimeDir(t *testing.T) {
	host := hostRuntimeDir()
	for _, c := range [][2]string{
		{runtimeEnv, host},
		{sessionBusEnv, "unix:path=" + host + "/bus"},
		{systemBusEnv, "unix:path=" + host + "/system-bus"},
		{pulseEnv, "unix:" + host + "/pulse/native"},
		{displayEnv, host + "/wayland-1"},
		{"PIPEWIRE_REMOTE", host + "/pipewire-0"},
		{"PIPEWIRE_RUNTIME_DIR", host},
		{"PULSE_RUNTIME_PATH", host + "/pulse"},
	} {
		t.Run(c[0], func(t *testing.T) {
			t.Setenv(c[0], c[1])
			wantOnly(t, guard(t), c[0]+"="+strconv.Quote(c[1])+" is in the host's runtime directory")
		})
	}
}

func TestTheGuardFailsAHeadlessRunOnTheHostsSession(t *testing.T) {
	t.Setenv(wltest.HeadlessEnv, "1")
	t.Setenv(SessionEnv, "")
	t.Setenv(runtimeEnv, hostRuntimeDir())
	wantOnly(t, verdictOf(t), "is in the host's runtime directory")
}

func recipeSession(t *testing.T, dir string, env map[string]string) (home string) {
	t.Helper()
	restoreAfter(t)
	for _, k := range slices.Concat(sessionEnv[:], []string{sessionBusEnv, wltest.HeadlessEnv}) {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(SessionEnv, dir)
	t.Setenv(runtimeEnv, dir)
	for k, v := range env {
		t.Setenv(k, v)
	}
	home = t.TempDir()
	if err := isolate(home); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestASessionARecipeNamedStaysAndPassesTheGuardWhereverItLies(t *testing.T) {
	for _, c := range []struct{ name, dir string }{
		{"a directory of the recipe's", t.TempDir()},
		{"the runtime directory of the user, as in a VM", hostRuntimeDir()},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := recipeSession(t, c.dir, map[string]string{displayEnv: "wayland-1", "HYPRLAND_INSTANCE_SIGNATURE": "recipe"})
			for k, want := range map[string]string{
				runtimeEnv: c.dir, displayEnv: "wayland-1", "HYPRLAND_INSTANCE_SIGNATURE": "recipe",
				sessionBusEnv: busAddress(home + "/session-bus"), systemBusEnv: busAddress(home + "/system-bus"),
				pulseEnv: "unix:" + c.dir + "/pulse/native", "HOME": home,
			} {
				if got := os.Getenv(k); got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
			if errs := verdictOf(t); len(errs) != 0 {
				t.Errorf("the guard fails the session the recipe named: %q", errs)
			}
		})
	}
}

func TestIsolateKeepsTheSessionBusARecipeNamed(t *testing.T) {
	dir := t.TempDir()
	bus := "unix:path=" + dir + "/bus,guid=0123"
	recipeSession(t, dir, map[string]string{sessionBusEnv: bus})
	if got := os.Getenv(sessionBusEnv); got != bus {
		t.Errorf("%s = %q, want the recipe's %q", sessionBusEnv, got, bus)
	}
	if errs := verdictOf(t); len(errs) != 0 {
		t.Errorf("the guard fails the recipe's own session bus: %q", errs)
	}
}

func TestTheGuardHoldsEveryEndpointToTheSessionARecipeNamed(t *testing.T) {
	host := hostRuntimeDir()
	for _, c := range []struct {
		key, value, want string
	}{
		{runtimeEnv, host, runtimeEnv + "=" + strconv.Quote(host) + " is not the session"},
		{sessionBusEnv, "unix:path=" + host + "/bus", "lies outside the session"},
		{systemBusEnv, "unix:path=/run/dbus/system_bus_socket", "lies outside the session"},
		{pulseEnv, "unix:" + host + "/pulse/native", "lies outside the session"},
		{displayEnv, host + "/wayland-0", "lies outside the session"},
		{"PIPEWIRE_REMOTE", host + "/pipewire-0", "lies outside the session"},
		{"PIPEWIRE_RUNTIME_DIR", host, "lies outside the session"},
		{"PULSE_RUNTIME_PATH", host + "/pulse", "lies outside the session"},
		{sessionBusEnv, "", sessionBusEnv + " is unset"},
		{systemBusEnv, "", systemBusEnv + " is unset"},
		{pulseEnv, "", pulseEnv + " is unset"},
	} {
		t.Run(c.key+"="+c.value, func(t *testing.T) {
			dir := t.TempDir()
			recipeSession(t, dir, nil)
			t.Setenv(c.key, c.value)
			wantOnly(t, verdictOf(t), c.want)
		})
	}
}

func TestTheGuardTakesASessionDirectoryThatOnlyStartsLikeTheNamedOneForAnother(t *testing.T) {
	dir := t.TempDir()
	recipeSession(t, dir, nil)
	t.Setenv(sessionBusEnv, "unix:path="+dir+"-of-the-desktop/bus")
	wantOnly(t, verdictOf(t), "lies outside the session")
	t.Setenv(sessionBusEnv, "unix:path=/elsewhere"+dir+"/bus")
	wantOnly(t, verdictOf(t), "lies outside the session")
}

func TestTheGuardFailsASystemBusThatAnswersInASessionARecipeNamed(t *testing.T) {
	dir := sockdir.Dir(t)
	recipeSession(t, dir, nil)
	t.Setenv(systemBusEnv, dbustest.StartAt(t, filepath.Join(dir, "system-bus")).Address)
	wantOnly(t, verdictOf(t), "a system bus answers")
}

func TestASessionARecipeNamedWithNoTestHomeOwnsOnlyItsDirectory(t *testing.T) {
	dir := t.TempDir()
	recipeSession(t, dir, nil)
	t.Setenv(isolatedEnv, "")
	errs := verdictOf(t)
	if len(errs) != 2 || !strings.Contains(errs[0], sessionBusEnv) || !strings.Contains(errs[1], systemBusEnv) {
		t.Errorf("without a test home the guard said %q, want the buses Main pinned there refused", errs)
	}
}
