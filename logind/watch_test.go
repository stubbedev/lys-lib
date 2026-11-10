package logind_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/logind"
	"github.com/stubbedev/lys-lib/logind/logindtest"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

type received struct {
	events chan logind.Event
	errs   chan error
}

func watch(t *testing.T, s logind.Session) (*logind.Watch, received) {
	t.Helper()
	r := received{events: make(chan logind.Event, 32), errs: make(chan error, 32)}
	w, err := s.Watch(t.Context(), func(e logind.Event, err error) {
		if err != nil {
			r.errs <- err
			return
		}
		r.events <- e
	})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	t.Cleanup(func() { _ = w.Stop() })
	return w, r
}

func (r received) next(t *testing.T) logind.Event {
	t.Helper()
	select {
	case e := <-r.events:
		return e
	case err := <-r.errs:
		t.Fatalf("the watch failed to decode a signal: %v", err)
	case <-time.After(wait):
		t.Fatal("no event within " + wait.String())
	}
	return nil
}

func TestWatchDecodesEverySignalOfTheSession(t *testing.T) {
	t.Parallel()
	fake, m := manager(t, logindtest.Config{Own: own, Display: display})
	s := session(t, m, own)
	_, r := watch(t, s)
	dev := logind.Device{Major: 13, Minor: 64}

	fake.Lock(t, display)
	fake.Lock(t, own)
	fake.Unlock(t, own)
	fake.PauseDevice(t, own, dev, logind.PausePause)
	fake.ResumeDevice(t, own, dev)
	fake.SetActive(t, own, false)
	fake.SetLockedHint(t, own, true)
	fake.PrepareForSleep(t, true)

	if e := r.next(t); e != (logind.Locked{}) {
		t.Fatalf("first event %#v, want the own session's Lock (another session's leaked?)", e)
	}
	if e := r.next(t); e != (logind.Unlocked{}) {
		t.Fatalf("event %#v, want Unlock", e)
	}
	if e := r.next(t); e != (logind.DevicePaused{Major: 13, Minor: 64, Kind: logind.PausePause}) {
		t.Fatalf("event %#v, want PauseDevice", e)
	}
	resumed, ok := r.next(t).(logind.DeviceResumed)
	if !ok || resumed.Device() != dev || resumed.FD < 0 {
		t.Fatalf("event %#v, want ResumeDevice of %v with a descriptor", resumed, dev)
	}
	if err := unix.Close(int(resumed.FD)); err != nil {
		t.Errorf("ResumeDevice's descriptor: %v", err)
	}
	if e := r.next(t); e != (logind.ActiveChanged{Active: false}) {
		t.Fatalf("event %#v, want Active turning false", e)
	}
	if e := r.next(t); e != (logind.LockedHintChanged{Locked: true}) {
		t.Fatalf("event %#v, want LockedHint turning true", e)
	}
	if e := r.next(t); e != (logind.Sleep{Start: true}) {
		t.Fatalf("event %#v, want PrepareForSleep(true)", e)
	}
}

func TestWatchIgnoresSignalsNotFromLogind(t *testing.T) {
	t.Parallel()
	fake, _ := manager(t, logindtest.Config{Own: own})
	conn := fake.Bus.Conn(t)
	s, err := logind.NewManager(conn).GetSession(t.Context(), own)
	if err != nil {
		t.Fatal(err)
	}
	broad := []dbus.MatchOption{dbus.WithMatchObjectPath(s.Path())}
	if err := conn.AddMatchSignal(broad...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.RemoveMatchSignal(broad...) })
	_, r := watch(t, s)

	forger := fake.Bus.Conn(t)
	fds := make([]int, 2)
	if err := unix.Pipe2(fds, unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fds[0]) })
	if err := errors.Join(
		logind.Lock.Emit(forger, s.Path(), logind.Locked{}),
		logind.ResumeDevice.Emit(forger, s.Path(), logind.DeviceResumed{Major: 1, Minor: 3, FD: dbus.UnixFD(fds[1])}),
		unix.Close(fds[1]),
	); err != nil {
		t.Fatal(err)
	}
	fake.Unlock(t, own)
	if e := r.next(t); e != (logind.Unlocked{}) {
		t.Fatalf("event %#v, want logind's Unlock and nothing forged before it", e)
	}
	for deadline := time.Now().Add(wait); ; time.Sleep(5 * time.Millisecond) {
		n, err := unix.Read(fds[0], make([]byte, 1))
		if n == 0 && err == nil {
			break
		}
		if !errors.Is(err, unix.EAGAIN) || time.Now().After(deadline) {
			t.Fatalf("read the forged ResumeDevice's pipe = %d, %v; want end of file, its descriptor closed", n, err)
		}
	}
	select {
	case e := <-r.events:
		t.Errorf("a forged signal was reported: %#v", e)
	case err := <-r.errs:
		t.Errorf("a forged signal was decoded: %v", err)
	default:
	}
}

