// Package logindtest is a fake systemd-logind on a private bus: the
// manager, the user's sessions on seat0, the calling user, the calls lys
// makes of them, and the signals logind sends.
package logindtest

import (
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/logind"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// The seat every session of the fake is on.
const (
	Seat     logind.SeatID   = "seat0"
	SeatPath dbus.ObjectPath = logind.ManagerPath + "/seat/seat0"
)

const userSelfPath = logind.ManagerPath + "/user/self"

// Config is the user the fake serves, seen from the caller. Own is the
// session every calling process is in, "" for a caller in none (a
// service of the systemd user manager); Display is the user's display
// session, "" for none. They may be the same session.
type Config struct {
	Own     logind.SessionID
	Display logind.SessionID
}

// Event is one entry of the fake's timeline.
type Event string

// The entries Events records, in the order they happen.
const (
	EventLock              Event = "lock"
	EventLockedHint        Event = "locked-hint"
	EventUnlockedHint      Event = "unlocked-hint"
	EventInhibit           Event = "inhibit"
	EventInhibitorReleased Event = "inhibitor-released"
	EventSuspend           Event = "suspend"
	EventReboot            Event = "reboot"
	EventPowerOff          Event = "poweroff"
)

// Call is one method call the fake answered, refused or not. Method is
// the interface's last name and the member, such as Manager.Suspend;
// Path is the object's real path, an alias resolved.
type Call struct {
	Method string
	Path   dbus.ObjectPath
	Args   []any
}

// Logind is the fake. Its manager resolves "auto" to Own, else Display,
// and "self" to Own; the /session/auto and /session/self aliases and
// /user/self are served likewise. Sessions start active and unlocked.
type Logind struct {
	// Bus is the private bus logind owns its name on.
	Bus *dbustest.Bus
	// Calls lists every method call, in order.
	Calls dbustest.Var[[]Call]
	// Events lists the locks, LockedHint changes, inhibitors taken and
	// released and power actions, in the order they happened.
	Events dbustest.Var[[]Event]
	// Inhibitors counts the inhibitors held now.
	Inhibitors dbustest.Var[int]
	// OnLock runs after a Lock is recorded, as the lock screen answering
	// logind's Lock signal would; nil does nothing.
	OnLock dbustest.Var[func()]

	cfg  Config
	conn *dbus.Conn

	mu       sync.Mutex
	sessions map[logind.SessionID]*session
	refusals map[string]*dbus.Error
	sleeping bool
}

type session struct {
	id     logind.SessionID
	path   dbus.ObjectPath
	active bool
	locked bool
}

// Start runs the fake for cfg on a private bus of its own.
func Start(t testing.TB, cfg Config) *Logind {
	t.Helper()
	return Serve(t, dbustest.Start(t), cfg)
}

// StartUnit runs the fake as the system bus of a process the systemd
// user manager started: XDG_SESSION_ID unset for the test, and every
// D-Bus client the test runs dialling the fake for the system bus.
func StartUnit(t testing.TB, cfg Config) *Logind {
	t.Helper()
	t.Setenv("XDG_SESSION_ID", "")
	if err := os.Unsetenv("XDG_SESSION_ID"); err != nil {
		t.Fatalf("logindtest: unset XDG_SESSION_ID: %v", err)
	}
	l := Start(t, cfg)
	l.Bus.UseAsSystemBus(t)
	return l
}

// Serve runs the fake for cfg on bus, which the test shares with other
// services, on a connection of its own.
func Serve(t testing.TB, bus *dbustest.Bus, cfg Config) *Logind {
	t.Helper()
	l := &Logind{Bus: bus, cfg: cfg, conn: bus.Conn(t), sessions: map[logind.SessionID]*session{}, refusals: map[string]*dbus.Error{}}
	for _, id := range []logind.SessionID{cfg.Own, cfg.Display} {
		if id != "" {
			l.sessions[id] = &session{id: id, path: logind.SessionPath(id), active: true}
		}
	}
	exports := []struct {
		path  dbus.ObjectPath
		iface dbusx.Interface
	}{
		{logind.ManagerPath, dbusx.Interface{Name: logind.ManagerInterface, Methods: manager{l}, Signals: []dbusx.SignalDecl{logind.PrepareForSleep}}},
		{SeatPath, dbusx.Interface{Name: logind.SeatInterface, Methods: seat{l}, Properties: dbusx.Getters{"Id": func() any { return string(Seat) }}}},
		{userSelfPath, dbusx.Interface{Name: logind.UserInterface, Methods: user{}, Properties: dbusx.Getters{"Display": l.display}}},
	}
	for _, e := range exports {
		if _, err := dbusx.Export(l.conn, e.path, e.iface); err != nil {
			t.Fatalf("logindtest: export %s: %v", e.path, err)
		}
	}
	paths := map[dbus.ObjectPath]*session{}
	for _, name := range []logind.SessionID{cfg.Own, cfg.Display, logind.AutoSession, logind.SelfSession} {
		if s := l.resolve(name); s != nil {
			paths[logind.SessionPath(name)] = s
		}
	}
	for path, s := range paths {
		iface := dbusx.Interface{
			Name:    logind.SessionInterface,
			Methods: sessionObject{l, s},
			Properties: dbusx.Getters{
				"Id":         func() any { return string(s.id) },
				"Active":     func() any { return l.state(func() bool { return s.active }) },
				"LockedHint": func() any { return l.state(func() bool { return s.locked }) },
				"Seat":       func() any { return objectRef{ID: string(Seat), Path: SeatPath} },
			},
			Signals: []dbusx.SignalDecl{logind.Lock, logind.Unlock, logind.PauseDevice, logind.ResumeDevice},
		}
		if _, err := dbusx.Export(l.conn, path, iface); err != nil {
			t.Fatalf("logindtest: export %s: %v", path, err)
		}
	}
	if _, err := dbusx.Own(l.conn, logind.BusName); err != nil {
		t.Fatalf("logindtest: %v", err)
	}
	return l
}

type objectRef struct {
	ID   string
	Path dbus.ObjectPath
}

func (l *Logind) display() any {
	if s := l.sessions[l.cfg.Display]; s != nil {
		return objectRef{ID: string(s.id), Path: s.path}
	}
	return objectRef{Path: "/"}
}

func (l *Logind) state(read func() bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return read()
}

func (l *Logind) resolve(name logind.SessionID) *session {
	switch name {
	case logind.AutoSession:
		if s := l.sessions[l.cfg.Own]; s != nil {
			return s
		}
		return l.sessions[l.cfg.Display]
	case logind.SelfSession:
		return l.sessions[l.cfg.Own]
	}
	return l.sessions[name]
}

func (l *Logind) session(t testing.TB, id logind.SessionID) *session {
	t.Helper()
	s := l.sessions[id]
	if s == nil {
		t.Fatalf("logindtest: no session %q", id)
	}
	return s
}

// Refuse makes every later call of method (such as Manager.Suspend)
// fail with err; nil answers it again.
func (l *Logind) Refuse(method string, err *dbus.Error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil {
		delete(l.refusals, method)
		return
	}
	l.refusals[method] = err
}

// CallsTo lists the calls of method, in order.
func (l *Logind) CallsTo(method string) []Call {
	var out []Call
	for _, c := range l.Calls.Load() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Methods lists the methods called, in order.
func (l *Logind) Methods() []string {
	calls := l.Calls.Load()
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.Method
	}
	return out
}

func (l *Logind) enter(path dbus.ObjectPath, method string, args ...any) *dbus.Error {
	l.Calls.Update(func(calls []Call) []Call {
		return append(calls, Call{Method: method, Path: path, Args: args})
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refusals[method]
}

func (l *Logind) record(event Event) {
	l.Events.Update(func(events []Event) []Event { return append(events, event) })
}

// SetLockedHint changes session id's LockedHint and announces it with
// PropertiesChanged, as logind does when the lock screen sets it.
func (l *Logind) SetLockedHint(t testing.TB, id logind.SessionID, locked bool) {
	t.Helper()
	if err := l.setLockedHint(l.session(t, id), locked); err != nil {
		t.Fatalf("logindtest: %v", err)
	}
}

func (l *Logind) setLockedHint(s *session, locked bool) error {
	l.mu.Lock()
	s.locked = locked
	l.mu.Unlock()
	event := EventUnlockedHint
	if locked {
		event = EventLockedHint
	}
	l.record(event)
	return l.propertyChanged(s, "LockedHint", locked)
}

// SetActive changes whether session id is in the foreground and
// announces it with PropertiesChanged, as logind does on a VT switch.
func (l *Logind) SetActive(t testing.TB, id logind.SessionID, active bool) {
	t.Helper()
	if err := l.setActive(l.session(t, id), active); err != nil {
		t.Fatalf("logindtest: %v", err)
	}
}

func (l *Logind) setActive(s *session, active bool) error {
	l.mu.Lock()
	s.active = active
	l.mu.Unlock()
	return l.propertyChanged(s, "Active", active)
}

func (l *Logind) propertyChanged(s *session, name string, v bool) error {
	return l.conn.Emit(s.path, "org.freedesktop.DBus.Properties.PropertiesChanged", logind.SessionInterface, map[string]dbus.Variant{name: dbus.MakeVariant(v)}, []string{})
}

// PrepareForSleep emits the manager's PrepareForSleep(start), as logind
// does around a sleep someone else asked for.
func (l *Logind) PrepareForSleep(t testing.TB, start bool) {
	t.Helper()
	l.mu.Lock()
	l.sleeping = start
	l.mu.Unlock()
	if err := logind.PrepareForSleep.Emit(l.conn, logind.ManagerPath, logind.Sleep{Start: start}); err != nil {
		t.Fatalf("logindtest: %v", err)
	}
}

// Lock emits session id's Lock signal, as `loginctl lock-session` has
// logind do.
func (l *Logind) Lock(t testing.TB, id logind.SessionID) {
	t.Helper()
	if err := logind.Lock.Emit(l.conn, l.session(t, id).path, logind.Locked{}); err != nil {
		t.Fatalf("logindtest: %v", err)
	}
}

// Unlock emits session id's Unlock signal, as `loginctl unlock-session`
// has logind do.
func (l *Logind) Unlock(t testing.TB, id logind.SessionID) {
	t.Helper()
	if err := logind.Unlock.Emit(l.conn, l.session(t, id).path, logind.Unlocked{}); err != nil {
		t.Fatalf("logindtest: %v", err)
	}
}

// PauseDevice emits session id's PauseDevice for d.
func (l *Logind) PauseDevice(t testing.TB, id logind.SessionID, d logind.Device, kind logind.PauseKind) {
	t.Helper()
	if err := logind.PauseDevice.Emit(l.conn, l.session(t, id).path, logind.DevicePaused{Major: d.Major, Minor: d.Minor, Kind: kind}); err != nil {
		t.Fatalf("logindtest: %v", err)
	}
}

// ResumeDevice emits session id's ResumeDevice for d, carrying a
// descriptor of /dev/null.
func (l *Logind) ResumeDevice(t testing.TB, id logind.SessionID, d logind.Device) {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("logindtest: open the device stand-in: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := logind.ResumeDevice.Emit(l.conn, l.session(t, id).path, logind.DeviceResumed{Major: d.Major, Minor: d.Minor, FD: dbus.UnixFD(intconv.Must[int32](f.Fd()))}); err != nil {
		t.Fatalf("logindtest: %v", err)
	}
}

func failed(err error) *dbus.Error { return dbus.MakeFailedError(err) }

type manager struct{ l *Logind }

func (m manager) GetSession(name string) (dbus.ObjectPath, *dbus.Error) {
	if err := m.l.enter(logind.ManagerPath, "Manager.GetSession", name); err != nil {
		return "", err
	}
	s := m.l.resolve(logind.SessionID(name))
	if s == nil {
		return "", dbus.NewError(logind.ErrNoSuchSession, []any{"No session '" + name + "' known"})
	}
	return s.path, nil
}

func (m manager) GetSessionByPID(pid uint32) (dbus.ObjectPath, *dbus.Error) {
	if err := m.l.enter(logind.ManagerPath, "Manager.GetSessionByPID", pid); err != nil {
		return "", err
	}
	s := m.l.sessions[m.l.cfg.Own]
	if s == nil {
		return "", dbus.NewError(logind.ErrNoSessionForPID, []any{"PID does not belong to any known session"})
	}
	return s.path, nil
}

func (m manager) LockSession(name string) *dbus.Error {
	if err := m.l.enter(logind.ManagerPath, "Manager.LockSession", name); err != nil {
		return err
	}
	s := m.l.resolve(logind.SessionID(name))
	if s == nil {
		return dbus.NewError(logind.ErrNoSuchSession, []any{"No session '" + name + "' known"})
	}
	return m.l.lock(s)
}

func (m manager) ActivateSession(name string) *dbus.Error {
	if err := m.l.enter(logind.ManagerPath, "Manager.ActivateSession", name); err != nil {
		return err
	}
	target := m.l.resolve(logind.SessionID(name))
	if target == nil {
		return dbus.NewError(logind.ErrNoSuchSession, []any{"No session '" + name + "' known"})
	}
	ids := make([]logind.SessionID, 0, len(m.l.sessions))
	for id := range m.l.sessions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var errs []error
	for _, id := range ids {
		s := m.l.sessions[id]
		active := s == target
		if m.l.state(func() bool { return s.active }) != active {
			errs = append(errs, m.l.setActive(s, active))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return failed(err)
	}
	return nil
}

func (m manager) Suspend(interactive bool) *dbus.Error {
	if err := m.l.enter(logind.ManagerPath, "Manager.Suspend", interactive); err != nil {
		return err
	}
	m.l.mu.Lock()
	if m.l.sleeping {
		m.l.mu.Unlock()
		return dbus.NewError(logind.ErrOperationInProgress, []any{"There's already a shutdown or sleep operation in progress"})
	}
	m.l.sleeping = true
	m.l.mu.Unlock()
	m.l.record(EventSuspend)
	if err := logind.PrepareForSleep.Emit(m.l.conn, logind.ManagerPath, logind.Sleep{Start: true}); err != nil {
		return failed(err)
	}
	return nil
}

func (m manager) Reboot(interactive bool) *dbus.Error {
	if err := m.l.enter(logind.ManagerPath, "Manager.Reboot", interactive); err != nil {
		return err
	}
	m.l.record(EventReboot)
	return nil
}

func (m manager) PowerOff(interactive bool) *dbus.Error {
	if err := m.l.enter(logind.ManagerPath, "Manager.PowerOff", interactive); err != nil {
		return err
	}
	m.l.record(EventPowerOff)
	return nil
}

var inhibitable = []string{"shutdown", "sleep", "idle", "handle-power-key", "handle-suspend-key", "handle-hibernate-key", "handle-lid-switch"}

func (m manager) Inhibit(what, who, why, mode string) (*os.File, *dbus.Error) {
	if err := m.l.enter(logind.ManagerPath, "Manager.Inhibit", what, who, why, mode); err != nil {
		return nil, err
	}
	for w := range strings.SplitSeq(what, ":") {
		if !slices.Contains(inhibitable, w) {
			return nil, dbusx.InvalidArgs("Invalid what specification " + what)
		}
	}
	if mode != "block" && mode != "delay" {
		return nil, dbusx.InvalidArgs("Invalid mode specification " + mode)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, failed(err)
	}
	m.l.Inhibitors.Update(func(n int) int { return n + 1 })
	m.l.record(EventInhibit)
	go func() {
		_, _ = io.Copy(io.Discard, r)
		_ = r.Close()
		m.l.Inhibitors.Update(func(n int) int { return n - 1 })
		m.l.record(EventInhibitorReleased)
	}()
	return w, nil
}

func (l *Logind) lock(s *session) *dbus.Error {
	l.record(EventLock)
	if err := logind.Lock.Emit(l.conn, s.path, logind.Locked{}); err != nil {
		return failed(err)
	}
	if hook := l.OnLock.Load(); hook != nil {
		go hook()
	}
	return nil
}

type sessionObject struct {
	l *Logind
	s *session
}

func (o sessionObject) enter(method string, args ...any) *dbus.Error {
	return o.l.enter(o.s.path, "Session."+method, args...)
}

func (o sessionObject) Terminate() *dbus.Error {
	return o.enter("Terminate")
}

func (o sessionObject) Lock() *dbus.Error {
	if err := o.enter("Lock"); err != nil {
		return err
	}
	return o.l.lock(o.s)
}

func (o sessionObject) SetLockedHint(locked bool) *dbus.Error {
	if err := o.enter("SetLockedHint", locked); err != nil {
		return err
	}
	if err := o.l.setLockedHint(o.s, locked); err != nil {
		return failed(err)
	}
	return nil
}

func (o sessionObject) SetBrightness(subsystem, name string, value uint32) *dbus.Error {
	if err := o.enter("SetBrightness", subsystem, name, value); err != nil {
		return err
	}
	if subsystem != "backlight" && subsystem != "leds" {
		return dbusx.InvalidArgs("Subsystem type " + subsystem + " not supported")
	}
	return nil
}

func (o sessionObject) TakeControl(force bool) *dbus.Error {
	return o.enter("TakeControl", force)
}

func (o sessionObject) ReleaseControl() *dbus.Error {
	return o.enter("ReleaseControl")
}

func (o sessionObject) TakeDevice(major, minor uint32) (*os.File, bool, *dbus.Error) {
	if err := o.enter("TakeDevice", major, minor); err != nil {
		return nil, false, err
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		return nil, false, failed(err)
	}
	return f, !o.l.state(func() bool { return o.s.active }), nil
}

func (o sessionObject) ReleaseDevice(major, minor uint32) *dbus.Error {
	return o.enter("ReleaseDevice", major, minor)
}

func (o sessionObject) PauseDeviceComplete(major, minor uint32) *dbus.Error {
	return o.enter("PauseDeviceComplete", major, minor)
}

type seat struct{ l *Logind }

func (s seat) SwitchTo(vt uint32) *dbus.Error {
	if err := s.l.enter(SeatPath, "Seat.SwitchTo", vt); err != nil {
		return err
	}
	if vt == 0 {
		return dbusx.InvalidArgs("Invalid virtual terminal")
	}
	return nil
}

type user struct{}
