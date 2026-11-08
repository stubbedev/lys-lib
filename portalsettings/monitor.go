// Package portalsettings follows the desktop settings xdg-desktop-portal
// publishes (color scheme, accent color, contrast, icon theme, the
// interface font and its text scaling) over the session bus. Listeners run on the monitor's own goroutine; the
// application bridges them onto its loop.
package portalsettings

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/stubbedev/lys-lib/appearance"
	"github.com/stubbedev/lys-lib/logutil"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// The portal: a well-known name, one object, one method, one signal
// (https://flatpak.github.io/xdg-desktop-portal/). The color-scheme
// value is a variant around uint32: 0 no preference, 1 prefer-dark,
// 2 prefer-light.
const (
	portalName  = "org.freedesktop.portal.Desktop"
	portalPath  = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalIface = "org.freedesktop.portal.Settings"

	readAll    = portalIface + ".ReadAll"
	changedSig = portalIface + ".SettingChanged"

	busName          = "org.freedesktop.DBus"
	nameOwnerChanged = busName + ".NameOwnerChanged"

	schemeNamespace = "org.freedesktop.appearance"
	schemeKey       = "color-scheme"
	accentKey       = "accent-color"
	contrastKey     = "contrast"
	motionKey       = "reduced-motion"

	interfaceNamespace = "org.gnome.desktop.interface"
	iconThemeKey       = "icon-theme"
	fontNameKey        = "font-name"
	textScaleKey       = "text-scaling-factor"

	schemeDark  uint32 = 1
	schemeLight uint32 = 2

	// signalBuffer rides out two failure modes: a slow OnChange handler
	// must not wedge the bus's reader (godbus falls back to spawning a
	// delivery goroutine per signal once the buffer is full), and a
	// burst of signals queued behind one handler's Invoke must not drop
	// events. 32 is far beyond any human flipping dark/light by hand.
	signalBuffer = 32

	// probeTimeout bounds the is-the-portal-here check against the bus
	// daemon (which answers fast or is broken); readTimeout bounds the
	// portal's ReadAll. godbus's default call timeout is 25s — far too
	// long for a startup probe against a missing portal. Both apply to
	// the startup read and to each reconnect refresh, so New and the
	// reconnect loop never hang on a stalled peer.
	probeTimeout = 3 * time.Second
	readTimeout  = 5 * time.Second

	defaultRetry = 250 * time.Millisecond
	maxRetry     = 8 * time.Second
)

// dialSession is New's connection source: a private (non-shared) session
// bus connection from DBUS_SESSION_BUS_ADDRESS, authed and helloed. The
// monitor owns it and closes it, so Close really does drain godbus's
// reader/writer goroutines — the shared dbus.SessionBus singleton would
// keep them for the process lifetime.
func dialSession() (*dbus.Conn, error) {
	return dbus.ConnectSessionBus()
}

// Monitor tracks the desktop's appearance preferences: the
// dark/light color scheme, the icon-theme name (#64), the accent
// color (#88), and the high-contrast preference (#88) - all portal
// settings, all with the same contract. Create one with New (session
// bus) or NewOn (an existing connection), call OnChange, and Close it
// when the application exits. All methods are safe from any
// goroutine; see the package comment for the threading and failure
// contracts.
type Monitor struct {
	// dial produces the (owned) connections for the initial read and
	// every reconnect. nil means the monitor is inert: no bus was
	// reachable at startup, so there is nothing to follow and nothing
	// to retry — Unknown, no events, no goroutines.
	dial func() (*dbus.Conn, error)
	// retry is the first reconnect backoff; it doubles up to maxRetry.
	retry time.Duration

	done chan struct{}

	mu       sync.Mutex
	closed   bool
	scheme   setting[appearance.ColorScheme]
	icons    setting[string]
	accent   setting[appearance.Accent]
	contrast setting[appearance.Contrast]
	motion   setting[appearance.Motion]
	font     setting[string]
	scale    setting[float64]
}

// New starts a monitor on the session bus. It performs the startup read
// synchronously (bounded by probeTimeout + readTimeout), so Appearance
// is valid as soon as New returns. Without a session bus the monitor is
// inert — Unknown, no events, no goroutines (see the package comment).
func New() *Monitor {
	return newMonitor(dialSession, defaultRetry)
}

