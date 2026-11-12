package logind_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/logind"
	"github.com/stubbedev/lys-lib/logind/logindtest"
	"github.com/stubbedev/lys-lib/sockdir"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const (
	own     logind.SessionID = "c2"
	display logind.SessionID = "3"
	wait                     = 5 * time.Second
)

func manager(t *testing.T, cfg logindtest.Config) (*logindtest.Logind, logind.Manager) {
	t.Helper()
	fake := logindtest.Start(t, cfg)
	return fake, logind.NewManager(fake.Bus.Conn(t))
}

func session(t *testing.T, m logind.Manager, id logind.SessionID) logind.Session {
	t.Helper()
	s, err := m.GetSession(t.Context(), id)
	if err != nil {
		t.Fatalf("GetSession(%q): %v", id, err)
	}
	return s
}

func TestSessionPathEscapesTheID(t *testing.T) {
	t.Parallel()
	for id, want := range map[logind.SessionID]dbus.ObjectPath{
		"c2":   "/org/freedesktop/login1/session/c2",
		"3":    "/org/freedesktop/login1/session/_33",
		"31":   "/org/freedesktop/login1/session/_331",
		"auto": "/org/freedesktop/login1/session/auto",
		"a-b":  "/org/freedesktop/login1/session/a_2db",
		"":     "/org/freedesktop/login1/session/_",
	} {
		if got := logind.SessionPath(id); got != want {
			t.Errorf("SessionPath(%q) = %s, want %s", id, got, want)
		}
	}
}

func TestGetSessionResolvesAsLogindDoes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  logindtest.Config
		id   logind.SessionID
		want logind.SessionID
	}{
		{"auto outside any session is the display session", logindtest.Config{Display: display}, logind.AutoSession, display},
		{"auto inside a session is that session", logindtest.Config{Own: own, Display: display}, logind.AutoSession, own},
		{"self is the caller's own session", logindtest.Config{Own: own, Display: display}, logind.SelfSession, own},
		{"an id is that session", logindtest.Config{Own: own, Display: display}, display, display},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, m := manager(t, tc.cfg)
			s := session(t, m, tc.id)
			if s.Path() != logind.SessionPath(tc.want) {
				t.Errorf("GetSession(%q) = %s, want the real path %s", tc.id, s.Path(), logind.SessionPath(tc.want))
			}
			if id, err := s.ID(t.Context()); err != nil || id != tc.want {
				t.Errorf("ID = %q, %v; want %q", id, err, tc.want)
			}
		})
	}
}

func TestErrorsNameTheMethodAndTheObjectAndKeepLogindsError(t *testing.T) {
	t.Parallel()
	_, m := manager(t, logindtest.Config{})
	_, err := m.GetSession(t.Context(), logind.SelfSession)
	want := `logind Manager.GetSession("self") on /org/freedesktop/login1: `
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("GetSession = %v, want it to start %q", err, want)
	}
	if derr, ok := errors.AsType[dbus.Error](err); !ok || derr.Name != logind.ErrNoSuchSession {
		t.Errorf("GetSession = %#v, want logind's %s", err, logind.ErrNoSuchSession)
	}

	_, err = m.GetSessionByPID(t.Context(), 42)
	if derr, ok := errors.AsType[dbus.Error](err); !ok || derr.Name != logind.ErrNoSessionForPID || !strings.Contains(err.Error(), "Manager.GetSessionByPID(42)") {
		t.Errorf("GetSessionByPID = %v, want logind's %s naming the pid", err, logind.ErrNoSessionForPID)
	}
}

func TestWithoutLogindTheCallFails(t *testing.T) {
	t.Parallel()
	m := logind.NewManager(dbustest.Start(t).Conn(t))
	if _, err := m.GetSession(t.Context(), logind.AutoSession); err == nil || !strings.Contains(err.Error(), "Manager.GetSession") {
		t.Errorf("GetSession = %v, want the failed call named", err)
	}
}

