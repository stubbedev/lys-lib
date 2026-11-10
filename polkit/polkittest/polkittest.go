// Package polkittest is a fake polkitd Authority for tests: it answers
// CheckAuthorization with the result the test sets and records every
// check and cancel, records agent registrations and refuses a second
// agent for a subject, as polkit 127 does, describes the actions it is
// given, keeps temporary authorizations, and counts the calls it
// answers.
package polkittest

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	"github.com/stubbedev/lys-lib/polkit"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// AlreadyExists is polkitd's refusal of a second agent for a subject
// (polkitbackendinteractiveauthority.c).
const AlreadyExists = "An authentication agent already exists for the given subject"

// ExecAction is pkexec's action as polkit 127 registers it, for a test
// that wants a real action described.
var ExecAction = polkit.ActionDescription{
	ActionID:         "org.freedesktop.policykit.exec",
	Description:      "Run a program as another user",
	Message:          "Authentication is required to run a program as another user",
	VendorName:       "The polkit project",
	VendorURL:        "https://github.com/polkit-org/polkit/",
	IconName:         "",
	ImplicitAny:      2,
	ImplicitInactive: 2,
	ImplicitActive:   2,
	Annotations:      map[string]string{},
}

// The answers polkitd gives a check: Challenge is what it answers when
// authentication is needed and no agent could ask for it.
var (
	Authorized = polkit.AuthorizationResult{IsAuthorized: true, Details: map[string]string{}}
	Denied     = polkit.AuthorizationResult{Details: map[string]string{}}
	Challenge  = polkit.AuthorizationResult{IsChallenge: true, Details: map[string]string{}}
	Dismissed  = polkit.AuthorizationResult{Details: map[string]string{polkit.DismissedDetail: "true"}}
)

// Event is one registration change the Authority saw.
type Event struct {
	Register bool
	Sender   string
	Subject  string
	Locale   string
	Path     string
}

// Check is one CheckAuthorization the Authority received. PIDFD is the
// pid the subject's pidfd names, 0 for a subject that carried none.
type Check struct {
	Subject        polkit.Subject
	PIDFD          int
	Action         polkit.Action
	Details        map[string]string
	Flags          polkit.CheckFlags
	CancellationID string
}

// Authority is the fake. Events receives every successful Register and
// Unregister, in order. Actions are what EnumerateActions describes,
// unless FailActions makes it fail; both are set before Serve. Checks
// lists every CheckAuthorization and Cancelled the id of every
// CancelCheckAuthorization, in order.
type Authority struct {
	Events      chan Event
	Actions     []polkit.ActionDescription
	FailActions bool
	Checks      dbustest.Var[[]Check]
	Cancelled   dbustest.Var[[]string]

	mu        sync.Mutex
	conn      *dbus.Conn
	agents    map[string]string
	calls     map[string]int
	temporary []polkit.TemporaryAuthorization
	result    polkit.AuthorizationResult
	holding   bool
	held      map[string]chan struct{}
	ended     chan struct{}
	end       sync.Once
}

// New makes an Authority whose Events holds up to 16 unread events and
// which denies every check until Answer says otherwise.
func New() *Authority {
	return &Authority{
		Events: make(chan Event, 16),
		agents: map[string]string{},
		calls:  map[string]int{},
		result: Denied,
		held:   map[string]chan struct{}{},
		ended:  make(chan struct{}),
	}
}

// Start serves an Authority answering checks with r on a private bus
// that it makes the test's system bus, for code that dials the system
// bus itself.
func Start(t testing.TB, r polkit.AuthorizationResult) *Authority {
	t.Helper()
	bus := dbustest.Start(t)
	bus.UseAsSystemBus(t)
	a := New()
	a.Answer(r)
	t.Cleanup(a.Serve(t, bus.Conn(t)))
	return a
}

// StartHolding is Start for an Authority that answers no check: each
// waits until it is cancelled, as a person who never answers the
// dialog makes polkitd wait, or until the test ends.
func StartHolding(t testing.TB) *Authority {
	t.Helper()
	a := Start(t, Denied)
	a.mu.Lock()
	a.holding = true
	a.mu.Unlock()
	return a
}