// NewOn starts a monitor on a caller-provided connection — for apps
// that already hold one shared session bus connection. The monitor
// does NOT own conn: Close leaves it open, and when it dies the
// monitor fails silent (keeps the last known value; no re-dial, since
// only the owner knows how the connection was made). Otherwise the
// behavior matches New.
func NewOn(conn *dbus.Conn) *Monitor {
	return startOn(&Monitor{done: make(chan struct{}), retry: defaultRetry}, conn)
}

// newMonitor dials, performs the startup read, and starts the loop.
// The retry seam keeps the reconnect tests fast without reachable
// globals; New passes defaultRetry.
func newMonitor(dial func() (*dbus.Conn, error), retry time.Duration) *Monitor {
	m := &Monitor{dial: dial, retry: retry, done: make(chan struct{})}
	if dial == nil {
		return m
	}
	conn, err := dial()
	if err != nil {
		// No session bus: no portal desktop. Fail silent — Unknown
		// forever, no goroutines, nothing to leak (documented) — and
		// say so at Debug for programs that installed a logger with
		// logutil.Set.
		logutil.L().Debug("appearance: no session bus; preference monitoring disabled", slog.Any("err", err))
		return m
	}
	return startOn(m, conn)
}

// startOn subscribes first, then reads: the match rule must be live
// before New returns, so a change emitted right after construction is
// queued, not lost. The startup read is the baseline, never an event:
// listeners register after New and must not see history. The reconnect
// refresh passes true instead, so a preference flipped while
// disconnected is still delivered.
func startOn(m *Monitor, conn *dbus.Conn) *Monitor {
	owned := m.dial != nil
	ch := m.subscribe(conn)
	if ch == nil {
		// The connection was already dying: fail silent like no bus.
		if owned {
			_ = conn.Close()
		}
		return m
	}
	m.refresh(conn, false)
	go m.run(conn, ch, owned)
	return m
}

// refresh reads every tracked setting from conn in one ReadAll; deliver decides
// whether a moved value fires its listeners (the startup read is the
// baseline and never fires; a later refresh is an event). With no portal
// on the bus there is nothing to read and nothing is known to have
// changed: the values stay, and the portal's arrival refreshes them.
func (m *Monitor) refresh(conn *dbus.Conn, deliver bool) {
	if !portalOwned(conn) {
		return
	}
	values, ok := readAllSettings(conn)
	if !ok {
		return
	}
	refreshSetting(m, &m.scheme, values, schemeNamespace, schemeKey, schemeValue, appearance.Unknown, deliver)
	refreshSetting(m, &m.icons, values, interfaceNamespace, iconThemeKey, stringValue, "", deliver)
	refreshSetting(m, &m.accent, values, schemeNamespace, accentKey, accentValue, appearance.Accent{}, deliver)
	refreshSetting(m, &m.contrast, values, schemeNamespace, contrastKey, contrastValue, appearance.ContrastUnknown, deliver)
	refreshSetting(m, &m.motion, values, schemeNamespace, motionKey, motionValue, appearance.MotionUnknown, deliver)
	refreshSetting(m, &m.font, values, interfaceNamespace, fontNameKey, stringValue, "", deliver)
	refreshSetting(m, &m.scale, values, interfaceNamespace, textScaleKey, scaleValue, 0, deliver)
}

// ColorScheme returns the current color-scheme preference, Unknown when
// the monitor never connected. Safe from any goroutine.
func (m *Monitor) ColorScheme() appearance.ColorScheme {
	return m.scheme.get(m)
}

// OnColorSchemeChange registers fn for every color-scheme change, on
// the monitor's goroutine; the returned function unregisters it. The
// startup read is the baseline and never fires.
func (m *Monitor) OnColorSchemeChange(fn func(appearance.ColorScheme)) (off func()) {
	return m.scheme.on(m, fn)
}

// Close stops the monitor: the loop goroutine exits, the subscription
// is removed, and a monitor-owned connection is closed (NewOn callers
// keep theirs). Idempotent, safe from any goroutine. A callback already
// running finishes first; Close does not wait for it. Appearance stays
// readable with the last known value.
func (m *Monitor) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	close(m.done)
}

