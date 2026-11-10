package polkit_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	"github.com/stubbedev/lys-lib/peercred"
	"github.com/stubbedev/lys-lib/peercred/peercredtest"
	"github.com/stubbedev/lys-lib/polkit"
	"github.com/stubbedev/lys-lib/polkit/polkittest"
	"github.com/stubbedev/lys-lib/sockdir"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const (
	action    polkit.Action = "dev.stubbe.test.action"
	agentPath               = dbus.ObjectPath("/dev/stubbe/test/AuthenticationAgent")
	wait                    = 5 * time.Second
)

var someone = polkit.UnixProcess{PID: 4242, StartTime: 987654, UID: 1000}

func dial(t *testing.T) polkit.Authority {
	t.Helper()
	conn, err := polkit.Dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return polkit.NewAuthority(conn)
}

func await[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(wait):
		t.Fatalf("no %s within %s", what, wait)
		var zero T
		return zero
	}
}

func TestCheckAuthorizationAsksForTheSubjectAndReadsTheAnswer(t *testing.T) {
	fake := polkittest.Start(t, polkittest.Authorized)
	authority := dial(t)
	for _, c := range []struct {
		answer polkit.AuthorizationResult
		want   polkit.Verdict
	}{
		{polkittest.Authorized, polkit.Authorized},
		{polkittest.Denied, polkit.Denied},
		{polkittest.Dismissed, polkit.Dismissed},
		{polkittest.Challenge, polkit.Challenge},
	} {
		fake.Answer(c.answer)
		r, err := authority.CheckAuthorization(t.Context(), someone, action, polkit.AllowUserInteraction)
		if err != nil || r.Verdict() != c.want {
			t.Errorf("CheckAuthorization = %+v, %v; want %s", r, err, c.want)
		}
	}
	checks := fake.Checks.Load()
	if len(checks) != 4 {
		t.Fatalf("polkit saw %d checks, want 4", len(checks))
	}
	ids := make([]string, 0, len(checks))
	for _, c := range checks {
		if c.Subject != someone || c.PIDFD != 0 || c.Action != action || c.Flags != polkit.AllowUserInteraction || len(c.Details) != 0 {
			t.Errorf("check = %+v, want %s for %s with interaction allowed", c, action, someone)
		}
		if slices.Contains(ids, c.CancellationID) || c.CancellationID == "" {
			t.Errorf("cancellation id %q repeats one of %q or is empty", c.CancellationID, ids)
		}
		ids = append(ids, c.CancellationID)
	}
	if cancelled := fake.Cancelled.Load(); len(cancelled) != 0 {
		t.Errorf("answered checks were cancelled: %q", cancelled)
	}
}

func TestCheckAuthorizationCancelsTheCheckItGivesUpOn(t *testing.T) {
	fake := polkittest.StartHolding(t)
	authority := dial(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := authority.CheckAuthorization(ctx, someone, action, polkit.AllowUserInteraction)
		done <- err
	}()
	for deadline := time.Now().Add(wait); len(fake.Checks.Load()) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the check never reached polkit")
		}
	}
	select {
	case err := <-done:
		t.Fatalf("a check polkit holds returned %v", err)
	default:
	}
	cancel()
	err := await(t, done, "return of the check whose context ended")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("CheckAuthorization = %v, want its context's end", err)
	}
	for _, want := range []string{string(action), someone.String()} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	checks, cancelled := fake.Checks.Load(), fake.Cancelled.Load()
	if len(checks) != 1 || !slices.Equal(cancelled, []string{checks[0].CancellationID}) {
		t.Errorf("checks %+v, cancelled %q; want the one check cancelled", checks, cancelled)
	}
}

func TestCheckAuthorizationWithoutPolkitNamesTheActionAndTheSubject(t *testing.T) {
	dbustest.Start(t).UseAsSystemBus(t)
	authority := dial(t)
	_, err := authority.CheckAuthorization(t.Context(), someone, action, polkit.AllowUserInteraction)
	if err == nil {
		t.Fatal("a check without polkitd succeeded")
	}
	if want := "polkit: check " + string(action) + " for " + someone.String() + ": "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error %q, want it to start %q", err, want)
	}
	var zero polkit.Authority
	if _, err := zero.CheckAuthorization(t.Context(), someone, action, 0); err == nil {
		t.Error("a check on a zero Authority succeeded")
	}
	if _, err := authority.CheckAuthorization(t.Context(), polkit.UnixSession{}, action, 0); err == nil {
		t.Error("a check for a session without an id was sent")
	}
}

func TestDialErrorsNamePolkit(t *testing.T) {
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+filepath.Join(sockdir.Dir(t), "absent"))
	conn, err := polkit.Dial(t.Context())
	if err == nil {
		_ = conn.Close()
		t.Fatal("Dial with no system bus connected")
	}
	if want := "polkit: connect to the system bus: "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error %q, want it to start %q", err, want)
	}
}

func TestMain(m *testing.M) { peercredtest.Main(m) }