// Serve exports the Authority on conn and takes org.freedesktop.PolicyKit1;
// the returned release drops the name. Checks it holds end with the
// test.
func (a *Authority) Serve(t testing.TB, conn *dbus.Conn) (release func()) {
	t.Helper()
	release, err := dbusx.Serve(conn, dbusx.Service{
		Name: polkit.BusName, Path: polkit.AuthorityPath, Interface: polkit.AuthorityInterface,
		Methods: authority{a}, Signals: []dbusx.SignalDecl{polkit.Changed},
	})
	if err != nil {
		t.Fatalf("polkittest: %v", err)
	}
	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()
	t.Cleanup(func() { a.end.Do(func() { close(a.ended) }) })
	return release
}

// Answer sets the result of every later check.
func (a *Authority) Answer(r polkit.AuthorizationResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.result = r
}

// Preregister records an agent for subject, as if another process had
// registered it first.
func (a *Authority) Preregister(subject polkit.Subject) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.agents[subject.String()] = "another agent"
}

// Calls counts the calls of method, such as EnumerateActions, the
// Authority has received.
func (a *Authority) Calls(method string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls[method]
}

func (a *Authority) count(method string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls[method]++
}

// EmitChanged sends the Authority's Changed signal.
func (a *Authority) EmitChanged() error {
	a.mu.Lock()
	conn := a.conn
	a.mu.Unlock()
	return polkit.Changed.Emit(conn, polkit.AuthorityPath, polkit.AuthorityChanged{})
}

// Wire is subject as it travels, for the TemporaryAuthorization a test
// gives SetTemporary.
func Wire(t testing.TB, subject polkit.Subject) polkit.WireSubject {
	t.Helper()
	w, err := subject.Wire()
	if err != nil {
		t.Fatalf("polkittest: %v", err)
	}
	return w
}

// SetTemporary replaces the temporary authorizations the Authority
// retains.
func (a *Authority) SetTemporary(list []polkit.TemporaryAuthorization) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.temporary = list
}

func failed(format string, args ...any) *dbus.Error {
	return dbus.NewError(polkit.ErrFailed, []any{fmt.Sprintf(format, args...)})
}

// authority holds the exported methods, so Authority's own methods stay
// off the bus.
type authority struct{ a *Authority }

// received reads a subject off the bus, closing the pidfd it carries
// after noting the process that names.
func received(w polkit.WireSubject) (subject polkit.Subject, pidfd int, derr *dbus.Error) {
	if fd, carried := w.PIDFD(); carried {
		pid, err := pidOf(int(fd))
		if err = errors.Join(err, unix.Close(int(fd))); err != nil {
			return nil, 0, failed("Error getting subject: %v", err)
		}
		pidfd = pid
	}
	subject, err := w.Subject()
	if err != nil {
		return nil, 0, failed("Error getting subject: %v", err)
	}
	return subject, pidfd, nil
}

func pidOf(pidfd int) (int, error) {
	path := "/proc/self/fdinfo/" + strconv.Itoa(pidfd)
	info, err := os.ReadFile(path) //nolint:gosec // procfs of a descriptor the bus handed over
	if err != nil {
		return 0, fmt.Errorf("the subject's pidfd: %w", err)
	}
	for line := range strings.SplitSeq(string(info), "\n") {
		if v, ok := strings.CutPrefix(line, "Pid:"); ok {
			pid, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return 0, fmt.Errorf("%s: %q: %w", path, line, err)
			}
			return pid, nil
		}
	}
	return 0, fmt.Errorf("the subject's pidfd is not a pidfd: %s has no Pid", path)
}

