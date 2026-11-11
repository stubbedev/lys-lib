package portalsettings

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/appearance"
	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/icons"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const xdgDesktopPortal = "org.freedesktop.portal.Desktop"

// The mock: a real dbus daemon on a private unix socket (never the
// session bus), with a test-owned connection that claims the portal's
// well-known name, exports a scripted ReadOne, and emits raw
// SettingChanged signals. The monitor under test connects to the same
// socket through the dial seam, so every test exercises the real wire
// path — marshalling, match rules, activation-free name lookup — with
// zero session-bus contact.

// mockPortal is the scripted org.freedesktop.portal.Settings service.
// Fields are mutex-guarded: ReadOne runs on godbus's handler goroutine
// while tests reconfigure the script from the test goroutine.
type mockPortal struct {
	mu    sync.Mutex
	conn  *dbus.Conn
	value uint32
	// iconTheme answers the org.gnome.desktop.interface icon-theme
	// read; iconRaw overrides it for unreadable-value cases.
	iconTheme string
	// settings answers any other namespace.key the test set.
	settings map[string]any
	iconRaw  any
	// raw, when set, overrides value entirely — for variants the spec
	// does not define (a string where the uint32 belongs).
	raw any
	// fails makes ReadOne report an error: the key is absent.
	fails bool
	// readOnes and readAlls count the calls of each read method.
	readOnes, readAlls int
}

func (p *mockPortal) set(value uint32) {
	p.mu.Lock()
	p.value = value
	p.mu.Unlock()
}

func (p *mockPortal) setRaw(raw any) {
	p.mu.Lock()
	p.raw = raw
	p.mu.Unlock()
}

func (p *mockPortal) setIconTheme(name string) {
	p.mu.Lock()
	p.iconTheme = name
	p.mu.Unlock()
}

// setSetting answers namespace.key with v from now on.
func (p *mockPortal) setSetting(namespace, key string, v any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.settings == nil {
		p.settings = map[string]any{}
	}
	p.settings[namespace+"."+key] = v
}

func (p *mockPortal) fail() {
	p.mu.Lock()
	p.fails = true
	p.mu.Unlock()
}

// ReadOne is the portal method: (namespace, key) -> v.
func (p *mockPortal) ReadOne(namespace, key string) (dbus.Variant, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.readOnes++
	return p.read(namespace, key)
}

// ReadAll is the portal method: namespaces -> every key they hold.
func (p *mockPortal) ReadAll(namespaces []string) (map[string]map[string]dbus.Variant, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.readAlls++
	tracked := map[string][]string{
		schemeNamespace:    {schemeKey, accentKey, contrastKey, motionKey},
		interfaceNamespace: {iconThemeKey, fontNameKey, textScaleKey},
	}
	out := map[string]map[string]dbus.Variant{}
	for _, ns := range namespaces {
		keys := map[string]dbus.Variant{}
		for _, key := range tracked[ns] {
			if v, err := p.read(ns, key); err == nil {
				keys[key] = v
			}
		}
		out[ns] = keys
	}
	return out, nil
}

// reads reports how many ReadOne and ReadAll calls the portal answered.
func (p *mockPortal) reads() (readOnes, readAlls int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.readOnes, p.readAlls
}

func (p *mockPortal) read(namespace, key string) (dbus.Variant, error) {
	if v, ok := p.settings[namespace+"."+key]; ok {
		return dbus.MakeVariant(v), nil
	}
	if namespace == interfaceNamespace && key == iconThemeKey {
		if p.iconRaw != nil {
			return dbus.MakeVariant(p.iconRaw), nil
		}
		return dbus.MakeVariant(p.iconTheme), nil
	}
	if namespace != schemeNamespace || key != schemeKey {
		return dbus.Variant{}, fmt.Errorf("no such key %s.%s", namespace, key)
	}
	if p.fails {
		return dbus.Variant{}, errors.New("portal: no such key")
	}
	if p.raw != nil {
		return dbus.MakeVariant(p.raw), nil
	}
	return dbus.MakeVariant(p.value), nil
}

// publishMock connects to bus, claims the portal's well-known name,
// and exports the mock on the portal object path.
func publishMock(t *testing.T, bus *dbustest.Bus) *mockPortal {
	t.Helper()
	return publishMockSaying(t, bus, 0)
}

