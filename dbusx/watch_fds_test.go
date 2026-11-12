package dbusx_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

func openDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries) - 1
}

func inodeOf(t *testing.T, fd dbus.UnixFD) uint64 {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Fstat(int(fd), &st); err != nil {
		t.Fatalf("descriptor %d is not open: %v", fd, err)
	}
	return st.Ino
}

func TestWatchesOfOneSignalEachOwnItsDescriptor(t *testing.T) {
	w := startWatched(t)
	takers := []chan dbus.UnixFD{make(chan dbus.UnixFD, 1), make(chan dbus.UnixFD, 1)}
	failed := func(err error) { t.Errorf("a watch failed: %v", err) }
	for _, took := range takers {
		watch, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), failed,
			dbusx.On(handSignal, watchedPath, func(h handed, _ dbus.Sequence) error {
				took <- h.FD
				return nil
			}))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = watch.Stop() })
	}
	ticked := make(chan uint32, 1)
	bystander, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), failed,
		dbusx.On(tickSignal, watchedPath, func(c counted, _ dbus.Sequence) error {
			ticked <- c.N
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bystander.Stop() })

	pipe := dbustest.NewPipe(t)
	write := pipe.Write
	inode := inodeOf(t, write)
	before := openDescriptors(t)
	if err := handSignal.Emit(w.service, watchedPath, handed{FD: write}); err != nil {
		t.Fatal(err)
	}
	if err := tickSignal.Emit(w.service, watchedPath, counted{N: 1}); err != nil {
		t.Fatal(err)
	}
	first, second := awaitWatch(t, takers[0], "descriptor for the first watch"), awaitWatch(t, takers[1], "descriptor for the second watch")
	awaitWatch(t, ticked, "Tick behind the Hand, by which the third watch has dropped its copy")
	if first == second || first == write || second == write || first < 0 || second < 0 {
		t.Fatalf("the watches were handed descriptors %d and %d of the sender's %d, want one of its own each", first, second, write)
	}
	for _, fd := range []dbus.UnixFD{first, second} {
		if got := inodeOf(t, fd); got != inode {
			t.Errorf("descriptor %d is inode %d, want the pipe's %d", fd, got, inode)
		}
	}
	if got := openDescriptors(t); got != before+2 {
		t.Errorf("%d descriptors open with the signal handled, want the %d before and one per watch that routes it", got, before)
	}
	for _, fd := range []dbus.UnixFD{first, second} {
		if err := unix.Close(int(fd)); err != nil {
			t.Errorf("close descriptor %d: %v", fd, err)
		}
	}
	if got := openDescriptors(t); got != before {
		t.Errorf("%d descriptors open once both watches closed their own, want the %d before", got, before)
	}
	pipe.RequireEveryCopyClosed(t)
}

func TestAWatchThatEndsClosesTheDescriptorsItNeverRead(t *testing.T) {
	w := startWatched(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var mu sync.Mutex
	var took []dbus.UnixFD
	watch, err := dbusx.WatchSignals(ctx, w.client, dbusx.FromOwner(watchedName), func(err error) { t.Errorf("the watch failed: %v", err) },
		dbusx.On(handSignal, watchedPath, func(h handed, _ dbus.Sequence) error {
			mu.Lock()
			took = append(took, h.FD)
			mu.Unlock()
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	pipe := dbustest.NewPipe(t)
	write := pipe.Write
	before := openDescriptors(t)
	const sent = 8
	for range sent {
		if err := handSignal.Emit(w.service, watchedPath, handed{FD: write}); err != nil {
			t.Fatal(err)
		}
	}
	awaitWatch(t, entered, "first Hand in its handler")
	arrived := make(chan uint32, 1)
	marker, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), func(error) {},
		dbusx.On(tickSignal, watchedPath, func(c counted, _ dbus.Sequence) error {
			arrived <- c.N
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = marker.Stop() })
	if err := tickSignal.Emit(w.service, watchedPath, counted{N: 1}); err != nil {
		t.Fatal(err)
	}
	awaitWatch(t, arrived, "Tick behind every Hand, by which the connection has read them all")
	cancel()
	close(release)
	if err := watch.Stop(); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(took) == 0 || len(took) > sent {
		t.Fatalf("the handler took %d descriptors of %d sent", len(took), sent)
	}
	if got := openDescriptors(t); got != before+len(took) {
		t.Errorf("%d descriptors open after the watch ended, want the %d before and the %d its handler took: the unread ones leaked", got, before, len(took))
	}
	for _, fd := range took {
		if err := unix.Close(int(fd)); err != nil {
			t.Errorf("close descriptor %d: %v", fd, err)
		}
	}
	pipe.RequireEveryCopyClosed(t)
}

func TestAWatchClosesTheDescriptorsInArgumentsItsSignalDoesNotDeclare(t *testing.T) {
	w := startWatched(t)
	ticked := make(chan uint32, 2)
	watch, err := dbusx.WatchSignals(t.Context(), w.client, dbusx.FromOwner(watchedName), func(err error) { t.Errorf("the watch failed: %v", err) },
		dbusx.On(tickSignal, watchedPath, func(c counted, _ dbus.Sequence) error {
			ticked <- c.N
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Stop() })

	pipe := dbustest.NewPipe(t)
	write := pipe.Write
	before := openDescriptors(t)
	if err := w.service.Emit(watchedPath, tickSignal.Name(), uint32(1), write, []dbus.UnixFD{write}); err != nil {
		t.Fatal(err)
	}
	if err := tickSignal.Emit(w.service, watchedPath, counted{N: 2}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint32{1, 2} {
		if got := awaitWatch(t, ticked, "Tick, the first with two arguments appended"); got != want {
			t.Fatalf("Tick %d, want %d", got, want)
		}
	}
	if got := openDescriptors(t); got != before {
		t.Errorf("%d descriptors open with the Tick handled, want the %d before: those in the arguments Tick does not declare belong to nobody", got, before)
	}
	pipe.RequireEveryCopyClosed(t)
}
