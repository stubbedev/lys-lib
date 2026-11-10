package dbusx

import (
	"context"
	"fmt"
	"time"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// DialSystem connects to the system bus, giving up when ctx ends or
// timeout has passed, whichever comes first: a bus that does not answer
// never hangs its caller. Once made, the connection outlives both and
// is the caller's to close.
func DialSystem(ctx context.Context, timeout time.Duration) (*dbus.Conn, error) {
	return dial(ctx, timeout, "system", dbus.ConnectSystemBus)
}

// DialSession connects to the session bus as DialSystem does to the
// system bus, bounded the same way.
func DialSession(ctx context.Context, timeout time.Duration) (*dbus.Conn, error) {
	return dial(ctx, timeout, "session", dbus.ConnectSessionBus)
}

func dial(ctx context.Context, timeout time.Duration, bus string, connect func(...dbus.ConnOption) (*dbus.Conn, error)) (*dbus.Conn, error) {
	dialing, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	life, closeConn := context.WithCancel(context.Background())
	giveUp := context.AfterFunc(dialing, closeConn)
	conn, err := connect(dbus.WithContext(life))
	if !giveUp() {
		gaveUp := dialing.Err()
		if err != nil {
			gaveUp = fmt.Errorf("%w: %w", gaveUp, err)
		}
		err = gaveUp
	}
	if err != nil {
		closeConn()
		return nil, fmt.Errorf("connect to the %s bus: %w", bus, err)
	}
	return conn, nil
}