func TestAZeroHandleFailsInsteadOfPanicking(t *testing.T) {
	t.Parallel()
	var s logind.Session
	if err := s.Lock(t.Context()); err == nil || !strings.Contains(err.Error(), "Session.Lock()") {
		t.Errorf("Lock on a zero Session = %v", err)
	}
	if _, err := s.Watch(t.Context(), func(logind.Event, error) {}); err == nil {
		t.Error("a zero Session was watched")
	}
}

func TestGetSessionByPIDIsTheProcessSession(t *testing.T) {
	t.Parallel()
	_, m := manager(t, logindtest.Config{Own: own, Display: display})
	s, err := m.GetSessionByPID(t.Context(), 42)
	if err != nil || s.Path() != logind.SessionPath(own) {
		t.Errorf("GetSessionByPID = %s, %v; want %s", s.Path(), err, logind.SessionPath(own))
	}
}

func TestSessionProperties(t *testing.T) {
	t.Parallel()
	fake, m := manager(t, logindtest.Config{Display: display})
	s := session(t, m, logind.AutoSession)
	if active, err := s.Active(t.Context()); err != nil || !active {
		t.Errorf("Active = %v, %v", active, err)
	}
	if locked, err := s.LockedHint(t.Context()); err != nil || locked {
		t.Errorf("LockedHint = %v, %v", locked, err)
	}
	seat, err := s.Seat(t.Context())
	if err != nil || seat.ID() != logindtest.Seat || seat.Path() != logindtest.SeatPath {
		t.Errorf("Seat = %q at %s, %v", seat.ID(), seat.Path(), err)
	}
	fake.SetLockedHint(t, display, true)
	fake.SetActive(t, display, false)
	if locked, err := s.LockedHint(t.Context()); err != nil || !locked {
		t.Errorf("LockedHint after it was set = %v, %v", locked, err)
	}
	if active, err := s.Active(t.Context()); err != nil || active {
		t.Errorf("Active after the session went to the background = %v, %v", active, err)
	}
}

func TestUserDisplay(t *testing.T) {
	t.Parallel()
	_, m := manager(t, logindtest.Config{Own: own, Display: display})
	s, err := m.SelfUser().Display(t.Context())
	if err != nil || s.Path() != logind.SessionPath(display) {
		t.Fatalf("Display = %s, %v", s.Path(), err)
	}
	if id, err := s.ID(t.Context()); err != nil || id != display {
		t.Errorf("Display's ID = %q, %v", id, err)
	}

	_, m = manager(t, logindtest.Config{Own: own})
	if _, err := m.SelfUser().Display(t.Context()); !errors.Is(err, logind.ErrNoDisplay) {
		t.Errorf("Display of a user without one = %v, want ErrNoDisplay", err)
	}
}

