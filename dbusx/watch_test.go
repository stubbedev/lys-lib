package dbusx_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const (
	watchedName  = "dev.stubbe.lys.Watched"
	watchedIface = "dev.stubbe.lys.Watched"
	otherIface   = "dev.stubbe.lys.Other"
	watchedPath  = dbus.ObjectPath("/dev/stubbe/lys/Watched")
	otherPath    = dbus.ObjectPath("/dev/stubbe/lys/Other")
	watchWait    = 5 * time.Second
)

type counted struct {
	N uint32 `dbus:"n"`
}

type about struct {
	Subject string `dbus:"subject"`
	N       uint32 `dbus:"n"`
}

type handed struct {
	FD dbus.UnixFD `dbus:"fd"`
}

var (
	tickSignal  = dbusx.NewSignal[counted](watchedIface, "Tick")
	tockSignal  = dbusx.NewSignal[counted](watchedIface, "Tock")
	aboutSignal = dbusx.NewSignal[about](otherIface, "About")
	handSignal  = dbusx.NewSignal[handed](watchedIface, "Hand")
)

func watchPipe(t *testing.T) (read int, write dbus.UnixFD) {
	t.Helper()
	fds := make([]int, 2)
	if err := unix.Pipe2(fds, unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fds[0]) })
	return fds[0], dbus.UnixFD(fds[1])
}

func writeEndClosed(t *testing.T, read int) bool {
	t.Helper()
	n, err := unix.Read(read, make([]byte, 1))
	if errors.Is(err, unix.EAGAIN) {
		return false
	}
	if n != 0 || err != nil {
		t.Fatalf("read the pipe = %d, %v", n, err)
	}
	return true
}

func TestDispatchRoutesByObjectSignalAndFirstArgument(t *testing.T) {
	const sender = ":1.7"
	var got []string
	record := func(what string) func(counted, dbus.Sequence) error {
		return func(c counted, _ dbus.Sequence) error {
			got = append(got, what+" "+string(rune('0'+c.N)))
			return nil
		}
	}
	routes := []dbusx.Route{
		dbusx.On(tickSignal, watchedPath, record("tick")),
		dbusx.On(tockSignal, watchedPath, record("tock")),
		dbusx.On(tickSignal, otherPath, record("other tick")),
		dbusx.On(aboutSignal, watchedPath, func(a about, _ dbus.Sequence) error {
			got = append(got, "about "+a.Subject)
			return nil
		}).WithArg0("mine"),
	}
	for _, sig := range []*dbus.Signal{
		{Sender: sender, Path: watchedPath, Name: tickSignal.Name(), Body: []any{uint32(1)}},
		{Sender: sender, Path: otherPath, Name: tickSignal.Name(), Body: []any{uint32(2)}},
		{Sender: sender, Path: watchedPath, Name: tockSignal.Name(), Body: []any{uint32(3)}},
		{Sender: sender, Path: otherPath, Name: tockSignal.Name(), Body: []any{uint32(4)}},
		{Sender: sender, Path: watchedPath, Name: watchedIface + ".Unrouted", Body: []any{uint32(5)}},
		{Sender: ":1.9", Path: watchedPath, Name: tickSignal.Name(), Body: []any{uint32(6)}},
		{Sender: sender, Path: watchedPath, Name: aboutSignal.Name(), Body: []any{"mine", uint32(7)}},
		{Sender: sender, Path: watchedPath, Name: aboutSignal.Name(), Body: []any{"theirs", uint32(8)}},
	} {
		if err := dbusx.Dispatch(sig, sender, routes); err != nil {
			t.Errorf("Dispatch(%s at %s from %s) = %v", sig.Name, sig.Path, sig.Sender, err)
		}
	}
	if want := []string{"tick 1", "other tick 2", "tock 3", "about mine"}; !slices.Equal(got, want) {
		t.Errorf("handled %q, want %q", got, want)
	}
}

