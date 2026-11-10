// Package logind is systemd-logind (org.freedesktop.login1) on the
// system bus as every lys process uses it: its names declared once,
// typed manager, session, seat and user handles, the calls, properties
// and signals lys makes and reads, and one context-bounded call path
// whose errors name the method and the object.
package logind

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// The logind service, its manager object, and its interfaces.
const (
	BusName          = "org.freedesktop.login1"
	ManagerPath      = dbus.ObjectPath("/org/freedesktop/login1")
	ManagerInterface = BusName + ".Manager"
	SessionInterface = BusName + ".Session"
	SeatInterface    = BusName + ".Seat"
	UserInterface    = BusName + ".User"
)

// The logind errors lys tells apart.
const (
	ErrNoSuchSession       = BusName + ".NoSuchSession"
	ErrNoSessionForPID     = BusName + ".NoSessionForPID"
	ErrOperationInProgress = BusName + ".OperationInProgress"
)

// CallTimeout bounds every call to logind, whatever the caller's
// context allows: a logind that does not answer never hangs its caller.
const CallTimeout = 5 * time.Second

const propertiesInterface = "org.freedesktop.DBus.Properties"

// SessionID is a logind session id, as XDG_SESSION_ID and the Id
// property carry it.
type SessionID string

// The session names logind resolves for the caller: AutoSession is the
// caller's own session or, for a caller outside any session (a service
// of the systemd user manager), the user's display session; SelfSession
// is the caller's own session only.
const (
	AutoSession SessionID = "auto"
	SelfSession SessionID = "self"
)

// SeatID is a logind seat id, such as seat0.
type SeatID string

// SessionPath is the object path logind serves the session id at: the
// id escaped as systemd escapes an object path label.
func SessionPath(id SessionID) dbus.ObjectPath {
	return ManagerPath + "/session/" + dbus.ObjectPath(escapeLabel(string(id)))
}

func escapeLabel(s string) string {
	if s == "" {
		return "_"
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		c := s[i]
		alpha := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		digit := c >= '0' && c <= '9'
		if alpha || digit && i > 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('_')
		b.WriteString(strconv.FormatUint(uint64(c)>>4, 16))
		b.WriteString(strconv.FormatUint(uint64(c)&0xf, 16))
	}
	return b.String()
}

var errZeroHandle = errors.New("a zero handle, not one logind gave")

// object is one logind object on one connection: what every handle
// calls through.
type object struct {
	conn *dbus.Conn
	path dbus.ObjectPath
}

func (o object) call(ctx context.Context, iface, method string, out []any, args ...any) error {
	if o.conn == nil {
		return fmt.Errorf("logind %s: %w", describe(iface, method, args), errZeroHandle)
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	c := o.conn.Object(BusName, o.path).CallWithContext(ctx, iface+"."+method, 0, args...)
	if c.Err != nil {
		return fmt.Errorf("logind %s on %s: %w", describe(iface, method, args), o.path, c.Err)
	}
	if len(out) == 0 {
		return nil
	}
	if err := c.Store(out...); err != nil {
		return errors.Join(
			fmt.Errorf("logind %s on %s: decode the reply: %w", describe(iface, method, args), o.path, err),
			closeFDs(c.Body),
		)
	}
	return nil
}

func closeFDs(body []any) error {
	var errs []error
	for _, v := range body {
		if fd, ok := v.(dbus.UnixFD); ok && fd >= 0 {
			if err := unix.Close(int(fd)); err != nil {
				errs = append(errs, fmt.Errorf("close descriptor %d: %w", fd, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (o object) property(ctx context.Context, iface, name string, dest any) error {
	var v dbus.Variant
	if err := o.call(ctx, propertiesInterface, "Get", []any{&v}, iface, name); err != nil {
		return err
	}
	if err := v.Store(dest); err != nil {
		return fmt.Errorf("logind %s.%s of %s: decode %s: %w", shortInterface(iface), name, o.path, v.Signature(), err)
	}
	return nil
}

func describe(iface, method string, args []any) string {
	var b strings.Builder
	b.WriteString(shortInterface(iface))
	b.WriteByte('.')
	b.WriteString(method)
	b.WriteByte('(')
	for i, a := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		if s, ok := a.(string); ok {
			b.WriteString(strconv.Quote(s))
			continue
		}
		fmt.Fprint(&b, a)
	}
	b.WriteByte(')')
	return b.String()
}

func shortInterface(iface string) string {
	return strings.TrimPrefix(iface, BusName+".")
}

// Manager is logind's manager object on one connection.
type Manager struct{ obj object }

// NewManager is logind's manager on conn, the system bus.
func NewManager(conn *dbus.Conn) Manager {
	return Manager{obj: object{conn: conn, path: ManagerPath}}
}

func (m Manager) session(path dbus.ObjectPath) Session {
	return Session{obj: object{conn: m.obj.conn, path: path}}
}

// GetSession resolves id, or AutoSession or SelfSession, to its session.
// The handle is the session's real path: logind sends its signals from
// that one, never from the auto or self alias.
func (m Manager) GetSession(ctx context.Context, id SessionID) (Session, error) {
	var path dbus.ObjectPath
	if err := m.obj.call(ctx, ManagerInterface, "GetSession", []any{&path}, string(id)); err != nil {
		return Session{}, err
	}
	return m.session(path), nil
}

// GetSessionByPID is the session process pid belongs to; a process in
// none is logind's ErrNoSessionForPID.
func (m Manager) GetSessionByPID(ctx context.Context, pid uint32) (Session, error) {
	var path dbus.ObjectPath
	if err := m.obj.call(ctx, ManagerInterface, "GetSessionByPID", []any{&path}, pid); err != nil {
		return Session{}, err
	}
	return m.session(path), nil
}

// SelfSession is logind's alias object for the caller's own session,
// for calls that need no signals: logind sends those from the real path
// (GetSession).
func (m Manager) SelfSession() Session {
	return m.session(SessionPath(SelfSession))
}

// SelfUser is logind's alias object for the calling user.
func (m Manager) SelfUser() User {
	return User{obj: object{conn: m.obj.conn, path: ManagerPath + "/user/self"}}
}

// LockSession asks logind to lock session id: logind sends the
// session's Lock signal, which its lock screen answers.
func (m Manager) LockSession(ctx context.Context, id SessionID) error {
	return m.obj.call(ctx, ManagerInterface, "LockSession", nil, string(id))
}

// ActivateSession brings session id to the foreground of its seat,
// switching VT when it is on another.
func (m Manager) ActivateSession(ctx context.Context, id SessionID) error {
	return m.obj.call(ctx, ManagerInterface, "ActivateSession", nil, string(id))
}

// Power asks logind to suspend, reboot or power off the system, never
// interactively: polkit decides without a prompt, as it does for the
// active local session.
func (m Manager) Power(ctx context.Context, action PowerAction) error {
	method, err := action.method()
	if err != nil {
		return err
	}
	return m.obj.call(ctx, ManagerInterface, method, nil, false)
}

// Dial connects to the system bus, where logind is, giving up when ctx
// ends first; once made, the connection outlives ctx and is the
// caller's to close.
func Dial(ctx context.Context) (*dbus.Conn, error) {
	life, closeConn := context.WithCancel(context.Background())
	giveUp := context.AfterFunc(ctx, closeConn)
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(life))
	if !giveUp() {
		closeConn()
		if err == nil {
			err = ctx.Err()
		}
	}
	if err != nil {
		closeConn()
		return nil, fmt.Errorf("logind: connect to the system bus: %w", err)
	}
	return conn, nil
}