func TestSessionCalls(t *testing.T) {
	t.Parallel()
	fake, m := manager(t, logindtest.Config{Own: own})
	ctx := t.Context()
	s := session(t, m, logind.SelfSession)
	seat, err := s.Seat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dev := logind.Device{Major: 226, Minor: 0}
	if err := s.TakeControl(ctx, false); err != nil {
		t.Fatal(err)
	}
	fd, inactive, err := s.TakeDevice(ctx, dev)
	if err != nil || fd < 0 || inactive {
		t.Fatalf("TakeDevice = %d, inactive %v, %v", fd, inactive, err)
	}
	if err := os.NewFile(uintptr(fd), "device").Close(); err != nil {
		t.Errorf("the device descriptor is not one: %v", err)
	}
	if err := errors.Join(
		s.PauseDeviceComplete(ctx, dev),
		s.ReleaseDevice(ctx, dev),
		seat.SwitchTo(ctx, 2),
		s.SetLockedHint(ctx, true),
		s.SetBrightness(ctx, logind.Backlight, "intel_backlight", 400),
		s.Lock(ctx),
		s.ReleaseControl(ctx),
		s.Terminate(ctx),
	); err != nil {
		t.Fatal(err)
	}
	path := logind.SessionPath(own)
	want := []logindtest.Call{
		{Method: "Manager.GetSession", Path: logind.ManagerPath, Args: []any{"self"}},
		{Method: "Session.TakeControl", Path: path, Args: []any{false}},
		{Method: "Session.TakeDevice", Path: path, Args: []any{uint32(226), uint32(0)}},
		{Method: "Session.PauseDeviceComplete", Path: path, Args: []any{uint32(226), uint32(0)}},
		{Method: "Session.ReleaseDevice", Path: path, Args: []any{uint32(226), uint32(0)}},
		{Method: "Seat.SwitchTo", Path: logindtest.SeatPath, Args: []any{uint32(2)}},
		{Method: "Session.SetLockedHint", Path: path, Args: []any{true}},
		{Method: "Session.SetBrightness", Path: path, Args: []any{"backlight", "intel_backlight", uint32(400)}},
		{Method: "Session.Lock", Path: path, Args: nil},
		{Method: "Session.ReleaseControl", Path: path, Args: nil},
		{Method: "Session.Terminate", Path: path, Args: nil},
	}
	if got := fake.Calls.Load(); !reflect.DeepEqual(got, want) {
		t.Errorf("logind saw\n%v\nwant\n%v", got, want)
	}
	if locked, err := s.LockedHint(ctx); err != nil || !locked {
		t.Errorf("LockedHint after SetLockedHint(true) = %v, %v", locked, err)
	}
	if err := seat.SwitchTo(ctx, 0); err == nil || !strings.Contains(err.Error(), "Seat.SwitchTo(0) on "+string(logindtest.SeatPath)) {
		t.Errorf("SwitchTo(0) = %v, want logind's refusal naming the call", err)
	}
	if err := s.SetBrightness(ctx, logind.Subsystem(9), "x", 1); err == nil {
		t.Error("SetBrightness took an unknown subsystem")
	}
}

func TestTheSelfAliasNeedsNoLookup(t *testing.T) {
	t.Parallel()
	fake, m := manager(t, logindtest.Config{Own: own, Display: display})
	if err := m.SelfSession().SetBrightness(t.Context(), logind.Backlight, "acpi_video0", 7); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls.Load()
	if len(calls) != 1 || calls[0].Method != "Session.SetBrightness" || calls[0].Path != logind.SessionPath(own) {
		t.Errorf("logind saw %v, want one SetBrightness on %s", calls, logind.SessionPath(own))
	}

	_, m = manager(t, logindtest.Config{Display: display})
	if err := m.SelfSession().SetBrightness(t.Context(), logind.Backlight, "acpi_video0", 7); err == nil {
		t.Error("SetBrightness through /session/self succeeded for a caller in no session")
	}
}

func TestManagerSessionCalls(t *testing.T) {
	t.Parallel()
	fake, m := manager(t, logindtest.Config{Own: own, Display: display})
	if err := m.ActivateSession(t.Context(), display); err != nil {
		t.Fatal(err)
	}
	if active, err := session(t, m, own).Active(t.Context()); err != nil || active {
		t.Errorf("the other session after ActivateSession: active %v, %v", active, err)
	}
	if active, err := session(t, m, display).Active(t.Context()); err != nil || !active {
		t.Errorf("the activated session: active %v, %v", active, err)
	}
	if err := m.LockSession(t.Context(), display); err != nil {
		t.Fatal(err)
	}
	if got := fake.Events.Load(); !slices.Equal(got, []logindtest.Event{logindtest.EventLock}) {
		t.Errorf("events %v, want the lock", got)
	}
	if err := m.LockSession(t.Context(), "9"); err == nil || !strings.Contains(err.Error(), `Manager.LockSession("9")`) {
		t.Errorf("LockSession of no session = %v", err)
	}
}