// publishMockSaying is publishMock with the color scheme already in
// place, and like a real service it answers before it takes the name:
// whoever sees the name appear can read at once.
func publishMockSaying(t *testing.T, bus *dbustest.Bus, value uint32) *mockPortal {
	t.Helper()
	conn := bus.Conn(t)
	p := &mockPortal{conn: conn, value: value}
	// ExportAll, not Export: the plain-Go-error return is only accepted
	// by the ExportAll path (Export wants a *dbus.Error tail).
	if err := conn.ExportAll(p, portalPath, portalIface); err != nil {
		t.Fatalf("mock portal: export: %v", err)
	}
	reply, err := conn.RequestName(xdgDesktopPortal, dbus.NameFlagDoNotQueue)
	if err != nil {
		t.Fatalf("mock portal: request name: %v", err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("mock portal: %s already owned", xdgDesktopPortal)
	}
	return p
}

// emit sends one SettingChanged signal the way the real portal does:
// (namespace, key, value) on the portal path, no destination (bus
// broadcast through match rules).
func (p *mockPortal) emit(t *testing.T, value any) {
	t.Helper()
	emitSetting(t, p.conn, schemeNamespace, schemeKey, value)
}

// emitIconTheme sends the icon-theme SettingChanged: the signal the
// live-following icon cache rides (#64).
func (p *mockPortal) emitIconTheme(t *testing.T, name any) {
	t.Helper()
	emitSetting(t, p.conn, interfaceNamespace, iconThemeKey, name)
}

// emitSetting sends one SettingChanged for the given namespace and key.
func emitSetting(t *testing.T, conn *dbus.Conn, namespace, key string, value any) {
	t.Helper()
	body := []any{namespace, key, dbus.MakeVariant(value)}
	msg := &dbus.Message{
		Type: dbus.TypeSignal,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldPath:      dbus.MakeVariant(portalPath),
			dbus.FieldInterface: dbus.MakeVariant(portalIface),
			dbus.FieldMember:    dbus.MakeVariant("SettingChanged"),
			dbus.FieldSignature: dbus.MakeVariant(dbus.SignatureOf(body...)),
		},
		Body: body,
	}
	if call := conn.Send(msg, nil); call.Err != nil {
		t.Fatalf("mock portal: emit: %v", call.Err)
	}
}

// emitOther sends a SettingChanged for a different setting (same
// interface, foreign namespace) — the noise the filter must drop.
func (p *mockPortal) emitOther(t *testing.T) {
	t.Helper()
	body := []any{
		"org.gnome.desktop.interface", "gtk-theme",
		dbus.MakeVariant("adw-gtk3-dark"),
	}
	msg := &dbus.Message{
		Type: dbus.TypeSignal,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldPath:      dbus.MakeVariant(portalPath),
			dbus.FieldInterface: dbus.MakeVariant(portalIface),
			dbus.FieldMember:    dbus.MakeVariant("SettingChanged"),
			dbus.FieldSignature: dbus.MakeVariant(dbus.SignatureOf(body...)),
		},
		Body: body,
	}
	if call := p.conn.Send(msg, nil); call.Err != nil {
		t.Fatalf("mock portal: emitOther: %v", call.Err)
	}
}

// newTestMonitor builds a New()-shaped monitor bound to the private bus
// with a snappy reconnect backoff, registered for cleanup.
func newTestMonitor(t *testing.T, address string) *Monitor {
	t.Helper()
	m := newMonitor(func() (*dbus.Conn, error) { return dbus.Connect(address) },
		10*time.Millisecond)
	t.Cleanup(m.Close)
	return m
}

// collect returns an OnChange sink and a snapshot of what it received,
// both goroutine-safe (callbacks arrive on the monitor goroutine).
func collect() (sink func(appearance.ColorScheme), snapshot func() []appearance.ColorScheme) {
	var mu sync.Mutex
	var got []appearance.ColorScheme
	return func(a appearance.ColorScheme) {
			mu.Lock()
			got = append(got, a)
			mu.Unlock()
		}, func() []appearance.ColorScheme {
			mu.Lock()
			defer mu.Unlock()
			return append([]appearance.ColorScheme(nil), got...)
		}
}

