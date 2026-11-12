package dbusx_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const playersNamespace = "dev.stubbe.lys.Players"

func ownerChanged(from, name, old, owner string) *dbus.Signal {
	return &dbus.Signal{Sender: from, Path: busPath, Name: busName + ".NameOwnerChanged", Body: []any{name, old, owner}}
}

func TestOnUnderTakesTheObjectsInItsPathNamespace(t *testing.T) {
	const sender = ":1.7"
	sent := []dbus.ObjectPath{
		watchedPath,
		watchedPath + "/Child",
		watchedPath + "/Child/Grandchild",
		watchedPath + "Sibling",
		"/dev/stubbe/lys",
		otherPath,
	}
	for _, tc := range []struct {
		namespace dbus.ObjectPath
		want      []dbus.ObjectPath
	}{
		{watchedPath, sent[:3]},
		{"/", sent},
	} {
		var got []dbus.ObjectPath
		routes := []dbusx.Route{dbusx.OnUnder(tickSignal, tc.namespace, func(path dbus.ObjectPath, _ counted, _ dbus.Sequence) error {
			got = append(got, path)
			return nil
		})}
		for _, path := range sent {
			if err := dbusx.Dispatch(&dbus.Signal{Sender: sender, Path: path, Name: tickSignal.Name(), Body: []any{uint32(1)}}, sender, routes); err != nil {
				t.Errorf("Dispatch(Tick at %s) = %v", path, err)
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("under %s the route took %q, want %q", tc.namespace, got, tc.want)
		}
	}
}

func TestWithArg0NamespaceTakesTheNamesInTheNamespace(t *testing.T) {
	var got []string
	routes := []dbusx.Route{dbusx.OnNameOwnerChanged(func(c dbusx.OwnerChange, _ dbus.Sequence) error {
		got = append(got, c.Name)
		return nil
	}).WithArg0Namespace(playersNamespace)}
	for _, name := range []string{
		playersNamespace,
		playersNamespace + ".vlc",
		playersNamespace + ".vlc.instance1",
		playersNamespace + "x",
		playersNamespace + "x.vlc",
		"dev.stubbe.lys",
		":1.4",
	} {
		if err := dbusx.Dispatch(ownerChanged(busName, name, "", ":1.5"), "", routes); err != nil {
			t.Errorf("Dispatch(NameOwnerChanged of %s) = %v", name, err)
		}
	}
	if want := []string{playersNamespace, playersNamespace + ".vlc", playersNamespace + ".vlc.instance1"}; !slices.Equal(got, want) {
		t.Errorf("the route took %q, want %q", got, want)
	}
}

func TestOnNameLostTakesOnlyTheBussAnnouncementOfALoss(t *testing.T) {
	var got []string
	routes := []dbusx.Route{dbusx.OnNameLost(func(name string, announced dbus.Sequence) error {
		if announced != 5 {
			t.Errorf("%s lost at sequence %d, want the signal's 5", name, announced)
		}
		got = append(got, name)
		return nil
	})}
	for _, sig := range []*dbus.Signal{
		ownerChanged(busName, watchedName, "", ":1.2"),
		ownerChanged(busName, watchedName, ":1.2", ":1.3"),
		ownerChanged(busName, watchedName, ":1.3", ""),
		ownerChanged(busName, ":1.3", ":1.3", ""),
		ownerChanged(":1.9", "dev.stubbe.lys.Forged", ":1.4", ""),
	} {
		sig.Sequence = 5
		if err := dbusx.Dispatch(sig, ":1.7", routes); err != nil {
			t.Errorf("Dispatch = %v", err)
		}
	}
	if want := []string{watchedName, ":1.3"}; !slices.Equal(got, want) {
		t.Errorf("names lost %q, want %q: the bus's announcements of a name left without an owner", got, want)
	}
}

func TestAWatchFollowsNamespacesOnTheBus(t *testing.T) {
	w := startWatched(t)
	before := dbustest.MatchRules(t, w.client)
	failed := func(err error) { t.Errorf("a watch failed: %v", err) }
	paths := make(chan dbus.ObjectPath, 8)
	objects, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), failed,
		dbusx.OnUnder(tickSignal, watchedPath, func(path dbus.ObjectPath, _ counted, _ dbus.Sequence) error {
			paths <- path
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Stop() })
	names := make(chan string, 8)
	owners, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromBus(), failed,
		dbusx.OnNameOwnerChanged(func(c dbusx.OwnerChange, _ dbus.Sequence) error {
			if c.NewOwner != "" {
				names <- c.Name
			}
			return nil
		}).WithArg0Namespace(playersNamespace))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owners.Stop() })

	var pathNamespace, arg0Namespace int
	for _, rule := range rulesAdded(t, w.client, before) {
		if strings.Contains(rule, "path_namespace='"+string(watchedPath)+"'") {
			pathNamespace++
		}
		if strings.Contains(rule, "arg0namespace='"+playersNamespace+"'") {
			arg0Namespace++
		}
	}
	if pathNamespace != 1 || arg0Namespace != 1 {
		t.Errorf("rules %q: want one narrowed to the path namespace and one to the name namespace", rulesAdded(t, w.client, before))
	}

	for _, path := range []dbus.ObjectPath{watchedPath + "Sibling", watchedPath, watchedPath + "/Child", "/elsewhere", watchedPath} {
		if err := tickSignal.Emit(w.service, path, counted{N: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []dbus.ObjectPath{watchedPath, watchedPath + "/Child", watchedPath} {
		if got := awaitWatch(t, paths, "Tick in the namespace"); got != want {
			t.Fatalf("Tick from %s, want %s: only the objects in the namespace, in order", got, want)
		}
	}
	for _, name := range []string{playersNamespace + "x", playersNamespace + ".A", playersNamespace} {
		ownAs(t, w.bus, name)
	}
	for _, want := range []string{playersNamespace + ".A", playersNamespace} {
		if got := awaitWatch(t, names, "a name in the namespace gaining an owner"); got != want {
			t.Fatalf("%s gained an owner, want %s: only the names in the namespace, in order", got, want)
		}
	}
}

func TestAPropertiesChangedHandlerBorrowsTheDescriptorsInItsValues(t *testing.T) {
	w := startWatched(t)
	pipe := dbustest.NewPipe(t)
	borrowed := make(chan error, 1)
	ticked := make(chan uint32, 1)
	watch, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), func(err error) { t.Errorf("the watch failed: %v", err) },
		dbusx.On(dbusx.PropertiesChanged, watchedPath, func(c dbusx.ChangedProperties, _ dbus.Sequence) error {
			v := c.Changed["Pipe"]
			fd, ok := v.Value().(dbus.UnixFD)
			if !ok {
				borrowed <- fmt.Errorf("the Pipe property changed to a %s, want h", v.Signature())
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

	if err := dbusx.PropertiesChanged.Emit(w.service, watchedPath, dbusx.ChangedProperties{
		Interface:   watchedIface,
		Changed:     map[string]dbus.Variant{"Pipe": dbus.MakeVariant(pipe.Write)},
		Invalidated: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tickSignal.Emit(w.service, watchedPath, counted{N: 1}); err != nil {
		t.Fatal(err)
	}
	if err := awaitWatch(t, borrowed, "PropertiesChanged in its handler"); err != nil {
		t.Errorf("the handler's descriptor: %v, want it open while the handler runs", err)
	}
	awaitWatch(t, ticked, "Tick behind the PropertiesChanged")
	pipe.RequireEveryCopyClosed(t)
}

func TestRemoveSignalClosesTheDescriptorsOfWhatNobodyRead(t *testing.T) {
	w := startWatched(t)
	rule := []dbus.MatchOption{dbus.WithMatchSender(watchedName), dbus.WithMatchObjectPath(watchedPath), dbus.WithMatchInterface(watchedIface)}
	if err := w.client.AddMatchSignal(rule...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.client.RemoveMatchSignal(rule...) })
	signals := make(chan *dbus.Signal, 8)
	w.client.Signal(signals)
	ticked := make(chan uint32, 1)
	marker, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), func(error) {},
		dbusx.On(tickSignal, watchedPath, func(c counted, _ dbus.Sequence) error {
			ticked <- c.N
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = marker.Stop() })

	pipe := dbustest.NewPipe(t)
	const sent = 3
	for range sent {
		if err := handSignal.Emit(w.service, watchedPath, handed{FD: pipe.Write}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tickSignal.Emit(w.service, watchedPath, counted{N: 1}); err != nil {
		t.Fatal(err)
	}
	awaitWatch(t, ticked, "Tick behind every Hand, by which the connection has read them all")
	if unread := len(signals); unread != sent+1 {
		t.Fatalf("%d signals unread, want the %d Hands and the Tick", unread, sent)
	}
	if err := dbusx.RemoveSignal(w.client, signals); err != nil {
		t.Errorf("RemoveSignal = %v", err)
	}
	if unread := len(signals); unread != 0 {
		t.Errorf("%d signals left on the channel, want it drained", unread)
	}
	pipe.RequireEveryCopyClosed(t)
}