func TestDispatchReportsWhatARouteCannotHandOver(t *testing.T) {
	const sender = ":1.7"
	refusal := errors.New("refused")
	routes := []dbusx.Route{
		dbusx.On(tickSignal, watchedPath, func(counted, dbus.Sequence) error { return refusal }),
		dbusx.On(aboutSignal, watchedPath, func(about, dbus.Sequence) error {
			t.Error("a signal that does not decode reached its handler")
			return nil
		}).WithArg0("mine"),
	}
	if err := dbusx.Dispatch(&dbus.Signal{Sender: sender, Path: watchedPath, Name: tickSignal.Name(), Body: []any{uint32(1)}}, sender, routes); !errors.Is(err, refusal) {
		t.Errorf("a handler's error = %v, want it returned", err)
	}
	for name, body := range map[string][]any{
		"a wrong type":           {"mine", "seven"},
		"too few arguments":      {"mine"},
		"no argument at all":     {},
		"a first argument not s": {uint32(1), uint32(2)},
	} {
		err := dbusx.Dispatch(&dbus.Signal{Sender: sender, Path: watchedPath, Name: aboutSignal.Name(), Body: body}, sender, routes)
		if err == nil || !strings.Contains(err.Error(), "decode "+aboutSignal.Name()) {
			t.Errorf("%s: Dispatch = %v, want the decode failure", name, err)
		}
	}
}

func TestDispatchClosesTheDescriptorsOfASignalItsHandlerDoesNotGet(t *testing.T) {
	const sender = ":1.7"
	var took []dbus.UnixFD
	routes := []dbusx.Route{
		dbusx.On(handSignal, watchedPath, func(h handed, _ dbus.Sequence) error {
			took = append(took, h.FD)
			return nil
		}),
		dbusx.On(tickSignal, watchedPath, func(counted, dbus.Sequence) error { return nil }),
	}
	tests := []struct {
		name       string
		sig        func(fd dbus.UnixFD) *dbus.Signal
		wantErr    bool
		wantClosed bool
	}{
		{"from the sender", func(fd dbus.UnixFD) *dbus.Signal {
			return &dbus.Signal{Sender: sender, Path: watchedPath, Name: handSignal.Name(), Body: []any{fd}}
		}, false, false},
		{"from another peer", func(fd dbus.UnixFD) *dbus.Signal {
			return &dbus.Signal{Sender: ":1.9", Path: watchedPath, Name: handSignal.Name(), Body: []any{fd}}
		}, false, true},
		{"not decoding", func(fd dbus.UnixFD) *dbus.Signal {
			return &dbus.Signal{Sender: sender, Path: watchedPath, Name: tickSignal.Name(), Body: []any{fd}}
		}, true, true},
		{"of no route", func(fd dbus.UnixFD) *dbus.Signal {
			return &dbus.Signal{Sender: sender, Path: otherPath, Name: handSignal.Name(), Body: []any{fd}}
		}, false, true},
		{"of no route, inside a variant", func(fd dbus.UnixFD) *dbus.Signal {
			return &dbus.Signal{Sender: sender, Path: watchedPath, Name: watchedIface + ".Unrouted", Body: []any{map[string]dbus.Variant{"fd": dbus.MakeVariant(fd)}}}
		}, false, true},
	}
	for _, tc := range tests {
		read, write := watchPipe(t)
		err := dbusx.Dispatch(tc.sig(write), sender, routes)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: Dispatch = %v, want an error: %v", tc.name, err, tc.wantErr)
		}
		if closed := writeEndClosed(t, read); closed != tc.wantClosed {
			t.Errorf("%s: descriptor closed: %v, want %v", tc.name, closed, tc.wantClosed)
		}
		if !tc.wantClosed {
			if err := unix.Close(int(write)); err != nil {
				t.Errorf("%s: the descriptor left open: %v", tc.name, err)
			}
		}
	}
	if len(took) != 1 {
		t.Errorf("the handler took %d descriptors, want the sender's one", len(took))
	}
}

type watched struct {
	bus     *dbustest.Bus
	service *dbus.Conn
	client  *dbus.Conn
}

func startWatched(t *testing.T) watched {
	t.Helper()
	bus := dbustest.Start(t)
	w := watched{bus: bus, service: bus.Conn(t), client: bus.Conn(t)}
	if _, err := dbusx.Own(w.service, watchedName); err != nil {
		t.Fatal(err)
	}
	return w
}

func awaitWatch[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(watchWait):
		t.Fatalf("no %s within %s", what, watchWait)
		var zero T
		return zero
	}
}