// wantEvents waits for snapshot to equal want exactly (delivery is
// asynchronous; the wait bounds it) and fails with both sides on any
// deviation — including extra events.
func wantEvents(t *testing.T, snapshot func() []appearance.ColorScheme, want ...appearance.ColorScheme) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := snapshot()
		if len(got) == len(want) {
			if !equalEvents(got, want) {
				t.Fatalf("events = %v, want %v", got, want)
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for events: got %v, want %v", snapshot(), want)
}

func equalEvents(got, want []appearance.ColorScheme) bool {
	for i, w := range want {
		if got[i] != w {
			return false
		}
	}
	return true
}

// wantNoEvents asserts the sink stays silent for a beat.
func wantNoEvents(t *testing.T, snapshot func() []appearance.ColorScheme) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	if got := snapshot(); len(got) != 0 {
		t.Fatalf("unexpected events: %v", got)
	}
}

// goroutineID parses the calling goroutine's id out of its own stack
// (the identity check in TestCallbacksRunOnMonitorGoroutine).
func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	line := string(buf[:n])
	var id uint64
	if _, err := fmt.Sscanf(line, "goroutine %d ", &id); err != nil {
		return 0
	}
	return id
}

func TestStartupRead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value uint32
		want  appearance.ColorScheme
	}{
		{"prefer-dark", 1, appearance.Dark},
		{"prefer-light", 2, appearance.Light},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := dbustest.Start(t)
			mock := publishMock(t, bus)
			mock.set(tc.value)
			m := newTestMonitor(t, bus.Address)
			// The startup read is synchronous: no waiting.
			if got := m.ColorScheme(); got != tc.want {
				t.Fatalf("ColorScheme() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStartupReadsEverySettingInOneReadAll(t *testing.T) {
	bus := dbustest.Start(t)
	mock := publishMockSaying(t, bus, schemeDark)
	mock.setIconTheme("Papirus")
	mock.setSetting(schemeNamespace, accentKey, []float64{0.2, 0.4, 0.6})
	mock.setSetting(schemeNamespace, contrastKey, contrastHigh)
	mock.setSetting(schemeNamespace, motionKey, motionReduced)
	mock.setSetting(interfaceNamespace, fontNameKey, "Adwaita Sans 11")
	mock.setSetting(interfaceNamespace, textScaleKey, 1.25)
	m := newTestMonitor(t, bus.Address)
	if ones, alls := mock.reads(); ones != 0 || alls != 1 {
		t.Fatalf("startup made %d ReadOne and %d ReadAll calls, want 0 and 1", ones, alls)
	}
	if got := m.ColorScheme(); got != appearance.Dark {
		t.Errorf("ColorScheme() = %v, want dark", got)
	}
	if got := m.IconTheme(); got != "Papirus" {
		t.Errorf("IconTheme() = %q, want Papirus", got)
	}
	if got := m.Accent(); got != (appearance.Accent{R: 0.2, G: 0.4, B: 0.6, Known: true}) {
		t.Errorf("Accent() = %+v", got)
	}
	if got := m.Contrast(); got != appearance.ContrastHigh {
		t.Errorf("Contrast() = %v, want high", got)
	}
	if got := m.Motion(); got != appearance.MotionReduced {
		t.Errorf("Motion() = %v, want reduced", got)
	}
	if got := m.FontName(); got != "Adwaita Sans 11" {
		t.Errorf("FontName() = %q", got)
	}
	if got := m.TextScale(); got != 1.25 {
		t.Errorf("TextScale() = %v, want 1.25", got)
	}
}

func TestStartupFallbacksToUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(p *mockPortal)
	}{
		{"key absent", (*mockPortal).fail},
		{"no preference", func(p *mockPortal) { p.set(0) }},
		{"out-of-range value", func(p *mockPortal) { p.set(9) }},
		{"garbage type", func(p *mockPortal) { p.setRaw("prefer-dark") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := dbustest.Start(t)
			mock := publishMock(t, bus)
			tc.set(mock)
			m := newTestMonitor(t, bus.Address)
			if got := m.ColorScheme(); got != appearance.Unknown {
				t.Fatalf("ColorScheme() = %v, want unknown", got)
			}
		})
	}
}

func TestNoPortalIsUnknownAndSilent(t *testing.T) {
	bus := dbustest.Start(t)
	// A bus, but nobody owns the portal name: the common
	// no-portal-desktop shape.
	m := newTestMonitor(t, bus.Address)
	onChange, snapshot := collect()
	m.OnColorSchemeChange(onChange)
	if got := m.ColorScheme(); got != appearance.Unknown {
		t.Fatalf("ColorScheme() = %v, want unknown", got)
	}
	wantNoEvents(t, snapshot)
}

