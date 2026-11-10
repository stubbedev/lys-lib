package logindtest_test

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/logind"
	"github.com/stubbedev/lys-lib/logind/logindtest"
)

const (
	own  logind.SessionID = "c2"
	wait                  = 5 * time.Second
)

type reports struct {
	testing.TB
	mu     sync.Mutex
	errors []string
	fatal  []string
}

func (r *reports) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *reports) Fatalf(format string, args ...any) {
	r.mu.Lock()
	r.fatal = append(r.fatal, fmt.Sprintf(format, args...))
	r.mu.Unlock()
	runtime.Goexit()
}

func (r *reports) read() (errors, fatal []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.errors), slices.Clone(r.fatal)
}

func lock(t *testing.T, fake *logindtest.Logind) {
	t.Helper()
	s, err := logind.NewManager(fake.Bus.Conn(t)).GetSession(t.Context(), own)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAHookReportsAFailedAnnouncementAndGoesOn(t *testing.T) {
	fake := logindtest.Start(t, logindtest.Config{Own: own})
	r := &reports{TB: t}
	results := make(chan bool, 2)
	fake.OnLock.Store(func() {
		results <- fake.SetLockedHint(r, "absent", true)
		results <- fake.SetLockedHint(r, own, true)
	})
	lock(t, fake)
	for i, want := range []bool{false, true} {
		select {
		case got := <-results:
			if got != want {
				t.Errorf("announcement %d from the hook = %v, want %v", i, got, want)
			}
		case <-time.After(wait):
			errs, fatal := r.read()
			t.Fatalf("the hook did not go on to announcement %d: errors %q, fatal %q", i, errs, fatal)
		}
	}
	errs, fatal := r.read()
	if len(fatal) != 0 {
		t.Errorf("the hook's goroutine was told to stop the test: %q", fatal)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], `no session "absent"`) {
		t.Errorf("reported %q, want the one session that does not exist", errs)
	}
	if got := fake.Events.Load(); !slices.Equal(got, []logindtest.Event{logindtest.EventLock, logindtest.EventLockedHint}) {
		t.Errorf("events %v, want the lock and the hint the hook set", got)
	}
}

func TestEveryAnnouncementReportsWithoutStoppingItsGoroutine(t *testing.T) {
	fake := logindtest.Start(t, logindtest.Config{Own: own})
	dev := logind.Device{Major: 13, Minor: 64}
	announcements := map[string]func(testing.TB, logind.SessionID) bool{
		"SetLockedHint": func(t testing.TB, id logind.SessionID) bool { return fake.SetLockedHint(t, id, true) },
		"SetActive":     func(t testing.TB, id logind.SessionID) bool { return fake.SetActive(t, id, false) },
		"Lock":          fake.Lock,
		"Unlock":        fake.Unlock,
		"PauseDevice": func(t testing.TB, id logind.SessionID) bool {
			return fake.PauseDevice(t, id, dev, logind.PauseGone)
		},
		"ResumeDevice": func(t testing.TB, id logind.SessionID) bool { return fake.ResumeDevice(t, id, dev) },
	}
	for name, announce := range announcements {
		r := &reports{TB: t}
		if !announce(r, own) {
			t.Errorf("%s of the session = false", name)
		}
		if errs, fatal := r.read(); len(errs) != 0 || len(fatal) != 0 {
			t.Errorf("%s of the session reported %q, %q", name, errs, fatal)
		}
		if announce(r, "absent") {
			t.Errorf("%s of a session that does not exist = true", name)
		}
		if errs, fatal := r.read(); len(errs) != 1 || len(fatal) != 0 {
			t.Errorf("%s of a session that does not exist reported errors %q, fatal %q; want one error", name, errs, fatal)
		}
	}

	fake.Bus.Stop(t)
	r := &reports{TB: t}
	for deadline := time.Now().Add(wait); fake.PrepareForSleep(r, true); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("PrepareForSleep still went out after the bus stopped")
		}
	}
	if errs, fatal := r.read(); len(errs) != 1 || len(fatal) != 0 {
		t.Errorf("PrepareForSleep on a stopped bus reported errors %q, fatal %q; want one error", errs, fatal)
	}
}

func TestAHookRunningWhenTheTestEndsFinishesOnALiveBus(t *testing.T) {
	var announced atomic.Bool
	t.Run("ends with its hook running", func(t *testing.T) {
		fake := logindtest.Start(t, logindtest.Config{Own: own})
		ending := make(chan struct{})
		t.Cleanup(func() { close(ending) })
		fake.OnLock.Store(func() {
			<-ending
			announced.Store(fake.SetLockedHint(t, own, true))
		})
		lock(t, fake)
	})
	if !announced.Load() {
		t.Error("the fake did not wait for the hook that was running when its test ended")
	}
}
