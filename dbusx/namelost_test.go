package dbusx_test

import (
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const lostName = "dev.stubbe.lys.Lost"

func watchLosses(t *testing.T, conn *dbus.Conn, route dbusx.Route) {
	t.Helper()
	w, err := dbusx.WatchSignals(t.Context(), conn, dbusx.FromBus(),
		func(err error) { t.Errorf("the watch failed: %v", err) }, route)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Stop(); err != nil {
			t.Errorf("stop the watch: %v", err)
		}
	})
}

func ownThenRelease(t *testing.T, peer *dbus.Conn, name string) {
	t.Helper()
	if reply, err := peer.RequestName(name, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v %v", name, reply, err)
	}
	if _, err := peer.ReleaseName(name); err != nil {
		t.Fatalf("release %s: %v", name, err)
	}
}

func busIDReplySequence(t *testing.T, conn *dbus.Conn) dbus.Sequence {
	t.Helper()
	call := conn.BusObject().Call("org.freedesktop.DBus.GetId", 0)
	if call.Err != nil {
		t.Fatalf("GetId: %v", call.Err)
	}
	return call.ResponseSequence
}

func TestOnNameLostHearsANameReleasedAndAPeerLeavingOnALiveBus(t *testing.T) {
	bus := dbustest.Start(t)
	lost := make(chan string, 4)
	watchLosses(t, bus.Conn(t), dbusx.OnNameLost(func(name string, _ dbus.Sequence) error {
		lost <- name
		return nil
	}))
	peer := bus.Conn(t)
	unique := peer.Names()[0]
	ownThenRelease(t, peer, lostName)
	if got := awaitWatch(t, lost, "word of the released name"); got != lostName {
		t.Fatalf("lost %q first, want %q: connecting and taking the name are not losses", got, lostName)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if got := awaitWatch(t, lost, "word of the peer leaving"); got != unique {
		t.Fatalf("lost %q, want the unique name %q of the peer that left", got, unique)
	}
}

func TestANameLossIsSequencedAmongTheRepliesOfItsConnection(t *testing.T) {
	bus := dbustest.Start(t)
	conn := bus.Conn(t)
	lost := make(chan dbus.Sequence, 4)
	watchLosses(t, conn, dbusx.OnNameLost(func(_ string, announced dbus.Sequence) error {
		lost <- announced
		return nil
	}))
	peer := bus.Conn(t)
	before := busIDReplySequence(t, conn)
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	announced := awaitWatch(t, lost, "word of the peer leaving")
	after := busIDReplySequence(t, conn)
	if before >= announced || announced >= after {
		t.Errorf("the loss is sequenced %d, want it between the replies sequenced %d and %d", announced, before, after)
	}
}

func TestOnNameLostNarrowedToOneNameHearsOnlyIt(t *testing.T) {
	bus := dbustest.Start(t)
	lost := make(chan string, 4)
	watchLosses(t, bus.Conn(t), dbusx.OnNameLost(func(name string, _ dbus.Sequence) error {
		lost <- name
		return nil
	}).WithArg0(lostName))
	if err := bus.Conn(t).Close(); err != nil {
		t.Fatal(err)
	}
	ownThenRelease(t, bus.Conn(t), lostName)
	if got := awaitWatch(t, lost, "word of the released name"); got != lostName {
		t.Fatalf("lost %q, want only %q: another peer left before it", got, lostName)
	}
}