func TestStartupReadNeverFiresOnChange(t *testing.T) {
	bus := dbustest.Start(t)
	mock := publishMock(t, bus)
	mock.set(2)
	m := newTestMonitor(t, bus.Address)
	onChange, snapshot := collect()
	m.OnColorSchemeChange(onChange)
	// The baseline is not history: a listener registered after New
	// must not see the startup read, ever (set(deliver=false)).
	wantNoEvents(t, snapshot)
	if got := m.ColorScheme(); got != appearance.Light {
		t.Fatalf("ColorScheme() = %v, want light", got)
	}
}

func TestSettingChangedDeliversInOrder(t *testing.T) {
	bus := dbustest.Start(t)
	mock := publishMock(t, bus)
	mock.set(1)
	m := newTestMonitor(t, bus.Address)
	if got := m.ColorScheme(); got != appearance.Dark {
		t.Fatalf("ColorScheme() = %v, want dark", got)
	}
	onChange, snapshot := collect()
	m.OnColorSchemeChange(onChange)

	// A burst: the deliveries must arrive in signal order, one at a
	// time, with the mapped values — including appearance.Unknown for garbage and
	// a filtered foreign setting. Both garbage signals map to appearance.Unknown;
	// the second is a duplicate value and dedups, like the final
	// re-announcement of dark.
	mock.emit(t, uint32(2))     // -> light
	mock.emit(t, uint32(9))     // -> unknown (out of range)
	mock.emit(t, "prefer-dark") // -> unknown again: deduped
	mock.emit(t, uint32(1))     // -> dark
	mock.emitOther(t)           // -> filtered, no event
	mock.emit(t, uint32(1))     // -> duplicate, no event
	wantEvents(t, snapshot, appearance.Light, appearance.Unknown, appearance.Dark)

	if got := m.ColorScheme(); got != appearance.Dark {
		t.Fatalf("ColorScheme() = %v, want dark", got)
	}
}

func TestNoBusIsInert(t *testing.T) {
	// No daemon anywhere near this address.
	dead := "unix:path=" + filepath.Join(t.TempDir(), "missing")
	before := runtime.NumGoroutine()
	m := newMonitor(func() (*dbus.Conn, error) { return dbus.Connect(dead) },
		10*time.Millisecond)
	t.Cleanup(m.Close)
	onChange, snapshot := collect()
	m.OnColorSchemeChange(onChange)
	if got := m.ColorScheme(); got != appearance.Unknown {
		t.Fatalf("ColorScheme() = %v, want unknown", got)
	}
	// No dial means no goroutines: an inert monitor is free.
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines grew %d -> %d; inert monitor must not spawn", before, after)
	}
	// Close must be a safe no-op.
	m.Close()
	wantNoEvents(t, snapshot)
}

func TestReconnectAfterBusRestart(t *testing.T) {
	// Both daemon generations bind the same socket path, so the
	// monitor's dial seam (a plain address) reaches either one.
	sock := dbustest.SocketPath(t)

	first := dbustest.StartAt(t, sock)
	mock := publishMock(t, first)
	mock.set(1)
	m := newMonitor(func() (*dbus.Conn, error) { return dbus.Connect("unix:path=" + sock) },
		defaultRetry)
	t.Cleanup(m.Close)
	if got := m.ColorScheme(); got != appearance.Dark {
		t.Fatalf("ColorScheme() = %v, want dark", got)
	}
	onChange, snapshot := collect()
	m.OnColorSchemeChange(onChange)

	// Stop generation one and wait for it to be gone: a new daemon on
	// the same path must not race a dying one that still owns the
	// socket (the monitor must never re-read the dead generation).
	first.Stop(t)

	// Generation two on the same socket path, mock now saying light.
	mock2 := publishMockSaying(t, dbustest.StartAt(t, sock), 2)

	// The reconnect path re-reads (2 -> light, delivered because it
	// differs), re-subscribes, and this very emit proves the new
	// subscription works: light again, deduped.
	mock2.emit(t, uint32(2))
	wantEvents(t, snapshot, appearance.Light)
	if got := m.ColorScheme(); got != appearance.Light {
		t.Fatalf("ColorScheme() = %v, want light after reconnect", got)
	}
}