func TestPowerActions(t *testing.T) {
	t.Parallel()
	fake, m := manager(t, logindtest.Config{Display: display})
	for _, a := range []logind.PowerAction{logind.Reboot, logind.PowerOff, logind.Suspend} {
		if err := m.Power(t.Context(), a); err != nil {
			t.Fatalf("Power(%s): %v", a, err)
		}
	}
	want := []logindtest.Event{logindtest.EventReboot, logindtest.EventPowerOff, logindtest.EventSuspend}
	if got := fake.Events.Load(); !slices.Equal(got, want) {
		t.Errorf("events %v, want %v", got, want)
	}
	for _, c := range fake.Calls.Load() {
		if !reflect.DeepEqual(c.Args, []any{false}) {
			t.Errorf("%s(%v), want it never interactive", c.Method, c.Args)
		}
	}
	err := m.Power(t.Context(), logind.Suspend)
	if derr, ok := errors.AsType[dbus.Error](err); !ok || derr.Name != logind.ErrOperationInProgress || !strings.Contains(err.Error(), "Manager.Suspend(false)") {
		t.Errorf("a second Suspend while asleep = %v, want logind's %s", err, logind.ErrOperationInProgress)
	}
	calls := len(fake.Calls.Load())
	if err := m.Power(t.Context(), logind.PowerAction(0)); err == nil || !strings.Contains(err.Error(), "PowerAction(0)") {
		t.Errorf("Power of no action = %v", err)
	}
	if n := len(fake.Calls.Load()); n != calls {
		t.Error("an invalid power action reached logind")
	}
}

func TestPowerActionNames(t *testing.T) {
	t.Parallel()
	for a, want := range map[logind.PowerAction]string{logind.Suspend: "suspend", logind.Reboot: "reboot", logind.PowerOff: "poweroff", 4: "PowerAction(4)"} {
		if got := a.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", a, got, want)
		}
	}
}

func TestARefusedCallReportsLogindsError(t *testing.T) {
	t.Parallel()
	fake, m := manager(t, logindtest.Config{Display: display})
	fake.Refuse("Manager.Reboot", dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", []any{"not allowed"}))
	err := m.Power(t.Context(), logind.Reboot)
	if derr, ok := errors.AsType[dbus.Error](err); !ok || derr.Name != "org.freedesktop.DBus.Error.AccessDenied" {
		t.Errorf("a refused Reboot = %v", err)
	}
	fake.Refuse("Manager.Reboot", nil)
	if err := m.Power(t.Context(), logind.Reboot); err != nil {
		t.Errorf("Reboot once answered again = %v", err)
	}
}

func TestInhibitHoldsUntilTheFileCloses(t *testing.T) {
	t.Parallel()
	fake, m := manager(t, logindtest.Config{Display: display})
	f, err := m.Inhibit(t.Context(), logind.InhibitIdle|logind.InhibitSleep|logind.InhibitShutdown, "lys-shell portal", "Playing", logind.InhibitBlock)
	if err != nil {
		t.Fatal(err)
	}
	want := logindtest.Call{Method: "Manager.Inhibit", Path: logind.ManagerPath, Args: []any{"shutdown:sleep:idle", "lys-shell portal", "Playing", "block"}}
	if got := fake.CallsTo("Manager.Inhibit"); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("logind saw %v, want %v", got, want)
	}
	if n := fake.Inhibitors.Load(); n != 1 {
		t.Errorf("%d inhibitors held, want 1", n)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(wait); fake.Inhibitors.Load() != 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the inhibitor outlived its file")
		}
	}
	if got := fake.Events.Load(); !slices.Equal(got, []logindtest.Event{logindtest.EventInhibit, logindtest.EventInhibitorReleased}) {
		t.Errorf("events %v", got)
	}
}

func TestInhibitRefusesWhatLogindWould(t *testing.T) {
	t.Parallel()
	fake, m := manager(t, logindtest.Config{Display: display})
	for _, tc := range []struct {
		what logind.InhibitWhat
		mode logind.InhibitMode
	}{
		{0, logind.InhibitBlock},
		{1 << 6, logind.InhibitBlock},
		{logind.InhibitSleep, 0},
	} {
		if f, err := m.Inhibit(t.Context(), tc.what, "test", "why", tc.mode); err == nil {
			_ = f.Close()
			t.Errorf("Inhibit(%s, %s) succeeded", tc.what, tc.mode)
		}
	}
	if calls := fake.Calls.Load(); len(calls) != 0 {
		t.Errorf("an invalid inhibitor reached logind: %v", calls)
	}
}

