// Package polkit is polkitd's Authority (org.freedesktop.PolicyKit1) on
// the system bus as every lys process uses it: its names declared once,
// typed subjects, the authorization check with its cancellation, the
// calls an authentication agent makes, and one context-bounded call
// path whose errors name the action and the subject.
package polkit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// The polkit service, its Authority object, and the interfaces of the
// Authority and of the agent that authenticates for it.
const (
	BusName            = "org.freedesktop.PolicyKit1"
	AuthorityPath      = dbus.ObjectPath("/org/freedesktop/PolicyKit1/Authority")
	AuthorityInterface = BusName + ".Authority"
	AgentInterface     = BusName + ".AuthenticationAgent"
)

// The polkit errors lys tells apart (polkiterror.c): what the Authority
// answers a refused call and a cancelled check with, and what an agent
// answers a failed and a cancelled authentication with.
const (
	ErrFailed    = BusName + ".Error.Failed"
	ErrCancelled = BusName + ".Error.Cancelled"
)

// CallTimeout bounds every call to polkitd but CheckAuthorization,
// whatever the caller's context allows: a polkitd that does not answer
// never hangs its caller. A check may wait for a person to authenticate
// and is bounded by its context alone.
const CallTimeout = 5 * time.Second

// Changed is the Authority's signal that actions, authorizations or
// agents changed.
var Changed = dbusx.NewSignal[AuthorityChanged](AuthorityInterface, "Changed")

// AuthorityChanged is what Changed carries: nothing.
type AuthorityChanged struct{}

// ChangedMatch matches Changed from polkitd's Authority.
func ChangedMatch() []dbus.MatchOption {
	return []dbus.MatchOption{dbus.WithMatchSender(BusName), dbus.WithMatchObjectPath(AuthorityPath), dbus.WithMatchInterface(AuthorityInterface), dbus.WithMatchMember("Changed")}
}

// Dial connects to the system bus, where polkitd is, giving up when ctx
// ends or after CallTimeout, whichever comes first; once made, the
// connection outlives ctx and is the caller's to close.
func Dial(ctx context.Context) (*dbus.Conn, error) {
	conn, err := dbusx.DialSystem(ctx, CallTimeout)
	if err != nil {
		return nil, fmt.Errorf("polkit: %w", err)
	}
	return conn, nil
}

var errZeroHandle = errors.New("a zero Authority, not one NewAuthority gave")

// Authority is polkitd's Authority object on one connection.
type Authority struct{ conn *dbus.Conn }

// NewAuthority is polkitd's Authority on conn, the system bus.
func NewAuthority(conn *dbus.Conn) Authority { return Authority{conn: conn} }

// call makes one call of the Authority and stores its reply in out. It
// returns the sequence of polkitd's reply.
func (a Authority) call(ctx context.Context, method string, out []any, args ...any) (dbus.Sequence, error) {
	if a.conn == nil {
		return dbus.NoSequence, errZeroHandle
	}
	c := a.conn.Object(BusName, AuthorityPath).CallWithContext(ctx, AuthorityInterface+"."+method, 0, args...)
	if c.Err != nil {
		return dbus.NoSequence, c.Err
	}
	if len(out) == 0 {
		return c.ResponseSequence, nil
	}
	if err := c.Store(out...); err != nil {
		return dbus.NoSequence, fmt.Errorf("decode the reply: %w", err)
	}
	return c.ResponseSequence, nil
}

// bounded is call within CallTimeout.
func (a Authority) bounded(ctx context.Context, method string, out []any, args ...any) (dbus.Sequence, error) {
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	return a.call(ctx, method, out, args...)
}

// about is bounded for a method whose first argument is a subject.
func (a Authority) about(ctx context.Context, subject Subject, method string, out []any, rest ...any) (dbus.Sequence, error) {
	wire, err := subject.Wire()
	if err != nil {
		return dbus.NoSequence, err
	}
	return a.bounded(ctx, method, out, append([]any{wire}, rest...)...)
}

