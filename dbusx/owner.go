package dbusx

import (
	"context"
	"fmt"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// bus is the message bus's own name and interface, which announces a
// name gaining or losing its owner.
const bus = "org.freedesktop.DBus"

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
	return []dbus.MatchOption{dbus.WithMatchSender(bus), dbus.WithMatchInterface(bus), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, name)}
}

// NameOwnerChange reads a NameOwnerChanged signal the bus sent about
// name: the old and new owners ("" for none), and whether the signal is
// that one. Another peer sending NameOwnerChanged is not the bus, so
// its signal is not that one.
func NameOwnerChange(sig *dbus.Signal, name string) (oldOwner, newOwner string, ok bool) {
	if sig.Sender != bus || sig.Name != bus+".NameOwnerChanged" || len(sig.Body) != 3 {
		return "", "", false
	}
	if n, _ := sig.Body[0].(string); n != name {
		return "", "", false
	}
	oldOwner, _ = sig.Body[1].(string)
	newOwner, _ = sig.Body[2].(string)
	return oldOwner, newOwner, true
}

// WatchOwner runs appeared, on a goroutine of its own, each time name
// gains an owner on conn, until ctx ends: a service that starts (or
// restarts) after its client did.
func WatchOwner(ctx context.Context, conn *dbus.Conn, name string, appeared func()) error {
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	if err := conn.AddMatchSignalContext(ctx, OwnerMatch(name)...); err != nil {
		conn.RemoveSignal(signals)
		return fmt.Errorf("watch %s: %w", name, err)
	}
	go func() {
		defer conn.RemoveSignal(signals)
		for {
			select {
			case <-ctx.Done():
				return
			case sig, ok := <-signals:
				if !ok {
					return
				}
				if _, owner, isName := NameOwnerChange(sig, name); isName && owner != "" {
					appeared()
				}
			}
		}
	}()
	return nil
}