// run is the monitor goroutine: listen on one connection until it or
// the monitor dies, then — only for connections this monitor dialed —
// reconnect, resubscribe, and refresh.
func (m *Monitor) run(conn *dbus.Conn, ch chan *dbus.Signal, owned bool) {
	for {
		if m.listen(conn, ch, owned) {
			return
		}
		// The connection died under us (bus or portal restart). An
		// injected connection cannot be replaced: fail silent, keep the
		// last known value (documented in NewOn).
		if !owned {
			logutil.L().Debug("appearance: session bus connection lost; monitoring stops, keeping last known preference")
			return
		}
		logutil.L().Debug("appearance: session bus connection lost; reconnecting")
		conn = m.redial()
		if conn == nil {
			return
		}
		ch = m.subscribe(conn)
		if ch == nil {
			return
		}
		// A reconnect is a fresh read of a possibly-changed world: the
		// refresh is an event (unlike the startup read), but only when
		// a value actually moved (update dedups). The subscription above
		// is live first, so a portal arriving after this read is heard.
		m.refresh(conn, true)
	}
}

// subscribe installs the bus match rule and the local signal channel —
// in that order, before any read, so nothing between construction and
// the loop start can be missed. nil means the connection refused the
// rule (already dying); the caller treats it as a dead connection.
func (m *Monitor) subscribe(conn *dbus.Conn) chan *dbus.Signal {
	// The match rule is what makes the daemon forward SettingChanged to
	// this connection at all; conn.Signal only registers the local
	// channel that receives what the rule lets through.
	err := conn.AddMatchSignal(
		dbus.WithMatchInterface(portalIface),
		dbus.WithMatchMember("SettingChanged"),
		dbus.WithMatchObjectPath(portalPath),
	)
	if err != nil {
		return nil
	}
	err = conn.AddMatchSignal(
		dbus.WithMatchSender(busName),
		dbus.WithMatchInterface(busName),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, portalName),
	)
	if err != nil {
		return nil
	}
	ch := make(chan *dbus.Signal, signalBuffer)
	conn.Signal(ch)
	return ch
}

// listen consumes the signal channel until the connection dies (returns
// false) or Close fires (returns true). The deferred cleanup runs
// whichever way it ends: deregister, and close the connection if this
// monitor owns it.
func (m *Monitor) listen(conn *dbus.Conn, ch chan *dbus.Signal, owned bool) (stopped bool) {
	defer func() {
		conn.RemoveSignal(ch)
		if owned {
			_ = conn.Close()
		}
	}()
	for {
		select {
		case sig, ok := <-ch:
			if !ok {
				// godbus closes registered signal channels when the
				// connection dies — our disconnect signal.
				return false
			}
			if portalAppeared(sig) {
				m.refresh(conn, true)
				continue
			}
			m.signal(sig)
		case <-m.done:
			return true
		}
	}
}

// signal dispatches one SettingChanged to whichever tracked setting it
// names; every other signal is not ours.
func (m *Monitor) signal(sig *dbus.Signal) {
	if v, ok := settingChanged(sig, schemeNamespace, schemeKey, schemeValue, appearance.Unknown); ok {
		m.scheme.update(m, v, true)
	}
	if v, ok := settingChanged(sig, interfaceNamespace, iconThemeKey, stringValue, ""); ok {
		m.icons.update(m, v, true)
	}
	if v, ok := settingChanged(sig, schemeNamespace, accentKey, accentValue, appearance.Accent{}); ok {
		m.accent.update(m, v, true)
	}
	if v, ok := settingChanged(sig, schemeNamespace, contrastKey, contrastValue, appearance.ContrastUnknown); ok {
		m.contrast.update(m, v, true)
	}
	if v, ok := settingChanged(sig, schemeNamespace, motionKey, motionValue, appearance.MotionUnknown); ok {
		m.motion.update(m, v, true)
	}
	if v, ok := settingChanged(sig, interfaceNamespace, fontNameKey, stringValue, ""); ok {
		m.font.update(m, v, true)
	}
	if v, ok := settingChanged(sig, interfaceNamespace, textScaleKey, scaleValue, 0); ok {
		m.scale.update(m, v, true)
	}
}

// redial reconnects with exponential backoff (retry doubling to
// maxRetry) until a dial succeeds or the monitor closes.
func (m *Monitor) redial() *dbus.Conn {
	delay := m.retry
	for {
		if m.isClosed() {
			return nil
		}
		if conn, err := m.dial(); err == nil {
			return conn
		}
		select {
		case <-m.done:
			return nil
		case <-time.After(delay):
		}
		if delay < maxRetry {
			delay *= 2
		}
	}
}

// IconTheme returns the current icon-theme name (the portal's
// org.gnome.desktop.interface icon-theme setting): empty means no
// portal, no setting, or a value that is not a string. Safe from any
// goroutine.
func (m *Monitor) IconTheme() string {
	return m.icons.get(m)
}

