package dbusx_test

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const managedPath = dbus.ObjectPath("/dev/stubbe/lys/Watched/Managed")

func TestTheObjectManagerSignalsDecode(t *testing.T) {
	added := dbusx.AddedInterfaces{Object: managedPath, Interfaces: map[string]map[string]dbus.Variant{
		watchedIface: {"Count": dbus.MakeVariant(uint32(3))},
		otherIface:   {},
	}}
	got, err := dbusx.InterfacesAdded.Decode(&dbus.Signal{
		Name: dbusx.ObjectManagerInterface + ".InterfacesAdded",
		Body: []any{added.Object, added.Interfaces},
	})
	if err != nil || !reflect.DeepEqual(got, added) {
		t.Errorf("Decode(InterfacesAdded) = %+v, %v; want %+v", got, err, added)
	}
	removed := dbusx.RemovedInterfaces{Object: managedPath, Interfaces: []string{watchedIface, otherIface}}
	gotRemoved, err := dbusx.InterfacesRemoved.Decode(&dbus.Signal{
		Name: dbusx.ObjectManagerInterface + ".InterfacesRemoved",
		Body: []any{removed.Object, removed.Interfaces},
	})
	if err != nil || !reflect.DeepEqual(gotRemoved, removed) {
		t.Errorf("Decode(InterfacesRemoved) = %+v, %v; want %+v", gotRemoved, err, removed)
	}
	if _, err := dbusx.InterfacesAdded.Decode(&dbus.Signal{
		Name: dbusx.InterfacesAdded.Name(),
		Body: []any{removed.Object, removed.Interfaces},
	}); err == nil {
		t.Error("an InterfacesAdded carrying interface names decoded, want it refused")
	}
}

func TestAnInterfacesAddedHandlerBorrowsTheDescriptorsInItsProperties(t *testing.T) {
	w := startWatched(t)
	pipe := dbustest.NewPipe(t)
	borrowed := make(chan error, 1)
	ticked := make(chan uint32, 1)
	watch, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), func(err error) { t.Errorf("the watch failed: %v", err) },
		dbusx.On(dbusx.InterfacesAdded, watchedPath, func(a dbusx.AddedInterfaces, _ dbus.Sequence) error {
			v := a.Interfaces[watchedIface]["Pipe"]
			fd, ok := v.Value().(dbus.UnixFD)
			if !ok {
				borrowed <- fmt.Errorf("%s was added with a Pipe that is a %s, want h", a.Object, v.Signature())
				return nil
			}
			var st unix.Stat_t
			borrowed <- unix.Fstat(int(fd), &st)
			return nil
		}),
		dbusx.On(tickSignal, watchedPath, func(c counted, _ dbus.Sequence) error {
			ticked <- c.N
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Stop() })

	if err := dbusx.InterfacesAdded.Emit(w.service, watchedPath, dbusx.AddedInterfaces{
		Object:     managedPath,
		Interfaces: map[string]map[string]dbus.Variant{watchedIface: {"Pipe": dbus.MakeVariant(pipe.Write)}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tickSignal.Emit(w.service, watchedPath, counted{N: 1}); err != nil {
		t.Fatal(err)
	}
	if err := awaitWatch(t, borrowed, "InterfacesAdded in its handler"); err != nil {
		t.Errorf("the handler's descriptor: %v, want it open while the handler runs", err)
	}
	awaitWatch(t, ticked, "Tick behind the InterfacesAdded")
	pipe.RequireEveryCopyClosed(t)
}

func TestAWatchTakesTheObjectManagerSignalsFromTheOwnerOnly(t *testing.T) {
	w := startWatched(t)
	got := make(chan string, 16)
	watch, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), func(err error) { t.Errorf("the watch failed: %v", err) },
		dbusx.On(dbusx.InterfacesAdded, watchedPath, func(a dbusx.AddedInterfaces, _ dbus.Sequence) error {
			got <- "added " + string(a.Object) + " " + slices.Sorted(maps.Keys(a.Interfaces))[0]
			return nil
		}),
		dbusx.On(dbusx.InterfacesRemoved, watchedPath, func(r dbusx.RemovedInterfaces, _ dbus.Sequence) error {
			got <- "removed " + string(r.Object) + " " + r.Interfaces[0]
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Stop() })

	forged := dbus.ObjectPath(string(managedPath) + "/Forged")
	impostor := w.bus.Conn(t)
	if err := errors.Join(
		dbusx.InterfacesAdded.Emit(impostor, watchedPath, dbusx.AddedInterfaces{Object: forged, Interfaces: map[string]map[string]dbus.Variant{otherIface: {}}}),
		dbusx.InterfacesRemoved.Emit(impostor, watchedPath, dbusx.RemovedInterfaces{Object: forged, Interfaces: []string{otherIface}}),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := dbusx.NameOwner(t.Context(), impostor, watchedName); err != nil {
		t.Fatalf("a round trip that puts the forged signals ahead of the owner's: %v", err)
	}
	if err := errors.Join(
		dbusx.InterfacesAdded.Emit(w.service, watchedPath, dbusx.AddedInterfaces{Object: managedPath, Interfaces: map[string]map[string]dbus.Variant{watchedIface: {}}}),
		dbusx.InterfacesRemoved.Emit(w.service, watchedPath, dbusx.RemovedInterfaces{Object: managedPath, Interfaces: []string{watchedIface}}),
	); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"added " + string(managedPath) + " " + watchedIface,
		"removed " + string(managedPath) + " " + watchedIface,
	} {
		if g := awaitWatch(t, got, want); g != want {
			t.Fatalf("the watch saw %q, want %q: only the owner's", g, want)
		}
	}
}
