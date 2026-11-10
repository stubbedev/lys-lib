package logind

import (
	"context"
	"errors"
	"fmt"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// Session is one logind session on one connection.
type Session struct{ obj object }

// Path is the session's object path.
func (s Session) Path() dbus.ObjectPath { return s.obj.path }

// ID reads the session's Id.
func (s Session) ID(ctx context.Context) (SessionID, error) {
	var id string
	if err := s.obj.property(ctx, SessionInterface, "Id", &id); err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("logind session %s has an empty Id", s.obj.path)
	}
	return SessionID(id), nil
}

// Active reads whether the session is in the foreground of its seat.
func (s Session) Active(ctx context.Context) (bool, error) {
	var active bool
	err := s.obj.property(ctx, SessionInterface, "Active", &active)
	return active, err
}

// LockedHint reads whether the session's lock screen says it is locked.
func (s Session) LockedHint(ctx context.Context) (bool, error) {
	var locked bool
	err := s.obj.property(ctx, SessionInterface, "LockedHint", &locked)
	return locked, err
}

// Seat reads the seat the session is on; a session on none is an error.
func (s Session) Seat(ctx context.Context) (Seat, error) {
	var ref struct {
		ID   string
		Path dbus.ObjectPath
	}
	if err := s.obj.property(ctx, SessionInterface, "Seat", &ref); err != nil {
		return Seat{}, err
	}
	if ref.ID == "" || ref.Path == noObject {
		return Seat{}, fmt.Errorf("logind session %s is on no seat", s.obj.path)
	}
	return Seat{obj: object{conn: s.obj.conn, path: ref.Path}, id: SeatID(ref.ID)}, nil
}

// SetLockedHint tells logind whether the session is locked.
func (s Session) SetLockedHint(ctx context.Context, locked bool) error {
	return s.obj.call(ctx, SessionInterface, "SetLockedHint", nil, locked)
}

// Lock asks logind to lock the session: logind sends the session's Lock
// signal, which its lock screen answers.
func (s Session) Lock(ctx context.Context) error {
	return s.obj.call(ctx, SessionInterface, "Lock", nil)
}

// Terminate ends the session and every process in it.
func (s Session) Terminate(ctx context.Context) error {
	return s.obj.call(ctx, SessionInterface, "Terminate", nil)
}

// Subsystem is the kind of device Session.SetBrightness drives.
type Subsystem uint8

// The brightness subsystems.
const (
	Backlight Subsystem = iota + 1
	LEDs
)

// String is the subsystem as the kernel and logind name it.
func (s Subsystem) String() string {
	switch s {
	case Backlight:
		return "backlight"
	case LEDs:
		return "leds"
	}
	return fmt.Sprintf("Subsystem(%d)", uint8(s))
}

// SetBrightness sets the raw brightness of the subsystem's device name,
// which the session may do without write access to sysfs.
func (s Session) SetBrightness(ctx context.Context, subsystem Subsystem, name string, value uint32) error {
	if subsystem != Backlight && subsystem != LEDs {
		return fmt.Errorf("logind: set the brightness of %s: no subsystem %s", name, subsystem)
	}
	return s.obj.call(ctx, SessionInterface, "SetBrightness", nil, subsystem.String(), name, value)
}

// TakeControl makes the caller the session's controller, the one
// process that takes its devices; force takes it from another
// controller, which only root may.
func (s Session) TakeControl(ctx context.Context, force bool) error {
	return s.obj.call(ctx, SessionInterface, "TakeControl", nil, force)
}

// ReleaseControl gives up control of the session, releasing every
// device the controller took.
func (s Session) ReleaseControl(ctx context.Context) error {
	return s.obj.call(ctx, SessionInterface, "ReleaseControl", nil)
}

// Device is a character device by number, as logind's device calls and
// signals name it.
type Device struct{ Major, Minor uint32 }

// TakeDevice opens d for the session's controller. The descriptor is
// the caller's to close; inactive says the session is in the background,
// where the device stays paused until ResumeDevice.
func (s Session) TakeDevice(ctx context.Context, d Device) (fd int, inactive bool, err error) {
	var ufd dbus.UnixFD
	if err := s.obj.call(ctx, SessionInterface, "TakeDevice", []any{&ufd, &inactive}, d.Major, d.Minor); err != nil {
		return -1, false, err
	}
	if ufd < 0 {
		return -1, false, fmt.Errorf("logind Session.TakeDevice(%d, %d) on %s: invalid file descriptor %d", d.Major, d.Minor, s.obj.path, ufd)
	}
	return int(ufd), inactive, nil
}

// ReleaseDevice gives d back.
func (s Session) ReleaseDevice(ctx context.Context, d Device) error {
	return s.obj.call(ctx, SessionInterface, "ReleaseDevice", nil, d.Major, d.Minor)
}

// PauseDeviceComplete acknowledges a PauseDevice of kind PausePause:
// logind waits for it before switching the session away.
func (s Session) PauseDeviceComplete(ctx context.Context, d Device) error {
	return s.obj.call(ctx, SessionInterface, "PauseDeviceComplete", nil, d.Major, d.Minor)
}

// noObject is the path logind's (so) and (uo) properties carry for none.
const noObject = dbus.ObjectPath("/")

// Seat is one logind seat on one connection.
type Seat struct {
	obj object
	id  SeatID
}

// ID is the seat's id.
func (s Seat) ID() SeatID { return s.id }

// Path is the seat's object path.
func (s Seat) Path() dbus.ObjectPath { return s.obj.path }

// SwitchTo switches the seat to virtual terminal vt.
func (s Seat) SwitchTo(ctx context.Context, vt uint32) error {
	return s.obj.call(ctx, SeatInterface, "SwitchTo", nil, vt)
}

// ErrNoDisplay is User.Display for a user with no display session.
var ErrNoDisplay = errors.New("no display session")

// User is one logind user on one connection.
type User struct{ obj object }

// Path is the user's object path.
func (u User) Path() dbus.ObjectPath { return u.obj.path }

// Display reads the user's display session, the one a caller outside
// any session belongs to; a user with none is ErrNoDisplay.
func (u User) Display(ctx context.Context) (Session, error) {
	var ref struct {
		ID   string
		Path dbus.ObjectPath
	}
	if err := u.obj.property(ctx, UserInterface, "Display", &ref); err != nil {
		return Session{}, err
	}
	if ref.Path == noObject || ref.Path == "" {
		return Session{}, fmt.Errorf("logind user %s: %w", u.obj.path, ErrNoDisplay)
	}
	return Session{obj: object{conn: u.obj.conn, path: ref.Path}}, nil
}