// RegisterAuthenticationAgent registers the caller's agent, exported at
// path with AgentInterface, as the one that authenticates for subject,
// its messages in locale. It returns the sequence of polkitd's reply:
// a NameOwnerChanged of BusName sequenced before it happened before the
// registration, one after it is a polkitd that has not seen the agent.
func (a Authority) RegisterAuthenticationAgent(ctx context.Context, subject Subject, locale string, path dbus.ObjectPath) (dbus.Sequence, error) {
	registered, err := a.about(ctx, subject, "RegisterAuthenticationAgent", nil, locale, string(path))
	if err != nil {
		return dbus.NoSequence, fmt.Errorf("polkit: register the agent at %s for %s: %w", path, subject, err)
	}
	return registered, nil
}

// UnregisterAuthenticationAgent takes back the registration of the
// caller's agent at path for subject.
func (a Authority) UnregisterAuthenticationAgent(ctx context.Context, subject Subject, path dbus.ObjectPath) error {
	if _, err := a.about(ctx, subject, "UnregisterAuthenticationAgent", nil, string(path)); err != nil {
		return fmt.Errorf("polkit: unregister the agent at %s for %s: %w", path, subject, err)
	}
	return nil
}

// ActionDescription is one registered action as EnumerateActions
// describes it, (ssssssuuua{ss}): its texts in the locale asked for,
// and the implicit authorizations of any, inactive and active sessions.
type ActionDescription struct {
	ActionID         string
	Description      string
	Message          string
	VendorName       string
	VendorURL        string
	IconName         string
	ImplicitAny      uint32
	ImplicitInactive uint32
	ImplicitActive   uint32
	Annotations      map[string]string
}

// EnumerateActions describes every action registered with polkitd, its
// texts translated to locale.
func (a Authority) EnumerateActions(ctx context.Context, locale string) ([]ActionDescription, error) {
	var all []ActionDescription
	if _, err := a.bounded(ctx, "EnumerateActions", []any{&all}, locale); err != nil {
		return nil, fmt.Errorf("polkit: enumerate the actions for locale %s: %w", locale, err)
	}
	return all, nil
}

// TemporaryAuthorization is one authorization polkitd retains for a
// while after an authentication, (ss(sa{sv})tt). Obtained is on the
// monotonic clock and Expires on the wall clock, both in seconds.
type TemporaryAuthorization struct {
	ID       string
	ActionID string
	Subject  WireSubject
	Obtained uint64
	Expires  uint64
}

// EnumerateTemporaryAuthorizations lists the authorizations polkitd
// retains for subject.
func (a Authority) EnumerateTemporaryAuthorizations(ctx context.Context, subject Subject) ([]TemporaryAuthorization, error) {
	var all []TemporaryAuthorization
	if _, err := a.about(ctx, subject, "EnumerateTemporaryAuthorizations", []any{&all}); err != nil {
		return nil, fmt.Errorf("polkit: enumerate the temporary authorizations of %s: %w", subject, err)
	}
	return all, nil
}

// RevokeTemporaryAuthorizations revokes every authorization polkitd
// retains for subject.
func (a Authority) RevokeTemporaryAuthorizations(ctx context.Context, subject Subject) error {
	if _, err := a.about(ctx, subject, "RevokeTemporaryAuthorizations", nil); err != nil {
		return fmt.Errorf("polkit: revoke the temporary authorizations of %s: %w", subject, err)
	}
	return nil
}

// RevokeTemporaryAuthorizationByID revokes the one retained
// authorization id.
func (a Authority) RevokeTemporaryAuthorizationByID(ctx context.Context, id string) error {
	if _, err := a.bounded(ctx, "RevokeTemporaryAuthorizationById", nil, id); err != nil {
		return fmt.Errorf("polkit: revoke temporary authorization %s: %w", id, err)
	}
	return nil
}