// A bus comes back before the services on it, so a monitor can reconnect
// to a bus with no portal yet: that is no news about the preference.
func TestReconnectingBeforeThePortalIsBackKeepsThePreference(t *testing.T) {
	sock := dbustest.SocketPath(t)
	first := dbustest.StartAt(t, sock)
	publishMockSaying(t, first, 1)
	m := newMonitor(func() (*dbus.Conn, error) { return dbus.Connect("unix:path=" + sock) }, time.Millisecond)
	t.Cleanup(m.Close)
	onChange, snapshot := collect()
	m.OnColorSchemeChange(onChange)

	first.Stop(t)
	second := dbustest.StartAt(t, sock)
	awaitOtherClient(t, second)
	wantNoEvents(t, snapshot)
	if got := m.ColorScheme(); got != appearance.Dark {
		t.Fatalf("ColorScheme() = %v, want the last known dark while the portal is away", got)
	}

	publishMockSaying(t, second, 2)
	wantEvents(t, snapshot, appearance.Light)
	if got := m.ColorScheme(); got != appearance.Light {
		t.Fatalf("ColorScheme() = %v, want light once the portal is back", got)
	}
}

// awaitOtherClient returns once a connection other than the one it asks
// on is on bus: the monitor has reconnected and subscribed or is about to.
func awaitOtherClient(t *testing.T, bus *dbustest.Bus) {
	t.Helper()
	conn := bus.Conn(t)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var names []string
		if err := conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
			t.Fatal(err)
		}
		unique := 0
		for _, n := range names {
			if strings.HasPrefix(n, ":") {
				unique++
			}
		}
		if unique >= 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the monitor never reconnected")
}

func TestAPortalThatStartsLaterIsReadAtOnce(t *testing.T) {
	bus := dbustest.Start(t)
	m := NewOn(bus.Conn(t))
	t.Cleanup(m.Close)
	if got := m.ColorScheme(); got != appearance.Unknown {
		t.Fatalf("ColorScheme() = %v before the portal", got)
	}
	onChange, snapshot := collect()
	m.OnColorSchemeChange(onChange)
	publishMockSaying(t, bus, 1)
	wantEvents(t, snapshot, appearance.Dark)
}

func TestAPortalThatGoesAwayLeavesTheLastKnownPreference(t *testing.T) {
	bus := dbustest.Start(t)
	mock := publishMockSaying(t, bus, 1)
	m := NewOn(bus.Conn(t))
	t.Cleanup(m.Close)
	onChange, snapshot := collect()
	m.OnColorSchemeChange(onChange)
	if err := mock.conn.Close(); err != nil {
		t.Fatal(err)
	}
	wantNoEvents(t, snapshot)
	if got := m.ColorScheme(); got != appearance.Dark {
		t.Fatalf("ColorScheme() = %v with the portal gone", got)
	}
	publishMockSaying(t, bus, 2)
	wantEvents(t, snapshot, appearance.Light)
}

