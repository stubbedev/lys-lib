package dbusx

import (
	"context"
	"fmt"
	"time"

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

// unwatchTimeout bounds removing a watch's match rule, which runs once
// the watch's context has ended.
const unwatchTimeout = 5 * time.Second

// WatchOwner runs appeared, on the watch's goroutine, each time name
// gains an owner on conn: a service that starts (or restarts) after its
// client did. The watch holds a match rule and a signal channel on conn
// until ctx ends or stop is called, whichever comes first, and then
// removes both, so a connection shared with others is left as it was.
// stop returns once they are gone, with the error removing the rule
// met; calling it again, or after ctx ended, waits for the same removal
// and reports the same error. stop must not be called from appeared.
func WatchOwner(ctx context.Context, conn *dbus.Conn, name string, appeared func()) (stop func() error, err error) {
	match := OwnerMatch(name)
	if err := conn.AddMatchSignalContext(ctx, match...); err != nil {
		return nil, fmt.Errorf("watch %s: %w", name, err)
	}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	watch, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var unwatchErr error
	go func() {
		defer close(done)
		defer func() {
			conn.RemoveSignal(signals)
			unwatch, cancelUnwatch := context.WithTimeout(context.WithoutCancel(ctx), unwatchTimeout)
			defer cancelUnwatch()
			if err := conn.RemoveMatchSignalContext(unwatch, match...); err != nil {
				unwatchErr = fmt.Errorf("stop watching %s: %w", name, err)
			}
		}()
		for {
			select {
			case <-watch.Done():
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
	return func() error {
		cancel()
		<-done
		return unwatchErr
	}, nil
}