// OnIconThemeChange registers fn to run with the new icon-theme name
// whenever the desktop setting changes (#64); the returned function
// unregisters it. The same threading contract as OnChange: callbacks
// run serialized on the monitor's goroutine, never on a Wayland loop
// goroutine — bridge with Application.Invoke. The startup read is a
// baseline and never fires; an explicitly emptied setting is reported
// as "" (the consumer owns the fallback policy).
func (m *Monitor) OnIconThemeChange(fn func(string)) (off func()) {
	return m.icons.on(m, fn)
}

// Accent returns the desktop's accent-color preference: RGB in [0,1]
// with Known set, or the zero Accent when the desktop published none
// (no portal, no key, unreadable value). Safe from any goroutine.
func (m *Monitor) Accent() appearance.Accent {
	return m.accent.get(m)
}

// OnAccentChange registers fn for every accent-color change; the
// returned function unregisters it. The same threading contract as
// OnChange: callbacks run serialized on the monitor's goroutine -
// bridge with Application.Invoke.
func (m *Monitor) OnAccentChange(fn func(appearance.Accent)) (off func()) {
	return m.accent.on(m, fn)
}

// Contrast returns the desktop's contrast preference: ContrastHigh or
// ContrastUnknown (no portal, no preference, unreadable value). Safe
// from any goroutine.
func (m *Monitor) Contrast() appearance.Contrast {
	return m.contrast.get(m)
}

// OnContrastChange registers fn for every contrast change; the
// returned function unregisters it. Same threading contract as
// OnChange.
func (m *Monitor) OnContrastChange(fn func(appearance.Contrast)) (off func()) {
	return m.contrast.on(m, fn)
}

// Motion returns the desktop's reduced-motion preference: MotionReduced
// or MotionUnknown (no portal, no preference, unreadable value). Safe
// from any goroutine.
func (m *Monitor) Motion() appearance.Motion {
	return m.motion.get(m)
}

// OnMotionChange registers fn for every reduced-motion change; the
// returned function unregisters it. Same threading contract as
// OnChange.
func (m *Monitor) OnMotionChange(fn func(appearance.Motion)) (off func()) {
	return m.motion.on(m, fn)
}

// FontName returns the desktop's interface font description (the
// portal's org.gnome.desktop.interface font-name, "Adwaita Sans 11"):
// empty means no portal, no setting, or a value that is not a string.
// Safe from any goroutine.
func (m *Monitor) FontName() string {
	return m.font.get(m)
}

// OnFontNameChange registers fn for every font-name change; the
// returned function unregisters it. Same threading contract as
// OnChange.
func (m *Monitor) OnFontNameChange(fn func(string)) (off func()) {
	return m.font.on(m, fn)
}

// TextScale returns the desktop's text-scaling-factor (1 is
// unscaled), 0 when it published none or an unusable value. Safe from
// any goroutine.
func (m *Monitor) TextScale() float64 {
	return m.scale.get(m)
}

// OnTextScaleChange registers fn for every text-scaling-factor change;
// the returned function unregisters it. Same threading contract as
// OnChange.
func (m *Monitor) OnTextScaleChange(fn func(float64)) (off func()) {
	return m.scale.on(m, fn)
}

// isClosed reports whether Close has fired.
func (m *Monitor) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// schemeValue maps a portal value — a variant around uint32 — to an
// Appearance. Anything the spec does not define — 0 "no preference",
// out-of-range numbers, wrong types — maps to Unknown rather than
// guessing a polarity.
func schemeValue(v any) appearance.ColorScheme {
	if variant, ok := v.(dbus.Variant); ok {
		v = variant.Value()
	}
	switch u := v.(type) {
	case uint32:
		switch u {
		case schemeDark:
			return appearance.Dark
		case schemeLight:
			return appearance.Light
		}
	}
	return appearance.Unknown
}

// stringValue maps a portal value to its string — a variant around
// string, or anything else mapped to empty rather than guessing.
func stringValue(v any) string {
	if variant, ok := v.(dbus.Variant); ok {
		v = variant.Value()
	}
	s, _ := v.(string)
	return s
}

// call issues one method call with a client-side deadline: godbus's
// default reply timeout is 25s, and neither a startup read nor a
// reconnect refresh may hang that long on a stalled peer.
func call(obj dbus.BusObject, timeout time.Duration, method string, args ...any) ([]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := obj.CallWithContext(ctx, method, 0, args...)
	return c.Body, c.Err
}
