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

func (s Session) matches() [][]dbus.MatchOption {
	return [][]dbus.MatchOption{
		{dbus.WithMatchSender(BusName), dbus.WithMatchObjectPath(s.obj.path), dbus.WithMatchInterface(SessionInterface)},
		{dbus.WithMatchSender(BusName), dbus.WithMatchObjectPath(s.obj.path), dbus.WithMatchInterface(propertiesInterface), dbus.WithMatchMember("PropertiesChanged"), dbus.WithMatchArg(0, SessionInterface)},
		PrepareForSleepMatch(),
	}
}

// decoder reads the signals of one session from logind's current owner.
type decoder struct {
	owner   string
	session dbus.ObjectPath
}

func (d decoder) decode(sig *dbus.Signal, emit func(Event)) error {
	if sig.Path == ManagerPath && sig.Name == PrepareForSleep.Name() {
		if sig.Sender != d.owner {
			return nil
		}
		return decodeAs(PrepareForSleep, sig, emit)
	}
	if sig.Path != d.session {
		return nil
	}
	if sig.Sender != d.owner {
		if sig.Name == ResumeDevice.Name() {
			return closeFDs(sig.Body)
		}
		return nil
	}
	switch sig.Name {
	case Lock.Name():
		return decodeAs(Lock, sig, emit)
	case Unlock.Name():
		return decodeAs(Unlock, sig, emit)
	case PauseDevice.Name():
		return decodeAs(PauseDevice, sig, emit)
	case ResumeDevice.Name():
		if err := decodeAs(ResumeDevice, sig, emit); err != nil {
			return errors.Join(err, closeFDs(sig.Body))
		}
		return nil
	case propertiesInterface + ".PropertiesChanged":
		return decodeProperties(sig, emit)
	}
	return nil
}

func decodeAs[T Event](s dbusx.Signal[T], sig *dbus.Signal, emit func(Event)) error {
	v, err := s.Decode(sig)
	if err != nil {
		return fmt.Errorf("logind: %w", err)
	}
	emit(v)
	return nil
}

func decodeProperties(sig *dbus.Signal, emit func(Event)) error {
	var (
		iface       string
		changed     map[string]dbus.Variant
		invalidated []string
	)
	if err := dbus.Store(sig.Body, &iface, &changed, &invalidated); err != nil {
		return fmt.Errorf("logind: decode PropertiesChanged of %s: %w", sig.Path, err)
	}
	if iface != SessionInterface {
		return nil
	}
	var errs []error
	if v, ok := changed["Active"]; ok {
		active, err := changedBool(sig.Path, "Active", v)
		if err == nil {
			emit(ActiveChanged{Active: active})
		}
		errs = append(errs, err)
	}
	if v, ok := changed["LockedHint"]; ok {
		locked, err := changedBool(sig.Path, "LockedHint", v)
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
		return false, fmt.Errorf("logind: %s of %s changed to a %s, want b", name, path, v.Signature())
	}
	return b, nil
}

// ErrDisconnected is what Watch.Stop reports when the watch ended
// because its connection closed.
var ErrDisconnected = errors.New("the bus connection closed")

const signalBuffer = 32

// Watch follows one session's signals until its context ends, it is
// stopped, or its connection closes.
type Watch struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// Watch runs handle, on the watch's goroutine and in arrival order, for
// every signal logind sends about the session (Lock, Unlock,
// PauseDevice, ResumeDevice, its Active and LockedHint changing) and
// for the manager's PrepareForSleep. Exactly one of handle's arguments
// is non-nil: the decoded Event, or why a signal from logind could not
// be decoded. Only signals from logind's owner at the time of the call
// count; a ResumeDevice from anyone else has its descriptor closed. The
// watch holds match rules and a signal channel on the session's
// connection until it ends, and then removes them, leaving a shared
// connection as it was.
func (s Session) Watch(ctx context.Context, handle func(Event, error)) (*Watch, error) {
	conn := s.obj.conn
	if conn == nil {
		return nil, fmt.Errorf("logind: watch a session: %w", errZeroHandle)
	}
	setup, cancelSetup := context.WithTimeout(ctx, CallTimeout)
	defer cancelSetup()
	owner, err := dbusx.NameOwner(setup, conn, BusName)
	if err != nil {
		return nil, fmt.Errorf("logind: watch session %s: %w", s.obj.path, err)
	}
	matches := s.matches()
	for i, match := range matches {
		if err := conn.AddMatchSignalContext(setup, match...); err != nil {
			return nil, errors.Join(fmt.Errorf("logind: watch session %s: %w", s.obj.path, err), removeMatches(ctx, conn, matches[:i]))
		}
	}
	signals := make(chan *dbus.Signal, signalBuffer)
	conn.Signal(signals)
	watch, cancel := context.WithCancel(ctx)
	w := &Watch{cancel: cancel, done: make(chan struct{})}
	d := decoder{owner: owner, session: s.obj.path}
	emit := func(e Event) { handle(e, nil) }
	go func() {
		defer close(w.done)
		defer func() {
			conn.RemoveSignal(signals)
			if err := removeMatches(ctx, conn, matches); err != nil {
				w.err = errors.Join(w.err, fmt.Errorf("logind: stop watching session %s: %w", s.obj.path, err))
			}
		}()
		for {
			select {
			case <-watch.Done():
				return
			case sig, ok := <-signals:
				if !ok {
					w.err = fmt.Errorf("logind: watch session %s: %w", s.obj.path, ErrDisconnected)
					return
				}
				if err := d.decode(sig, emit); err != nil {
					handle(nil, err)
				}
			}
		}
	}()
	return w, nil
}

func removeMatches(ctx context.Context, conn *dbus.Conn, matches [][]dbus.MatchOption) error {
	removing, cancel := context.WithTimeout(context.WithoutCancel(ctx), CallTimeout)
	defer cancel()
	var errs []error
	for _, match := range matches {
		if err := conn.RemoveMatchSignalContext(removing, match...); err != nil && !errors.Is(err, dbus.ErrClosed) && conn.Connected() {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Done is closed once the watch has ended and removed what it held.
func (w *Watch) Done() <-chan struct{} { return w.done }

// Stop ends the watch and returns once it has removed what it held:
// with ErrDisconnected when its connection had closed, with the error
// removing its match rules met, or nil. Calling it again waits for the
// same end and reports the same error. Stop must not be called from the
// watch's handler.
func (w *Watch) Stop() error {
	w.cancel()
	<-w.done
	return w.err
}

// WaitLocked returns once the session's LockedHint is true, or with
// ctx's error when it ends first.
func (s Session) WaitLocked(ctx context.Context) (err error) {
	locked := make(chan struct{}, 1)
	w, err := s.Watch(ctx, func(e Event, _ error) {
		if c, ok := e.(LockedHintChanged); ok && c.Locked {
			select {
			case locked <- struct{}{}:
			default:
			}
		}
	})
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
