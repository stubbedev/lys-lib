package dbusx_test

import (
	"context"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// A watched name that gains an owner after the watch began, and again
// after a restart, runs appeared each time; another name, and the
// owner leaving, run nothing.
func TestWatchOwnerSeesALateService(t *testing.T) {
	bus := dbustest.Start(t)
	client := bus.Conn(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	appeared := make(chan struct{}, 8)
	if err := dbusx.WatchOwner(ctx, client, "dev.stubbe.lys.Late", func() { appeared <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	expect := func(want bool, what string) {
		t.Helper()
		select {
		case <-appeared:
			if !want {
				t.Errorf("%s ran appeared", what)
			}
		case <-time.After(300 * time.Millisecond):
			if want {
				t.Errorf("%s did not run appeared", what)
			}
		}
	}
	own := func(name string) *dbus.Conn {
		t.Helper()
		c := bus.Conn(t)
		if reply, err := c.RequestName(name, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
			t.Fatalf("own %s: %v %v", name, reply, err)
		}
		return c
	}
	other := own("dev.stubbe.lys.Other")
	expect(false, "another name")
	_ = other.Close()

	service := own("dev.stubbe.lys.Late")
	expect(true, "the service starting")
	_ = service.Close()
	expect(false, "the service stopping")
	restarted := own("dev.stubbe.lys.Late")
	expect(true, "the service restarting")
	_ = restarted.Close()
	expect(false, "the service stopping again")

	cancel()
	time.Sleep(50 * time.Millisecond)
	own("dev.stubbe.lys.Late")
	expect(false, "the service starting after the watch ended")
}

func TestNameOwnerIsTheUniqueName(t *testing.T) {
	bus := dbustest.Start(t)
	client, service := bus.Conn(t), bus.Conn(t)
	if owner, err := dbusx.NameOwner(context.Background(), client, "dev.stubbe.lys.Late"); err == nil {
		t.Fatalf("NameOwner of an unowned name = %q", owner)
	}
	if _, err := dbusx.Own(service, "dev.stubbe.lys.Late"); err != nil {
		t.Fatal(err)
	}
	if owner, err := dbusx.NameOwner(context.Background(), client, "dev.stubbe.lys.Late"); err != nil || owner != service.Names()[0] {
		t.Fatalf("NameOwner = %q, %v; want %s", owner, err, service.Names()[0])
	}
}

// Only the bus announces owners: the same signal from a peer is not a
// NameOwnerChanged.
func TestNameOwnerChangeComesFromTheBus(t *testing.T) {
	sig := func(sender string) *dbus.Signal {
		return &dbus.Signal{Sender: sender, Name: "org.freedesktop.DBus.NameOwnerChanged", Body: []any{"dev.stubbe.lys.Late", ":1.1", ":1.2"}}
	}
	if old, owner, ok := dbusx.NameOwnerChange(sig("org.freedesktop.DBus"), "dev.stubbe.lys.Late"); !ok || old != ":1.1" || owner != ":1.2" {
		t.Errorf("from the bus = %q, %q, %v", old, owner, ok)
	}
	if _, _, ok := dbusx.NameOwnerChange(sig(":1.3"), "dev.stubbe.lys.Late"); ok {
		t.Error("a peer's NameOwnerChanged was read as the bus's")
	}
	if _, _, ok := dbusx.NameOwnerChange(sig("org.freedesktop.DBus"), "dev.stubbe.lys.Other"); ok {
		t.Error("a change of another name was read as this one")
	}
}