func (x authority) CheckAuthorization(w polkit.WireSubject, action string, details map[string]string, flags uint32, id string) (polkit.AuthorizationResult, *dbus.Error) {
	x.a.count("CheckAuthorization")
	subject, pidfd, derr := received(w)
	if derr != nil {
		return polkit.AuthorizationResult{}, derr
	}
	x.a.Checks.Update(func(c []Check) []Check {
		return append(c, Check{Subject: subject, PIDFD: pidfd, Action: polkit.Action(action), Details: details, Flags: polkit.CheckFlags(flags), CancellationID: id})
	})
	x.a.mu.Lock()
	result, holding := x.a.result, x.a.holding
	cancelled := make(chan struct{})
	if holding {
		x.a.held[id] = cancelled
	}
	x.a.mu.Unlock()
	if !holding {
		return result, nil
	}
	select {
	case <-cancelled:
	case <-x.a.ended:
	}
	return polkit.AuthorizationResult{}, dbus.NewError(polkit.ErrCancelled, []any{"Cancelled checking authorization"})
}

func (x authority) CancelCheckAuthorization(id string) *dbus.Error {
	x.a.count("CancelCheckAuthorization")
	x.a.Cancelled.Update(func(c []string) []string { return append(c, id) })
	x.a.mu.Lock()
	cancelled, ok := x.a.held[id]
	delete(x.a.held, id)
	x.a.mu.Unlock()
	if !ok {
		return failed("No such cancellation_id `%s' for name", id)
	}
	close(cancelled)
	return nil
}

func (x authority) RegisterAuthenticationAgent(sender dbus.Sender, w polkit.WireSubject, locale, path string) *dbus.Error {
	x.a.count("RegisterAuthenticationAgent")
	subject, _, derr := received(w)
	if derr != nil {
		return derr
	}
	key := subject.String()
	x.a.mu.Lock()
	defer x.a.mu.Unlock()
	if _, ok := x.a.agents[key]; ok {
		return failed("%s", AlreadyExists)
	}
	x.a.agents[key] = string(sender)
	x.a.Events <- Event{Register: true, Sender: string(sender), Subject: key, Locale: locale, Path: path}
	return nil
}

func (x authority) UnregisterAuthenticationAgent(sender dbus.Sender, w polkit.WireSubject, path string) *dbus.Error {
	x.a.count("UnregisterAuthenticationAgent")
	subject, _, derr := received(w)
	if derr != nil {
		return derr
	}
	key := subject.String()
	x.a.mu.Lock()
	defer x.a.mu.Unlock()
	if owner, ok := x.a.agents[key]; !ok || owner != string(sender) {
		return failed("No such agent registered: %s from %s", key, sender)
	}
	delete(x.a.agents, key)
	x.a.Events <- Event{Sender: string(sender), Subject: key, Path: path}
	return nil
}

func (x authority) EnumerateActions(string) ([]polkit.ActionDescription, *dbus.Error) {
	x.a.count("EnumerateActions")
	if x.a.FailActions {
		return nil, failed("the fake polkitd refuses to enumerate actions")
	}
	return x.a.Actions, nil
}

func (x authority) EnumerateTemporaryAuthorizations(w polkit.WireSubject) ([]polkit.TemporaryAuthorization, *dbus.Error) {
	x.a.count("EnumerateTemporaryAuthorizations")
	if _, _, derr := received(w); derr != nil {
		return nil, derr
	}
	x.a.mu.Lock()
	defer x.a.mu.Unlock()
	return append([]polkit.TemporaryAuthorization{}, x.a.temporary...), nil
}

func (x authority) RevokeTemporaryAuthorizations(w polkit.WireSubject) *dbus.Error {
	x.a.count("RevokeTemporaryAuthorizations")
	if _, _, derr := received(w); derr != nil {
		return derr
	}
	x.a.SetTemporary(nil)
	return x.changed()
}

func (x authority) RevokeTemporaryAuthorizationById(id string) *dbus.Error {
	x.a.count("RevokeTemporaryAuthorizationById")
	x.a.mu.Lock()
	i := slices.IndexFunc(x.a.temporary, func(t polkit.TemporaryAuthorization) bool { return t.ID == id })
	if i >= 0 {
		x.a.temporary = slices.Delete(slices.Clone(x.a.temporary), i, i+1)
	}
	x.a.mu.Unlock()
	if i < 0 {
		return failed("No such authorization with id `%s'", id)
	}
	return x.changed()
}

func (x authority) changed() *dbus.Error {
	if err := x.a.EmitChanged(); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}
