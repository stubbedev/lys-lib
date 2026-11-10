package dbusx

import (
	"context"
	"errors"
	"fmt"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// bus is the message bus's own name and interface, and busPath the
// object it announces a name gaining or losing its owner from.
const (
	bus     = "org.freedesktop.DBus"
	busPath = dbus.ObjectPath("/org/freedesktop/DBus")
)

// ownerChange is what NameOwnerChanged carries: the name, and its old
// and new owners, "" for none.
type ownerChange struct {
	Name     string `dbus:"name"`
	OldOwner string `dbus:"old_owner"`
	NewOwner string `dbus:"new_owner"`
}

var nameOwnerChanged = NewSignal[ownerChange](bus, "NameOwnerChanged")

func ownerChanges(name string, handle func(ownerChange) error) Route {
	return On(nameOwnerChanged, busPath, handle).WithArg0(name)
}

// NameOwner returns the unique name of name's current owner; a name
// nobody owns is an error.
func NameOwner(ctx context.Context, conn *dbus.Conn, name string) (string, error) {
	var owner string
	if err := conn.BusObject().CallWithContext(ctx, bus+".GetNameOwner", 0, name).Store(&owner); err != nil {
		return "", fmt.Errorf("owner of %s: %w", name, err)
	}
	return owner, nil
}

// OwnerMatch matches the NameOwnerChanged signals the bus sends about
// name.
func OwnerMatch(name string) []dbus.MatchOption {
	r := ownerChanges(name, nil)
	return r.match(bus, false)
}

// NameOwnerChange reads a NameOwnerChanged signal the bus sent about
// name: the old and new owners ("" for none), and whether the signal is
// that one. Another peer sending NameOwnerChanged is not the bus, so
// its signal is not that one.
func NameOwnerChange(sig *dbus.Signal, name string) (oldOwner, newOwner string, ok bool) {
	if sig.Sender != bus || sig.Name != nameOwnerChanged.Name() || len(sig.Body) == 0 || sig.Body[0] != name {
		return "", "", false
	}
	change, err := nameOwnerChanged.Decode(sig)
	if err != nil {
		return "", "", false
	}
	return change.OldOwner, change.NewOwner, true
}

// WatchOwner runs appeared, on the watch's goroutine, each time name
// gains an owner on conn: a service that starts (or restarts) after its
// client did. The watch holds a match rule and a signal channel on conn
// until ctx ends or stop is called, whichever comes first, and then
// removes both, so a connection shared with others is left as it was.
// stop returns once they are gone, with the error removing the rule
// met (none for a closed connection, whose rules the bus dropped);
// calling it again, or after ctx ended, waits for the same removal and
// reports the same error. stop must not be called from appeared.
func WatchOwner(ctx context.Context, conn *dbus.Conn, name string, appeared func()) (stop func() error, err error) {
	var undecoded []error
	w, err := WatchSignals(ctx, conn, FromBus(),
		func(err error) { undecoded = append(undecoded, err) },
		ownerChanges(name, func(change ownerChange) error {
			if change.NewOwner != "" {
				appeared()
			}
			return nil
		}))
	if err != nil {
		return nil, fmt.Errorf("watch the owner of %s: %w", name, err)
	}
	return func() error {
		err := w.Stop()
		if errors.Is(err, ErrDisconnected) {
			err = nil
		}
		if err := errors.Join(append(undecoded, err)...); err != nil {
			return fmt.Errorf("watch the owner of %s: %w", name, err)
		}
		return nil
	}, nil
}
