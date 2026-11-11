package dbusx_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const (
	busName = "org.freedesktop.DBus"
	busPath = dbus.ObjectPath("/org/freedesktop/DBus")
)

func TestOnOthersTakesWhatNoRouteBeforeItDoes(t *testing.T) {
	const sender = ":1.7"
	var got []string
	routes := []dbusx.Route{
		dbusx.On(tickSignal, watchedPath, func(c counted, _ dbus.Sequence) error {
			got = append(got, "tick")
			return nil
		}),
		dbusx.OnOthers(watchedIface, watchedPath, func(sig *dbus.Signal) error {
			got = append(got, "other "+sig.Name)
			return nil
		}),
		dbusx.OnOthers(otherIface, watchedPath, func(sig *dbus.Signal) error {
			got = append(got, "about "+sig.Body[0].(string))
			return nil
		}).WithArg0("mine"),
	}
	for _, sig := range []*dbus.Signal{
		{Sender: sender, Path: watchedPath, Name: tickSignal.Name(), Body: []any{uint32(1)}},
		{Sender: sender, Path: watchedPath, Name: tockSignal.Name(), Body: []any{uint32(2)}},
		{Sender: sender, Path: watchedPath, Name: watchedIface + ".Undeclared"},
		{Sender: sender, Path: otherPath, Name: tockSignal.Name(), Body: []any{uint32(3)}},
		{Sender: ":1.9", Path: watchedPath, Name: tockSignal.Name(), Body: []any{uint32(4)}},
		{Sender: sender, Path: watchedPath, Name: watchedIface + "x.Tock", Body: []any{uint32(5)}},
		{Sender: sender, Path: watchedPath, Name: aboutSignal.Name(), Body: []any{"mine", uint32(6)}},
		{Sender: sender, Path: watchedPath, Name: aboutSignal.Name(), Body: []any{"theirs", uint32(7)}},
	} {
		if err := dbusx.Dispatch(sig, sender, routes); err != nil {
			t.Errorf("Dispatch(%s at %s from %s) = %v", sig.Name, sig.Path, sig.Sender, err)
		}
	}
	want := []string{"tick", "other " + tockSignal.Name(), "other " + watchedIface + ".Undeclared", "about mine"}
	if !slices.Equal(got, want) {
		t.Errorf("handled %q, want %q", got, want)
	}
}

func TestAnOwnerChangeIsTheBussWhoeverTheWatchFollows(t *testing.T) {
	const sender = ":1.7"
	var got []string
	routes := []dbusx.Route{
		dbusx.OnOwnerChange(watchedName, func(old, owner string, announced dbus.Sequence) error {
			got = append(got, old+">"+owner)
			if announced != 5 {
				t.Errorf("announced at sequence %d, want the signal's 5", announced)
			}
			return nil
		}),
	}
	change := func(from, name, old, owner string) *dbus.Signal {
		return &dbus.Signal{Sender: from, Path: busPath, Name: busName + ".NameOwnerChanged", Body: []any{name, old, owner}, Sequence: 5}
	}
	for _, following := range []string{sender, ""} {
		for _, sig := range []*dbus.Signal{
			change(busName, watchedName, ":1.1", ":1.2"),
			change(sender, watchedName, ":1.2", ":1.66"),
			change(":1.9", watchedName, ":1.2", ":1.66"),
			change(busName, "dev.stubbe.lys.Another", ":1.3", ""),
			change(busName, watchedName, ":1.2", ""),
		} {
			if err := dbusx.Dispatch(sig, following, routes); err != nil {
				t.Errorf("Dispatch = %v", err)
			}
		}
	}
	if want := []string{":1.1>:1.2", ":1.2>", ":1.1>:1.2", ":1.2>"}; !slices.Equal(got, want) {
		t.Errorf("owner changes %q, want %q: the bus's about the name, whether or not the name has an owner", got, want)
	}
}

func TestAWatchLearnsInOrderThatItsSendersNameChangedOwner(t *testing.T) {
	w := startWatched(t)
	first := w.service.Names()[0]
	got := make(chan string, 16)
	watch, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), func(err error) { t.Errorf("the watch failed: %v", err) },
		dbusx.On(tickSignal, watchedPath, func(c counted, _ dbus.Sequence) error {
			got <- "tick " + string(rune('0'+c.N))
			return nil
		}),
		dbusx.OnOwnerChange(watchedName, func(old, owner string, _ dbus.Sequence) error {
			got <- old + ">" + owner
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Stop() })

	emitBehindTheBus(t, w.service, 1)
	if _, err := w.service.ReleaseName(watchedName); err != nil {
		t.Fatal(err)
	}
	emitBehindTheBus(t, w.service, 9)
	next := ownAs(t, w.bus, watchedName)
	second := next.Names()[0]
	emitBehindTheBus(t, next, 2)
	for _, want := range []string{"tick 1", first + ">", ">" + second, "tick 2"} {
		if g := awaitWatch(t, got, want); g != want {
			t.Fatalf("the watch saw %q, want %q: each owner's signals with the change between them", g, want)
		}
	}
}

func rulesAdded(t *testing.T, conn *dbus.Conn, before []string) []string {
	t.Helper()
	var added []string
	for _, rule := range dbustest.MatchRules(t, conn) {
		if !slices.Contains(before, rule) {
			added = append(added, rule)
		}
	}
	return added
}

func TestAWatchsRulesAreAsNarrowAsItsRoutesAllow(t *testing.T) {
	w := startWatched(t)
	before := dbustest.MatchRules(t, w.client)
	aboutMine := func(about, dbus.Sequence) error { return nil }
	watch, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), func(error) {},
		dbusx.On(aboutSignal, watchedPath, aboutMine).WithArg0("mine"),
		dbusx.OnOthers(otherIface, watchedPath, func(*dbus.Signal) error { return nil }).WithArg0("mine"),
		dbusx.On(aboutSignal, otherPath, aboutMine).WithArg0("mine"),
		dbusx.On(aboutSignal, otherPath, aboutMine).WithArg0("yours"),
		dbusx.OnOwnerChange(watchedName, func(string, string, dbus.Sequence) error { return nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	var sharedArg0, sharedMember, owner int
	for _, rule := range rulesAdded(t, w.client, before) {
		switch {
		case strings.Contains(rule, "sender='"+busName+"'") && strings.Contains(rule, "arg0='"+watchedName+"'"):
			owner++
		case strings.Contains(rule, "path='"+string(watchedPath)+"'") && strings.Contains(rule, "arg0='mine'") && !strings.Contains(rule, "member="):
			sharedArg0++
		case strings.Contains(rule, "path='"+string(otherPath)+"'") && strings.Contains(rule, "member='About'") && !strings.Contains(rule, "arg0="):
			sharedMember++
		default:
			t.Errorf("unexpected rule %q", rule)
		}
	}
	if sharedArg0 != 1 || sharedMember != 1 || owner != 1 {
		t.Errorf("rules %q: want one for the interface narrowed to the first argument its routes share, one for the signal routed by two first arguments, and one for the owner's change, which the watch and the route both read", rulesAdded(t, w.client, before))
	}
	if err := watch.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := dbustest.MatchRules(t, w.client); !slices.Equal(got, before) {
		t.Errorf("after Stop the connection holds %q, want %q", got, before)
	}
}