func peerPIDFD(t *testing.T) (*peercredtest.Peer, *peercred.PIDFD) {
	t.Helper()
	p := peercredtest.Start(t)
	pidfd, err := peercred.PIDFDOf(p.Conn)
	if errors.Is(err, peercred.ErrNoPIDFD) {
		t.Skipf("this kernel predates Linux 6.5: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pidfd.Close() })
	return p, pidfd
}

func TestCheckAuthorizationHandsPolkitThePeersPIDFD(t *testing.T) {
	fake := polkittest.Start(t, polkittest.Authorized)
	authority := dial(t)
	p, pidfd := peerPIDFD(t)
	subject, err := polkit.ProcessOfPIDFD(pidfd)
	if err != nil {
		t.Fatal(err)
	}
	byPID, err := polkit.ProcessOf(pidfd.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	if subject.PIDFD != pidfd || int(subject.PID) != p.PID || subject.UID != uint32(os.Getuid()) || subject.StartTime != byPID.StartTime || subject.StartTime == 0 {
		t.Fatalf("ProcessOfPIDFD = %+v, want the peer %d started at %d with its pidfd", subject, p.PID, byPID.StartTime)
	}
	if _, err := authority.CheckAuthorization(t.Context(), subject, action, polkit.AllowUserInteraction); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CheckAuthorization(t.Context(), byPID, action, polkit.AllowUserInteraction); err != nil {
		t.Fatal(err)
	}
	checks := fake.Checks.Load()
	if len(checks) != 2 {
		t.Fatalf("polkit saw %d checks, want 2", len(checks))
	}
	if checks[0].Subject != byPID || checks[0].PIDFD != p.PID {
		t.Errorf("polkit got %+v with a pidfd of pid %d; want %s with a pidfd of that process", checks[0].Subject, checks[0].PIDFD, byPID)
	}
	if checks[1].Subject != byPID || checks[1].PIDFD != 0 {
		t.Errorf("polkit got %+v with a pidfd of pid %d; want %s and no pidfd", checks[1].Subject, checks[1].PIDFD, byPID)
	}
	if exited, err := pidfd.Exited(); err != nil || exited {
		t.Errorf("the pidfd after the check: exited %v, %v; want it still the caller's and open", exited, err)
	}
}

func TestProcessOfPIDFDRefusesAPeerThatExited(t *testing.T) {
	p, pidfd := peerPIDFD(t)
	p.End()
	for deadline := time.Now().Add(wait); ; time.Sleep(time.Millisecond) {
		exited, err := pidfd.Exited()
		if err != nil {
			t.Fatal(err)
		}
		if exited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the peer did not exit")
		}
	}
	if subject, err := polkit.ProcessOfPIDFD(pidfd); err == nil || !strings.Contains(err.Error(), "has exited") {
		t.Errorf("ProcessOfPIDFD of an exited peer not yet reaped = %+v, %v; want it refused as exited", subject, err)
	}
	p.Reap(t)
	if subject, err := polkit.ProcessOfPIDFD(pidfd); err == nil {
		t.Errorf("ProcessOfPIDFD of a reaped peer = %+v, want an error", subject)
	}
	if err := pidfd.Close(); err != nil {
		t.Fatal(err)
	}
	sent := polkit.UnixProcess{PID: 4242, StartTime: 1, UID: 1000, PIDFD: pidfd}
	if w, err := sent.Wire(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("a subject whose pidfd is closed travels as %+v, %v; want os.ErrClosed", w, err)
	}
}

func TestAnAgentRegistersAndUnregisters(t *testing.T) {
	t.Parallel()
	bus := dbustest.Start(t)
	fake := polkittest.New()
	fake.Serve(t, bus.Conn(t))
	conn := bus.Conn(t)
	authority := polkit.NewAuthority(conn)
	session := polkit.UnixSession{ID: "c2"}

	registered, err := authority.RegisterAuthenticationAgent(t.Context(), session, "da_DK.UTF-8", agentPath)
	if err != nil {
		t.Fatal(err)
	}
	if registered == dbus.NoSequence {
		t.Error("RegisterAuthenticationAgent gave no sequence for polkitd's reply")
	}
	want := polkittest.Event{Register: true, Sender: conn.Names()[0], Subject: "unix-session c2", Locale: "da_DK.UTF-8", Path: string(agentPath)}
	if got := await(t, fake.Events, "registration"); got != want {
		t.Errorf("registered %+v, want %+v", got, want)
	}

	_, err = authority.RegisterAuthenticationAgent(t.Context(), session, "C", agentPath)
	var derr dbus.Error
	if !errors.As(err, &derr) || derr.Name != polkit.ErrFailed || !strings.Contains(err.Error(), polkittest.AlreadyExists) {
		t.Fatalf("a second registration = %v, want polkit's %q", err, polkittest.AlreadyExists)
	}
	if want := "polkit: register the agent at " + string(agentPath) + " for unix-session c2: "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error %q, want it to start %q", err, want)
	}

	if err := authority.UnregisterAuthenticationAgent(t.Context(), session, agentPath); err != nil {
		t.Fatal(err)
	}
	want = polkittest.Event{Sender: conn.Names()[0], Subject: "unix-session c2", Path: string(agentPath)}
	if got := await(t, fake.Events, "unregistration"); got != want {
		t.Errorf("unregistered %+v, want %+v", got, want)
	}
	err = authority.UnregisterAuthenticationAgent(t.Context(), session, agentPath)
	if !errors.As(err, &derr) || derr.Name != polkit.ErrFailed || !strings.Contains(err.Error(), "unregister the agent at "+string(agentPath)+" for unix-session c2") {
		t.Errorf("unregistering twice = %v, want polkit's refusal naming the agent and the subject", err)
	}

	other := polkit.UnixSession{ID: "3"}
	fake.Preregister(other)
	if _, err := authority.RegisterAuthenticationAgent(t.Context(), other, "C", agentPath); err == nil || !strings.Contains(err.Error(), polkittest.AlreadyExists) {
		t.Errorf("registering for a subject another agent has = %v", err)
	}
	if n := fake.Calls("RegisterAuthenticationAgent"); n != 3 {
		t.Errorf("%d RegisterAuthenticationAgent calls counted, want 3", n)
	}
}

func TestActionsAreEnumerated(t *testing.T) {
	t.Parallel()
	bus := dbustest.Start(t)
	fake := polkittest.New()
	fake.Actions = []polkit.ActionDescription{polkittest.ExecAction}
	fake.Serve(t, bus.Conn(t))
	authority := polkit.NewAuthority(bus.Conn(t))
	actions, err := authority.EnumerateActions(t.Context(), "C")
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].ActionID != polkittest.ExecAction.ActionID || actions[0].Message != polkittest.ExecAction.Message || actions[0].ImplicitActive != 2 {
		t.Errorf("EnumerateActions = %+v, want %+v", actions, polkittest.ExecAction)
	}

	failing := polkittest.New()
	failing.FailActions = true
	other := dbustest.Start(t)
	failing.Serve(t, other.Conn(t))
	_, err = polkit.NewAuthority(other.Conn(t)).EnumerateActions(t.Context(), "da_DK.UTF-8")
	var derr dbus.Error
	if !errors.As(err, &derr) || derr.Name != polkit.ErrFailed || !strings.HasPrefix(err.Error(), "polkit: enumerate the actions for locale da_DK.UTF-8: ") {
		t.Errorf("EnumerateActions refused = %v, want polkit's error naming the locale", err)
	}
}