func TestWatchSignalsDeliversTheSendersSignalsInOrder(t *testing.T) {
	w := startWatched(t)
	got := make(chan string, 16)
	failed := make(chan error, 16)
	note := func(what string) func(counted, dbus.Sequence) error {
		return func(c counted, _ dbus.Sequence) error {
			got <- what + " " + string(rune('0'+c.N))
			return nil
		}
	}
	watch, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), func(err error) { failed <- err },
		dbusx.On(tickSignal, watchedPath, note("tick")),
		dbusx.On(tockSignal, watchedPath, note("tock")),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Stop() })

	forger := w.bus.Conn(t)
	if err := tickSignal.Emit(forger, watchedPath, counted{N: 9}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbusx.NameOwner(t.Context(), forger, watchedName); err != nil {
		t.Fatalf("a round trip that puts the forged signal ahead of the sender's: %v", err)
	}
	if err := errors.Join(
		tickSignal.Emit(w.service, otherPath, counted{N: 8}),
		tickSignal.Emit(w.service, watchedPath, counted{N: 1}),
		tockSignal.Emit(w.service, watchedPath, counted{N: 2}),
		w.service.Emit(watchedPath, tockSignal.Name(), "three"),
		tickSignal.Emit(w.service, watchedPath, counted{N: 4}),
	); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tick 1", "tock 2", "tick 4"} {
		if g := awaitWatch(t, got, want); g != want {
			t.Fatalf("handled %q, want %q", g, want)
		}
	}
	if err := awaitWatch(t, failed, "report of the signal that does not decode"); !strings.Contains(err.Error(), "decode "+tockSignal.Name()) {
		t.Errorf("failed = %v, want the Tock that does not decode", err)
	}
	if err := watch.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case g := <-got:
		t.Errorf("handled %q too: a forged signal or another object's", g)
	case err := <-failed:
		t.Errorf("failed with %v too", err)
	default:
	}
}

func TestAWatchHoldsOneRulePerInterfaceOfAnObjectUntilItEnds(t *testing.T) {
	w := startWatched(t)
	other := []dbus.MatchOption{dbus.WithMatchInterface(otherIface)}
	if err := w.client.AddMatchSignal(other...); err != nil {
		t.Fatal(err)
	}
	before := dbustest.MatchRules(t, w.client)
	ignore := func(counted, dbus.Sequence) error { return nil }
	watch, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), func(error) {},
		dbusx.On(tickSignal, watchedPath, ignore),
		dbusx.On(tockSignal, watchedPath, ignore),
		dbusx.On(tickSignal, otherPath, ignore),
		dbusx.On(aboutSignal, watchedPath, func(about, dbus.Sequence) error { return nil }).WithArg0("mine"),
	)
	if err != nil {
		t.Fatal(err)
	}
	during := dbustest.MatchRules(t, w.client)
	if len(during) != len(before)+4 {
		t.Fatalf("the watch holds %q, want four rules more than %q: three for its signals and the one that follows its sender's owner", during, before)
	}
	var whole, single, narrowed, owner int
	for _, rule := range during {
		if slices.Contains(before, rule) {
			continue
		}
		if strings.Contains(rule, "member='NameOwnerChanged'") && strings.Contains(rule, "sender='org.freedesktop.DBus'") && strings.Contains(rule, "arg0='"+watchedName+"'") {
			owner++
			continue
		}
		if !strings.Contains(rule, "sender='"+watchedName+"'") {
			t.Errorf("rule %q does not name the sender", rule)
		}
		switch {
		case strings.Contains(rule, "arg0='mine'") && strings.Contains(rule, "member='About'"):
			narrowed++
		case strings.Contains(rule, "member='Tick'") && strings.Contains(rule, string(otherPath)):
			single++
		case !strings.Contains(rule, "member=") && strings.Contains(rule, string(watchedPath)):
			whole++
		}
	}
	if whole != 1 || single != 1 || narrowed != 1 || owner != 1 {
		t.Errorf("rules %q: want one for the interface two signals share, one for the single signal, one narrowed by its first argument, one for the sender's owner changing", during)
	}
	for range 2 {
		if err := watch.Stop(); err != nil {
			t.Fatal(err)
		}
		if got := dbustest.MatchRules(t, w.client); !slices.Equal(got, before) {
			t.Fatalf("after Stop the connection holds %q, want %q", got, before)
		}
	}
	select {
	case <-watch.Done():
	default:
		t.Error("Done is open after Stop")
	}
}

func TestAWatchOfNoSignalFails(t *testing.T) {
	bus := dbustest.Start(t)
	client := bus.Conn(t)
	before := dbustest.MatchRules(t, client)
	if _, err := dbusx.WatchSignals(t.Context(), client, dbusx.FromBus(), func(error) {}); err == nil {
		t.Error("a watch of no signal started")
	}
	if _, err := dbusx.WatchSignals(t.Context(), client, dbusx.FromOwner(watchedName), func(error) {}); err == nil || !strings.Contains(err.Error(), watchedName) {
		t.Errorf("a watch of no signal of %s = %v, want an error naming the sender", watchedName, err)
	}
	if got := dbustest.MatchRules(t, client); !slices.Equal(got, before) {
		t.Errorf("the failed watches left the rules %q, want %q", got, before)
	}
}