func TestStoppingAWatchLeavesTheConnectionAsItWas(t *testing.T) {
	t.Parallel()
	fake, _ := manager(t, logindtest.Config{Own: own})
	conn := fake.Bus.Conn(t)
	s, err := logind.NewManager(conn).GetSession(t.Context(), own)
	if err != nil {
		t.Fatal(err)
	}
	before := dbustest.MatchRules(t, conn)
	w, err := s.Watch(t.Context(), func(logind.Event, error) {})
	if err != nil {
		t.Fatal(err)
	}
	if during := dbustest.MatchRules(t, conn); len(during) != len(before)+3 {
		t.Errorf("match rules during the watch %v, want 3 more than %v", during, before)
	}
	if err := w.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := w.Stop(); err != nil {
		t.Errorf("Stop again: %v", err)
	}
	if after := dbustest.MatchRules(t, conn); !slices.Equal(after, before) {
		t.Errorf("match rules after the watch %v, want %v", after, before)
	}
	select {
	case <-w.Done():
	default:
		t.Error("Done is open after Stop")
	}
}

func TestAWatchEndsWithItsContext(t *testing.T) {
	t.Parallel()
	_, m := manager(t, logindtest.Config{Own: own})
	s := session(t, m, own)
	ctx, cancel := context.WithCancel(t.Context())
	w, err := s.Watch(ctx, func(logind.Event, error) {})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-w.Done():
	case <-time.After(wait):
		t.Fatal("the watch outlived its context")
	}
	if err := w.Stop(); err != nil {
		t.Errorf("Stop after the context ended = %v", err)
	}
}

func TestAWatchEndsWithItsConnection(t *testing.T) {
	t.Parallel()
	fake, _ := manager(t, logindtest.Config{Own: own})
	conn := fake.Bus.Conn(t)
	s, err := logind.NewManager(conn).GetSession(t.Context(), own)
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Watch(t.Context(), func(logind.Event, error) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Done():
	case <-time.After(wait):
		t.Fatal("the watch outlived its connection")
	}
	if err := w.Stop(); !errors.Is(err, logind.ErrDisconnected) {
		t.Errorf("Stop = %v, want ErrDisconnected", err)
	}
}

func TestWaitLocked(t *testing.T) {
	t.Parallel()
	fake, m := manager(t, logindtest.Config{Display: display})
	s := session(t, m, logind.AutoSession)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.WaitLocked(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("WaitLocked with its context ended = %v", err)
	}

	fake.OnLock.Store(func() { fake.SetLockedHint(t, display, true) })
	done := make(chan error, 1)
	go func() { done <- s.WaitLocked(t.Context()) }()
	if err := s.Lock(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitLocked = %v", err)
		}
	case <-time.After(wait):
		t.Fatal("WaitLocked missed LockedHint turning true")
	}
	if err := s.WaitLocked(t.Context()); err != nil {
		t.Errorf("WaitLocked on a locked session = %v", err)
	}
}

func TestPrepareForSleepFollowsSuspend(t *testing.T) {
	t.Parallel()
	fake, m := manager(t, logindtest.Config{Display: display})
	conn := fake.Bus.Conn(t)
	if err := conn.AddMatchSignal(logind.PrepareForSleepMatch()...); err != nil {
		t.Fatal(err)
	}
	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)
	t.Cleanup(func() { conn.RemoveSignal(signals) })
	if err := m.Power(t.Context(), logind.Suspend); err != nil {
		t.Fatal(err)
	}
	fake.PrepareForSleep(t, false)
	for _, want := range []bool{true, false} {
		select {
		case sig := <-signals:
			got, err := logind.PrepareForSleep.Decode(sig)
			if err != nil || got.Start != want {
				t.Fatalf("PrepareForSleep = %+v, %v; want start %v", got, err, want)
			}
		case <-time.After(wait):
			t.Fatalf("no PrepareForSleep(%v)", want)
		}
	}
	if err := m.Power(t.Context(), logind.Suspend); err != nil {
		t.Errorf("Suspend after waking = %v", err)
	}
}