func TestTemporaryAuthorizationsAreListedAndRevoked(t *testing.T) {
	t.Parallel()
	bus := dbustest.Start(t)
	fake := polkittest.New()
	session := polkit.UnixSession{ID: "c2"}
	subject := polkittest.Wire(t, session)
	fake.SetTemporary([]polkit.TemporaryAuthorization{
		{ID: "tmpauthz0", ActionID: "org.example.keep", Subject: subject, Obtained: 1, Expires: 301},
		{ID: "tmpauthz1", ActionID: "org.example.drop", Subject: subject, Obtained: 2, Expires: 302},
	})
	fake.Serve(t, bus.Conn(t))
	conn := bus.Conn(t)
	authority := polkit.NewAuthority(conn)

	changes := make(chan struct{}, 4)
	watch, err := dbusx.WatchSignals(t.Context(), conn, dbusx.FromOwner(polkit.BusName), func(err error) { t.Errorf("Changed: %v", err) },
		dbusx.On(polkit.Changed, polkit.AuthorityPath, func(polkit.AuthorityChanged) error {
			changes <- struct{}{}
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Stop() })

	list := func() []string {
		t.Helper()
		all, err := authority.EnumerateTemporaryAuthorizations(t.Context(), session)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, len(all))
		for i, a := range all {
			if got, err := a.Subject.Subject(); err != nil || got != session {
				t.Errorf("authorization %s is for %+v, %v; want %s", a.ID, got, err, session)
			}
			ids[i] = a.ID
		}
		return ids
	}
	if got := list(); !slices.Equal(got, []string{"tmpauthz0", "tmpauthz1"}) {
		t.Fatalf("temporary authorizations %q", got)
	}
	if err := authority.RevokeTemporaryAuthorizationByID(t.Context(), "tmpauthz1"); err != nil {
		t.Fatal(err)
	}
	await(t, changes, "Changed after revoking one authorization")
	if got := list(); !slices.Equal(got, []string{"tmpauthz0"}) {
		t.Errorf("after revoking tmpauthz1: %q", got)
	}
	err = authority.RevokeTemporaryAuthorizationByID(t.Context(), "tmpauthz1")
	var derr dbus.Error
	if !errors.As(err, &derr) || derr.Name != polkit.ErrFailed || !strings.HasPrefix(err.Error(), "polkit: revoke temporary authorization tmpauthz1: ") {
		t.Errorf("revoking it again = %v, want polkit's error naming it", err)
	}
	if err := authority.RevokeTemporaryAuthorizations(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	await(t, changes, "Changed after revoking the rest")
	if got := list(); len(got) != 0 {
		t.Errorf("after revoking all: %q", got)
	}
}