func ownAs(t *testing.T, bus *dbustest.Bus, name string) *dbus.Conn {
	t.Helper()
	conn := bus.Conn(t)
	if _, err := dbusx.Own(conn, name); err != nil {
		t.Fatal(err)
	}
	return conn
}

func emitBehindTheBus(t *testing.T, conn *dbus.Conn, n uint32) {
	t.Helper()
	if err := tickSignal.Emit(conn, watchedPath, counted{N: n}); err != nil {
		t.Fatal(err)
	}
	if err := conn.BusObject().CallWithContext(t.Context(), "org.freedesktop.DBus.GetId", 0).Err; err != nil {
		t.Fatalf("a round trip that puts the signal ahead of what follows: %v", err)
	}
}

func TestAWatchFollowsItsSendersNameFromNoOwnerToOwnerToOwner(t *testing.T) {
	bus := dbustest.Start(t)
	client := bus.Conn(t)
	broad := []dbus.MatchOption{dbus.WithMatchInterface(watchedIface)}
	if err := client.AddMatchSignal(broad...); err != nil {
		t.Fatal(err)
	}
	got := make(chan uint32, 16)
	watch, err := dbusx.WatchSignals(t.Context(), client, dbusx.FromOwner(watchedName), func(err error) { t.Errorf("the watch failed: %v", err) },
		dbusx.On(tickSignal, watchedPath, func(c counted, _ dbus.Sequence) error {
			got <- c.N
			return nil
		}))
	if err != nil {
		t.Fatalf("a watch of a name nobody owns yet: %v", err)
	}
	stranger := bus.Conn(t)
	emitBehindTheBus(t, stranger, 90)

	first := ownAs(t, bus, watchedName)
	emitBehindTheBus(t, first, 1)
	emitBehindTheBus(t, stranger, 91)
	emitBehindTheBus(t, first, 2)
	if _, err := first.ReleaseName(watchedName); err != nil {
		t.Fatal(err)
	}
	emitBehindTheBus(t, first, 92)

	second := ownAs(t, bus, watchedName)
	emitBehindTheBus(t, first, 93)
	emitBehindTheBus(t, second, 3)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	third := ownAs(t, bus, watchedName)
	emitBehindTheBus(t, third, 4)

	for _, want := range []uint32{1, 2, 3, 4} {
		if n := awaitWatch(t, got, "Tick from the name's owner"); n != want {
			t.Fatalf("handled Tick %d, want %d: only what the name's owner of the moment sent, across its owners", n, want)
		}
	}
	if err := watch.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-got:
		t.Errorf("handled Tick %d too, from a peer that did not own the name", n)
	default:
	}
}

func TestAWatchStartedOnAnOwnedNameFollowsItToTheNextOwner(t *testing.T) {
	w := startWatched(t)
	got := make(chan uint32, 4)
	watch, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), func(err error) { t.Errorf("the watch failed: %v", err) },
		dbusx.On(tickSignal, watchedPath, func(c counted, _ dbus.Sequence) error {
			got <- c.N
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Stop() })
	emitBehindTheBus(t, w.service, 1)
	if err := w.service.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := ownAs(t, w.bus, watchedName)
	emitBehindTheBus(t, restarted, 2)
	for _, want := range []uint32{1, 2} {
		if n := awaitWatch(t, got, "Tick from the name's owner"); n != want {
			t.Fatalf("handled Tick %d, want %d", n, want)
		}
	}
}

func TestHandlersGetTheSequenceThatOrdersASignalAgainstAReply(t *testing.T) {
	w := startWatched(t)
	ticks, owners := make(chan dbus.Sequence, 4), make(chan dbus.Sequence, 4)
	watch, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), func(err error) { t.Errorf("the watch failed: %v", err) },
		dbusx.On(tickSignal, watchedPath, func(_ counted, seq dbus.Sequence) error {
			ticks <- seq
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Stop() })
	stop, err := dbusx.WatchOwner(t.Context(), w.client, watchedName, func(seq dbus.Sequence) { owners <- seq })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stop() })
	reply := func() dbus.Sequence {
		t.Helper()
		call := w.client.BusObject().CallWithContext(t.Context(), "org.freedesktop.DBus.GetId", 0)
		if call.Err != nil {
			t.Fatal(call.Err)
		}
		return call.ResponseSequence
	}

	emitBehindTheBus(t, w.service, 1)
	before := awaitWatch(t, ticks, "Tick sent before the call")
	answered := reply()
	if err := w.service.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := ownAs(t, w.bus, watchedName)
	emitBehindTheBus(t, restarted, 2)
	appeared := awaitWatch(t, owners, "announcement of the restarted owner")
	after := awaitWatch(t, ticks, "Tick sent after the call")
	if before >= answered || answered >= appeared || appeared >= after {
		t.Errorf("sequences: Tick %d, the reply %d, the new owner %d, its Tick %d; want them in that order", before, answered, appeared, after)
	}
}

