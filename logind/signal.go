package logind

import (
	"context"
	"errors"
	"fmt"

	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// Event is one decoded logind signal about a watched session: Sleep,
// Locked, Unlocked, DevicePaused, DeviceResumed, ActiveChanged or
// LockedHintChanged.
type Event interface{ event() }

// Sleep is PrepareForSleep's argument: Start is true as the system is
// about to sleep, false once it has woken.
type Sleep struct{ Start bool }

// Locked is the session's Lock signal: its lock screen is to lock it.
type Locked struct{}

// Unlocked is the session's Unlock signal: its lock screen is to unlock
// it.
type Unlocked struct{}

// PauseKind is how a PauseDevice pauses its device.
type PauseKind string

// The PauseDevice kinds: PausePause waits for PauseDeviceComplete,
// PauseForce has paused the device already, PauseGone says it was
// removed.
const (
	PausePause PauseKind = "pause"
	PauseForce PauseKind = "force"
	PauseGone  PauseKind = "gone"
)

// DevicePaused is the session's PauseDevice signal.
type DevicePaused struct {
	Major, Minor uint32
	Kind         PauseKind
}

// Device is the paused device.
func (p DevicePaused) Device() Device { return Device{Major: p.Major, Minor: p.Minor} }

// DeviceResumed is the session's ResumeDevice signal. FD is a new
// descriptor for the device, the handler's to close.
type DeviceResumed struct {
	Major, Minor uint32
	FD           dbus.UnixFD
}

// Device is the resumed device.
func (r DeviceResumed) Device() Device { return Device{Major: r.Major, Minor: r.Minor} }

// ActiveChanged is the session's Active property changing.
type ActiveChanged struct{ Active bool }

// LockedHintChanged is the session's LockedHint property changing.
type LockedHintChanged struct{ Locked bool }

func (Sleep) event()             {}
func (Locked) event()            {}
func (Unlocked) event()          {}
func (DevicePaused) event()      {}
func (DeviceResumed) event()     {}
func (ActiveChanged) event()     {}
func (LockedHintChanged) event() {}

// The logind signals lys reads, each declared once: PrepareForSleep
// from the manager, the rest from a session.
var (
	PrepareForSleep = dbusx.NewSignal[Sleep](ManagerInterface, "PrepareForSleep")
	Lock            = dbusx.NewSignal[Locked](SessionInterface, "Lock")
	Unlock          = dbusx.NewSignal[Unlocked](SessionInterface, "Unlock")
	PauseDevice     = dbusx.NewSignal[DevicePaused](SessionInterface, "PauseDevice")
	ResumeDevice    = dbusx.NewSignal[DeviceResumed](SessionInterface, "ResumeDevice")
)

// PrepareForSleepMatch matches PrepareForSleep from logind's manager.
func PrepareForSleepMatch() []dbus.MatchOption {
	return []dbus.MatchOption{dbus.WithMatchSender(BusName), dbus.WithMatchObjectPath(ManagerPath), dbus.WithMatchInterface(ManagerInterface), dbus.WithMatchMember("PrepareForSleep")}
}

// routes are the signals a watch of the session reads, each decoded to
// the Event emit gets: the session's own, its Active and LockedHint
// changing, and the manager's PrepareForSleep.
func (s Session) routes(emit func(Event)) []dbusx.Route {
	path := s.obj.path
	return []dbusx.Route{
		dbusx.On(Lock, path, emitAs[Locked](emit)),
		dbusx.On(Unlock, path, emitAs[Unlocked](emit)),
		dbusx.On(PauseDevice, path, emitAs[DevicePaused](emit)),
		dbusx.On(ResumeDevice, path, emitAs[DeviceResumed](emit)),
		s.changes(emit),
		dbusx.On(PrepareForSleep, ManagerPath, emitAs[Sleep](emit)),
	}
}

// changes is the session's Active and LockedHint changing, each decoded
// to the Event emit gets.
func (s Session) changes(emit func(Event)) dbusx.Route {
	path := s.obj.path
	return dbusx.On(dbusx.PropertiesChanged, path, func(c dbusx.ChangedProperties, _ dbus.Sequence) error {
		return emitChanged(path, c.Changed, emit)
	}).WithArg0(SessionInterface)
}

func emitAs[T Event](emit func(Event)) func(T, dbus.Sequence) error {
	return func(v T, _ dbus.Sequence) error {
		emit(v)
		return nil
	}
}

func emitChanged(path dbus.ObjectPath, changed map[string]dbus.Variant, emit func(Event)) error {
	var errs []error
	if v, ok := changed["Active"]; ok {
		active, err := changedBool(path, "Active", v)
		if err == nil {
			emit(ActiveChanged{Active: active})
		}
		errs = append(errs, err)
	}
	if v, ok := changed["LockedHint"]; ok {
		locked, err := changedBool(path, "LockedHint", v)
		if err == nil {
			emit(LockedHintChanged{Locked: locked})
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func changedBool(path dbus.ObjectPath, name string, v dbus.Variant) (bool, error) {
	b, ok := v.Value().(bool)
	if !ok {
		return false, fmt.Errorf("%s of %s changed to a %s, want b", name, path, v.Signature())
	}
	return b, nil
}

// ErrDisconnected is what Watch.Stop reports when the watch ended
// because its connection closed.
var ErrDisconnected = dbusx.ErrDisconnected

// Watch follows one session's signals until its context ends, it is
// stopped, or its connection closes.
type Watch struct {
	signals *dbusx.Watch
	session dbus.ObjectPath
}

// Watch runs handle, on the watch's goroutine and in arrival order, for
// every signal logind sends about the session (Lock, Unlock,
// PauseDevice, ResumeDevice, its Active and LockedHint changing) and
// for the manager's PrepareForSleep. Exactly one of handle's arguments
// is non-nil: the decoded Event, or why a signal from logind could not
// be decoded. Only signals from whoever owns logind's name as they
// arrive count, so the watch outlives a restart of logind; a
// ResumeDevice from anyone else has its descriptor closed. A
// DeviceResumed's descriptor is this watch's own: every watch of the
// session, on this connection or another, is handed one of its own.
// The watch holds match rules and a signal channel on the session's
// connection until it ends, and then removes them, leaving a shared
// connection as it was.
func (s Session) Watch(ctx context.Context, handle func(Event, error)) (*Watch, error) {
	return s.watch(ctx, func(err error) { handle(nil, err) }, s.routes(func(e Event) { handle(e, nil) }))
}

func (s Session) watch(ctx context.Context, failed func(error), routes []dbusx.Route) (*Watch, error) {
	if s.obj.conn == nil {
		return nil, fmt.Errorf("logind: watch a session: %w", errZeroHandle)
	}
	signals, err := dbusx.WatchSignals(ctx, s.obj.conn, dbusx.FromOwner(BusName),
		func(err error) { failed(fmt.Errorf("logind: %w", err)) },
		routes...)
	if err != nil {
		return nil, fmt.Errorf("logind: watch session %s: %w", s.obj.path, err)
	}
	return &Watch{signals: signals, session: s.obj.path}, nil
}

// Done is closed once the watch has ended and removed what it held.
func (w *Watch) Done() <-chan struct{} { return w.signals.Done() }

// Stop ends the watch and returns once it has removed what it held:
// with ErrDisconnected when its connection had closed, with the error
// removing its match rules met, or nil. Calling it again waits for the
// same end and reports the same error. Stop must not be called from the
// watch's handler.
func (w *Watch) Stop() error {
	if err := w.signals.Stop(); err != nil {
		return fmt.Errorf("logind: session %s: %w", w.session, err)
	}
	return nil
}

// WaitLocked returns once the session's LockedHint is true, or with
// ctx's error when it ends first.
func (s Session) WaitLocked(ctx context.Context) (err error) {
	locked := make(chan struct{}, 1)
	w, err := s.watch(ctx, func(error) {}, []dbusx.Route{s.changes(func(e Event) {
		if c, ok := e.(LockedHintChanged); ok && c.Locked {
			select {
			case locked <- struct{}{}:
			default:
			}
		}
	})})
	if err != nil {
		return err
	}
	defer func() {
		if stopErr := w.Stop(); stopErr != nil && !errors.Is(err, ErrDisconnected) {
			err = errors.Join(err, stopErr)
		}
	}()
	if hint, err := s.LockedHint(ctx); err != nil || hint {
		return err
	}
	select {
	case <-locked:
		return nil
	case <-w.Done():
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("logind: wait for LockedHint of %s: %w", s.obj.path, err)
		}
		return fmt.Errorf("logind: wait for LockedHint of %s: %w", s.obj.path, ErrDisconnected)
	}
}
