package logind_test

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/logind"
	"github.com/stubbedev/lys-lib/logind/logindtest"
)

func TestWatchesOfASessionOnOneConnectionEachOwnTheResumedDevice(t *testing.T) {
	t.Parallel()
	fake, _ := manager(t, logindtest.Config{Own: own})
	conn := fake.Bus.Conn(t)
	s, err := logind.NewManager(conn).GetSession(t.Context(), own)
	if err != nil {
		t.Fatal(err)
	}
	_, first := watch(t, s)
	_, second := watch(t, s)
	waited := make(chan error, 1)
	go func() { waited <- s.WaitLocked(t.Context()) }()

	dev := logind.Device{Major: 226, Minor: 0}
	fake.ResumeDevice(t, own, dev)
	fake.SetLockedHint(t, own, true)
	var fds []int
	for _, r := range []received{first, second} {
		resumed, ok := r.next(t).(logind.DeviceResumed)
		if !ok || resumed.Device() != dev || resumed.FD < 0 {
			t.Fatalf("event %#v, want ResumeDevice of %v with a descriptor", resumed, dev)
		}
		fds = append(fds, int(resumed.FD))
		if e := r.next(t); e != (logind.LockedHintChanged{Locked: true}) {
			t.Fatalf("event %#v, want LockedHint turning true", e)
		}
	}
	if fds[0] == fds[1] {
		t.Fatalf("both watches were handed descriptor %d", fds[0])
	}
	var null unix.Stat_t
	if err := unix.Stat("/dev/null", &null); err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil || st.Rdev != null.Rdev {
			t.Errorf("descriptor %d: device %#x, %v; want the resumed device, open", fd, st.Rdev, err)
		}
	}
	select {
	case err := <-waited:
		if err != nil {
			t.Errorf("WaitLocked beside the watches = %v", err)
		}
	case <-time.After(wait):
		t.Error("WaitLocked beside the watches did not return")
	}
	for _, fd := range fds {
		if err := unix.Close(fd); err != nil {
			t.Errorf("close descriptor %d: %v: another watch, or WaitLocked, closed it", fd, err)
		}
	}
}

func TestAWatchClosesTheDescriptorsInPropertiesItDoesNotRead(t *testing.T) {
	t.Parallel()
	fake, m := manager(t, logindtest.Config{Own: own})
	s := session(t, m, own)
	_, r := watch(t, s)
	pipe := dbustest.NewPipe(t)
	fake.AnnounceProperty(t, own, "Pipe", pipe.Write)
	fake.SetActive(t, own, false)
	if e := r.next(t); e != (logind.ActiveChanged{Active: false}) {
		t.Fatalf("event %#v, want Active turning false and nothing for the property the watch does not read", e)
	}
	pipe.RequireEveryCopyClosed(t)
}