func TestAWatchEndsWithItsContextOrItsConnection(t *testing.T) {
	w := startWatched(t)
	ignore := dbusx.On(tickSignal, watchedPath, func(counted, dbus.Sequence) error { return nil })
	before := dbustest.MatchRules(t, w.client)
	ctx, cancel := context.WithCancel(t.Context())
	ended, err := dbusx.WatchSignals(ctx, w.client, dbusx.FromOwner(watchedName), func(error) {}, ignore)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	awaitWatch(t, ended.Done(), "end of the watch whose context ended")
	if err := ended.Stop(); err != nil {
		t.Errorf("Stop after the context ended = %v", err)
	}
	if got := dbustest.MatchRules(t, w.client); !slices.Equal(got, before) {
		t.Errorf("the ended watch left the rules %q, want %q", got, before)
	}

	cut, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), func(error) {}, ignore)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.client.Close(); err != nil {
		t.Fatal(err)
	}
	awaitWatch(t, cut.Done(), "end of the watch whose connection closed")
	if err := cut.Stop(); !errors.Is(err, dbusx.ErrDisconnected) {
		t.Errorf("Stop = %v, want ErrDisconnected", err)
	}
}

func TestNameOwnerChangeOfAnotherSignalAllocatesNothing(t *testing.T) {
	name := watchedName
	others := []*dbus.Signal{
		{Sender: "org.freedesktop.DBus", Name: "org.freedesktop.DBus.NameOwnerChanged", Body: []any{otherIface, "", ":1.2"}},
		{Sender: ":1.3", Name: "org.freedesktop.DBus.NameOwnerChanged", Body: []any{watchedName, "", ":1.2"}},
		{Sender: "org.freedesktop.DBus", Name: "org.freedesktop.DBus.NameAcquired", Body: []any{watchedName}},
		{Sender: ":1.3", Path: watchedPath, Name: tickSignal.Name(), Body: []any{uint32(1)}},
	}
	allocs := testing.AllocsPerRun(100, func() {
		for _, sig := range others {
			if _, _, ok := dbusx.NameOwnerChange(sig, name); ok {
				t.Error("a signal that is not the name's owner change was read as one")
			}
		}
	})
	if allocs != 0 {
		t.Errorf("NameOwnerChange allocated %v times telling four other signals apart, want none: it runs for every signal of a connection", allocs)
	}
}

func TestDispatchOfASignalItDoesNotDeliverAllocatesNothing(t *testing.T) {
	const sender = ":1.7"
	routes := []dbusx.Route{
		dbusx.On(tickSignal, watchedPath, func(counted, dbus.Sequence) error { return nil }),
		dbusx.On(tockSignal, watchedPath, func(counted, dbus.Sequence) error { return nil }),
		dbusx.On(aboutSignal, watchedPath, func(about, dbus.Sequence) error { return nil }).WithArg0("mine"),
	}
	others := []*dbus.Signal{
		{Sender: sender, Path: otherPath, Name: tickSignal.Name(), Body: []any{uint32(1)}},
		{Sender: sender, Path: watchedPath, Name: watchedIface + ".Unrouted", Body: []any{uint32(1)}},
		{Sender: sender, Path: watchedPath, Name: aboutSignal.Name(), Body: []any{"theirs", uint32(1)}},
		{Sender: ":1.9", Path: watchedPath, Name: tickSignal.Name(), Body: []any{uint32(1)}},
	}
	allocs := testing.AllocsPerRun(100, func() {
		for _, sig := range others {
			if err := dbusx.Dispatch(sig, sender, routes); err != nil {
				t.Error(err)
			}
		}
	})
	if allocs != 0 {
		t.Errorf("Dispatch allocated %v times dropping four signals, want none: a watch sees every signal of its connection", allocs)
	}
}