func TestSharedConnFailsSilentOnDeath(t *testing.T) {
	bus := dbustest.Start(t)
	mock := publishMock(t, bus)
	mock.set(1)
	shared := bus.Conn(t)

	m := NewOn(shared)
	t.Cleanup(m.Close)
	if got := m.ColorScheme(); got != appearance.Dark {
		t.Fatalf("ColorScheme() = %v, want dark", got)
	}
	onChange, snapshot := collect()
	m.OnColorSchemeChange(onChange)

	// The bus (or the owner) drops the connection. No re-dial, no
	// fabricated unknown: the last known value stands, no events.
	if err := shared.Close(); err != nil {
		t.Fatalf("close shared conn: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	wantNoEvents(t, snapshot)
	if got := m.ColorScheme(); got != appearance.Dark {
		t.Fatalf("ColorScheme() = %v, want the last known value (dark)", got)
	}
}

func TestCloseStopsGoroutines(t *testing.T) {
	bus := dbustest.Start(t)
	_ = publishMock(t, bus)
	before := runtime.NumGoroutine()

	m := newTestMonitor(t, bus.Address)
	onChange, _ := collect()
	m.OnColorSchemeChange(onChange)
	m.Close()
	m.Close() // idempotent

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("goroutines did not drain after Close: before %d, after %d",
		before, runtime.NumGoroutine())
}

func TestCallbacksRunOnMonitorGoroutine(t *testing.T) {
	bus := dbustest.Start(t)
	mock := publishMock(t, bus)
	mock.set(1)
	m := newTestMonitor(t, bus.Address)

	caller := goroutineID()
	delivered := make(chan uint64, 1)
	m.OnColorSchemeChange(func(a appearance.ColorScheme) {
		delivered <- goroutineID()
	})
	mock.emit(t, uint32(2))
	select {
	case id := <-delivered:
		if id == caller {
			t.Fatal("OnChange ran on the registering goroutine; the documented " +
				"contract is the monitor's goroutine + Application.Invoke")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("callback never ran")
	}
}

func TestCloseBeforeEventsDeliversNothing(t *testing.T) {
	bus := dbustest.Start(t)
	mock := publishMock(t, bus)
	mock.set(1)
	m := newTestMonitor(t, bus.Address)
	onChange, snapshot := collect()
	m.OnColorSchemeChange(onChange)
	m.Close()
	// Signals after Close must not panic or deliver: the loop is gone.
	mock.emit(t, uint32(2))
	wantNoEvents(t, snapshot)
}

func TestAppearanceString(t *testing.T) {
	for _, tc := range []struct {
		a    appearance.ColorScheme
		want string
	}{
		{appearance.Unknown, "unknown"},
		{appearance.Dark, "dark"},
		{appearance.Light, "light"},
		{appearance.ColorScheme(9), "unknown"},
	} {
		if got := tc.a.String(); got != tc.want {
			t.Fatalf("%d.String() = %q, want %q", tc.a, got, tc.want)
		}
	}
}

// collectIconThemes is collect() for the icon-theme stream.
func collectIconThemes() (sink func(string), snapshot func() []string) {
	var mu sync.Mutex
	var got []string
	return func(name string) {
			mu.Lock()
			got = append(got, name)
			mu.Unlock()
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), got...)
		}
}

// wantIconThemes waits for snapshot to equal want exactly.
func wantIconThemes(t *testing.T, snapshot func() []string, want ...string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := snapshot()
		if len(got) == len(want) && slices.Equal(got, want) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for icon themes: got %v, want %v", snapshot(), want)
}

// iconTree writes a two-theme fixture both themes of which hold a
// symbolic icon: the follow test resolves under one and re-resolves
// under the other.
func iconTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">` +
			`<rect width="16" height="16" fill="currentColor"/></svg>`
		if err := os.WriteFile(path, []byte(svg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index := func(theme string) {
		t.Helper()
		dir := filepath.Join(root, theme)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "[Icon Theme]\nName=" + theme + "\nDirectories=symbolic/apps\n\n[symbolic/apps]\nSize=16\n"
		if err := os.WriteFile(filepath.Join(dir, "index.theme"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, theme := range []string{"first", "second"} {
		index(theme)
		write(theme + "/symbolic/apps/face-symbolic.svg")
	}
	return root
}

// TestIconThemeStream pins the live icon-theme half of the monitor
// (#64): the startup read is a baseline, SettingChanged delivers, the
// foreign-namespace noise the filter must drop includes color-scheme
// itself, duplicate announcements dedup, and unregistering stops
// delivery.
func TestIconThemeStream(t *testing.T) {
	bus := dbustest.Start(t)
	mock := publishMock(t, bus)
	mock.setIconTheme("Papirus")
	m := newTestMonitor(t, bus.Address)

	if got := m.IconTheme(); got != "Papirus" {
		t.Fatalf("IconTheme() = %q, want the startup read Papirus", got)
	}

	sink, snapshot := collectIconThemes()
	off := m.OnIconThemeChange(sink)
	mock.emitIconTheme(t, "Adwaita")
	wantIconThemes(t, snapshot, "Adwaita")
	if got := m.IconTheme(); got != "Adwaita" {
		t.Errorf("IconTheme() = %q after the change, want Adwaita", got)
	}

	// Noise: other settings in the same interface, and our own other
	// key (color-scheme), never reach the icon listeners.
	mock.emitOther(t)
	schemes, schemeSnapshot := collect()
	m.OnColorSchemeChange(schemes)
	mock.emit(t, uint32(1))
	wantEvents(t, schemeSnapshot, appearance.Dark)
	if got := snapshot(); len(got) != 1 {
		t.Errorf("icon listeners saw %v across foreign noise", got)
	}

	// A re-announcement of the same theme dedups.
	mock.emitIconTheme(t, "Adwaita")
	time.Sleep(100 * time.Millisecond)
	if got := snapshot(); len(got) != 1 {
		t.Errorf("duplicate icon-theme event: %v", got)
	}

	// Unregistering stops delivery.
	off()
	mock.emitIconTheme(t, "Breeze")
	time.Sleep(100 * time.Millisecond)
	if got := snapshot(); len(got) != 1 {
		t.Errorf("unregistered listener kept receiving: %v", got)
	}
}

// TestIconThemeStreamUnreadableValues pins the value mapping: a
// non-string variant reads empty, and an explicitly emptied setting
// delivers "" — the consumer owns the fallback policy.
func TestIconThemeStreamUnreadableValues(t *testing.T) {
	bus := dbustest.Start(t)
	mock := publishMock(t, bus)
	mock.setIconTheme("Adwaita")
	m := newTestMonitor(t, bus.Address)

	sink, snapshot := collectIconThemes()
	m.OnIconThemeChange(sink)
	mock.emitIconTheme(t, 42)
	wantIconThemes(t, snapshot, "")
	if got := m.IconTheme(); got != "" {
		t.Errorf("IconTheme() = %q after a numeric change, want empty", got)
	}
}

// TestIconThemeFollowsThroughCache is the end-to-end pin of #64: a
// portal SettingChanged flips the followed icons.Cache's theme, drops
// its rasters (the generation moves), and re-resolves a themed icon
// against the new theme — while an emptied setting keeps the previous
// theme. The re-resolution is what live widget.Icons do on their next
// damage pass: they watch the same generation.
func TestIconThemeFollowsThroughCache(t *testing.T) {
	bus := dbustest.Start(t)
	mock := publishMock(t, bus)
	mock.setIconTheme("first")
	m := newTestMonitor(t, bus.Address)

	// The application's wiring, verbatim (app/application.go): the
	// monitor's icon-theme stream feeds the cache's applier.
	cache := icons.New("first")
	cache.SetSearchPaths([]string{iconTree(t)})
	off := m.OnIconThemeChange(cache.ApplyIconTheme)
	t.Cleanup(off)

	// Resolve a symbolic icon under the first theme.
	if _, err := cache.SymbolicIcon("face-symbolic", 16, 120, 0xFF0000FF); err != nil {
		t.Fatalf("symbolic resolve under the first theme: %v", err)
	}
	gen := cache.Generation()

	// The desktop switches themes: the cache follows, rasters drop, and
	// the same icon re-resolves from the new theme's files.
	notified := make(chan string, 1)
	cache.OnIconThemeChanged(func(name string) { notified <- name })
	mock.emitIconTheme(t, "second")
	select {
	case name := <-notified:
		if name != "second" {
			t.Fatalf("notified %q, want second", name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the followed switch never notified")
	}
	if got := cache.Theme(); got != "second" {
		t.Fatalf("cache theme = %q after the switch, want second", got)
	}
	if cache.Generation() == gen {
		t.Error("the followed switch never dropped the cached rasters")
	}
	if _, err := cache.SymbolicIcon("face-symbolic", 16, 120, 0xFF0000FF); err != nil {
		t.Errorf("symbolic resolve after the switch: %v", err)
	}

	// An emptied setting keeps the previous theme.
	mock.emitIconTheme(t, "")
	time.Sleep(200 * time.Millisecond)
	if got := cache.Theme(); got != "second" {
		t.Errorf("empty setting flipped the theme to %q; the previous one stays", got)
	}
	if cache.Generation() == gen {
		t.Error("the empty setting disturbed the cache")
	}
}

func TestUpdateAfterCloseDeliversNothing(t *testing.T) {
	bus := dbustest.Start(t)
	publishMock(t, bus)
	m := newTestMonitor(t, bus.Address)
	onChange, snapshot := collect()
	m.OnColorSchemeChange(onChange)
	m.Close()
	m.scheme.update(m, appearance.Light, true)
	m.scheme.update(m, appearance.Dark, true)
	if got := snapshot(); len(got) != 0 {
		t.Fatalf("a change after Close reached a listener: %v", got)
	}
}

// TestInterfaceFontStream pins the font-name and text-scaling-factor
// settings: the startup read, change delivery per key, and the value
// mapping (a non-string font and a non-positive or non-double scale
// read as unset).
func TestInterfaceFontStream(t *testing.T) {
	bus := dbustest.Start(t)
	mock := publishMock(t, bus)
	mock.setSetting(interfaceNamespace, fontNameKey, "Adwaita Sans 11")
	mock.setSetting(interfaceNamespace, textScaleKey, 1.25)
	m := newTestMonitor(t, bus.Address)
	if got := m.FontName(); got != "Adwaita Sans 11" {
		t.Fatalf("FontName() = %q, want the startup read", got)
	}
	if got := m.TextScale(); got != 1.25 {
		t.Fatalf("TextScale() = %v, want the startup read", got)
	}

	fonts := make(chan string, 4)
	scales := make(chan float64, 4)
	m.OnFontNameChange(func(s string) { fonts <- s })
	m.OnTextScaleChange(func(f float64) { scales <- f })
	emitSetting(t, mock.conn, interfaceNamespace, fontNameKey, "Cantarell 12")
	emitSetting(t, mock.conn, interfaceNamespace, textScaleKey, 1.5)
	if got := <-fonts; got != "Cantarell 12" {
		t.Errorf("font change = %q", got)
	}
	if got := <-scales; got != 1.5 {
		t.Errorf("scale change = %v", got)
	}

	emitSetting(t, mock.conn, interfaceNamespace, fontNameKey, uint32(3))
	emitSetting(t, mock.conn, interfaceNamespace, textScaleKey, -1.0)
	if got := <-fonts; got != "" {
		t.Errorf("a numeric font-name = %q, want unset", got)
	}
	if got := <-scales; got != 0 {
		t.Errorf("a negative scale = %v, want unset", got)
	}
	select {
	case got := <-scales:
		t.Errorf("an extra scale event %v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

// Only the portal is heard: any peer can broadcast a SettingChanged
// from the portal's path, and before the monitor followed the owner of
// the portal's name such a signal moved the preference.
func TestASettingChangedFromAnotherPeerIsNotThePortals(t *testing.T) {
	bus := dbustest.Start(t)
	mock := publishMockSaying(t, bus, 1)
	m := NewOn(bus.Conn(t))
	t.Cleanup(m.Close)
	onChange, snapshot := collect()
	m.OnColorSchemeChange(onChange)

	forger := bus.Conn(t)
	emitSetting(t, forger, schemeNamespace, schemeKey, uint32(2))
	if err := forger.BusObject().Call("org.freedesktop.DBus.GetId", 0).Err; err != nil {
		t.Fatal(err)
	}
	mock.emit(t, uint32(0))
	wantEvents(t, snapshot, appearance.Unknown)
	if got := m.ColorScheme(); got != appearance.Unknown {
		t.Errorf("ColorScheme() = %v, want what the portal signalled", got)
	}
}

// A monitor on a shared connection leaves it as it found it, and closes
// the descriptors of whatever signals the connection is sent meanwhile:
// its channel is handed copies of its own, which nobody else can close.
func TestAMonitorLeavesASharedConnectionAsItWas(t *testing.T) {
	bus := dbustest.Start(t)
	mock := publishMockSaying(t, bus, 1)
	shared := bus.Conn(t)
	before := dbustest.MatchRules(t, shared)
	m := NewOn(shared)
	if got := dbustest.MatchRules(t, shared); len(got) != len(before)+2 {
		t.Errorf("the monitor holds %q, want two rules more than %q: the portal's SettingChanged and its name changing owner", got, before)
	}
	onChange, snapshot := collect()
	m.OnColorSchemeChange(onChange)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	body := []any{schemeNamespace, dbus.UnixFD(w.Fd())}
	msg := &dbus.Message{Type: dbus.TypeSignal, Headers: map[dbus.HeaderField]dbus.Variant{
		dbus.FieldDestination: dbus.MakeVariant(shared.Names()[0]),
		dbus.FieldPath:        dbus.MakeVariant(portalPath),
		dbus.FieldInterface:   dbus.MakeVariant(portalIface),
		dbus.FieldMember:      dbus.MakeVariant("SettingHanded"),
		dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(body...)),
	}, Body: body}
	if err := mock.conn.Send(msg, nil).Err; err != nil {
		t.Fatal(err)
	}
	mock.emit(t, uint32(2))
	wantEvents(t, snapshot, appearance.Light)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("read the pipe = %d, %v; want end of file: the monitor closed its copy of the descriptor", n, err)
	}

	m.Close()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Equal(dbustest.MatchRules(t, shared), before) {
		if time.Now().After(deadline) {
			t.Fatalf("after Close the connection holds %q, want %q", dbustest.MatchRules(t, shared), before)
		}
		time.Sleep(time.Millisecond)
	}
}