func TestInhibitWhatNames(t *testing.T) {
	t.Parallel()
	for w, want := range map[logind.InhibitWhat]string{
		logind.InhibitLidSwitch:                        "handle-lid-switch",
		logind.InhibitSleep:                            "sleep",
		logind.InhibitIdle | logind.InhibitShutdown:    "shutdown:idle",
		logind.InhibitSleep | logind.InhibitWhat(1<<7): "sleep:InhibitWhat(0x80)",
	} {
		if got := w.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
	if logind.InhibitDelay.String() != "delay" || logind.InhibitBlock.String() != "block" {
		t.Errorf("modes %s, %s", logind.InhibitDelay, logind.InhibitBlock)
	}
}

type heldSession struct {
	entered chan struct{}
	release chan struct{}
}

func (h *heldSession) GetSession(string) (dbus.ObjectPath, *dbus.Error) {
	h.entered <- struct{}{}
	<-h.release
	return "/", nil
}

func TestCallsEndWithTheirContext(t *testing.T) {
	t.Parallel()
	bus := dbustest.Start(t)
	server := bus.Conn(t)
	held := &heldSession{entered: make(chan struct{}, 1), release: make(chan struct{})}
	t.Cleanup(func() { close(held.release) })
	if err := server.Export(held, logind.ManagerPath, logind.ManagerInterface); err != nil {
		t.Fatal(err)
	}
	if _, err := server.RequestName(logind.BusName, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	m := logind.NewManager(bus.Conn(t))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := m.GetSession(ctx, logind.AutoSession)
		done <- err
	}()
	select {
	case <-held.entered:
	case <-time.After(wait):
		t.Fatal("GetSession never reached logind")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("GetSession = %v, want its context's end", err)
		}
	case <-time.After(wait):
		t.Fatal("GetSession outlived its context with logind silent")
	}
}

func TestDialConnectsToTheSystemBus(t *testing.T) {
	fake := logindtest.StartUnit(t, logindtest.Config{Display: display})
	conn, err := logind.Dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := logind.NewManager(conn).GetSession(t.Context(), logind.AutoSession); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.CallsTo("Manager.GetSession")); n != 1 {
		t.Errorf("%d GetSession calls, want 1", n)
	}
}

func TestDialEndsWithItsContextOnASilentBus(t *testing.T) {
	sock := filepath.Join(sockdir.Dir(t), "silent")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+sock)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		conn, err := logind.Dial(ctx)
		if err == nil {
			_ = conn.Close()
		}
		done <- err
	}()
	peer, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a connection from a bus that never answered")
		}
	case <-time.After(wait):
		t.Fatal("Dial outlived its context on a silent bus")
	}
}

func TestDialWithoutABusNamesLogindAndTheSystemBus(t *testing.T) {
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+filepath.Join(sockdir.Dir(t), "absent"))
	conn, err := logind.Dial(t.Context())
	if err == nil {
		_ = conn.Close()
		t.Fatal("Dial with no system bus connected")
	}
	if want := "logind: connect to the system bus: "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error %q, want it to start %q", err, want)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %q is the dial's timeout, want the dial's own failure at once", err)
	}
}

func TestLidClosedReadsTheManagersLidState(t *testing.T) {
	t.Parallel()
	fake, m := manager(t, logindtest.Config{Display: display})
	for _, closed := range []bool{false, true, false} {
		fake.SetLidClosed(closed)
		got, err := m.LidClosed(t.Context())
		if err != nil || got != closed {
			t.Fatalf("LidClosed = %v, %v; want %v", got, err, closed)
		}
	}
}
