package portaldialogs

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/third_party/godbus/introspect"
)

type fakeHost struct {
	yes       bool
	choice    string
	asked     dbustest.Var[[]any]
	caller    dbustest.Var[Caller]
	dismissed dbustest.Var[[]string]
	auth      dbustest.Var[AuthReply]
	// triggers, when set, is BindShortcuts' reply in place of every
	// row kept as offered.
	triggers dbustest.Var[[]ShortcutTrigger]
}

func (f *fakeHost) record(c Caller, args ...any) {
	f.caller.Store(c)
	f.asked.Store(args)
}

func (f *fakeHost) Access(c Caller, r AccessRequest) (bool, []Selection) {
	f.record(c, r)
	if !f.yes {
		return false, nil
	}
	return true, []Selection{{ID: "picked", Value: "x"}}
}

func (f *fakeHost) Account(c Caller, reason string, info AccountInfo) (bool, AccountInfo) {
	f.record(c, reason, info)
	return f.yes, AccountInfo{Name: info.Name + " (edited)", Image: info.Image}
}

func (f *fakeHost) ConfirmWallpaper(c Caller, uri string) bool {
	f.record(c, uri)
	return f.yes
}

func (f *fakeHost) ChooseApplication(c Caller, r ChooserRequest) string {
	f.record(c, r)
	return f.choice
}

func (f *fakeHost) UpdateChoices(handle string, choices []string) bool {
	f.asked.Store([]any{handle, choices})
	return handle == "/r/1"
}

func (f *fakeHost) ConfirmInstall(c Caller, r LauncherRequest) (bool, string) {
	f.record(c, r)
	return f.yes, r.Name + " (edited)"
}

func (f *fakeHost) ChooseDevices(c Caller, devices []Device) (bool, []string) {
	f.record(c, devices)
	if !f.yes {
		return false, nil
	}
	return true, []string{devices[0].ID}
}

func (f *fakeHost) Password(c Caller, r PasswordRequest) (bool, string, []Selection) {
	f.record(c, r)
	if !f.yes {
		return false, "", nil
	}
	return true, "typed", []Selection{{ID: r.Choices[0].ID, Value: "true"}}
}

func (f *fakeHost) BindShortcuts(c Caller, r ShortcutsRequest) (bool, []ShortcutTrigger) {
	f.record(c, r)
	if !f.yes {
		return false, nil
	}
	if set := f.triggers.Load(); set != nil {
		return true, set
	}
	kept := make([]ShortcutTrigger, len(r.Rows))
	for i, row := range r.Rows {
		kept[i] = ShortcutTrigger{ID: row.ID, Trigger: row.Trigger}
	}
	return true, kept
}

func (f *fakeHost) Authenticate(c Caller, r AuthRequest) AuthReply {
	f.record(c, r)
	return f.auth.Load()
}

func (f *fakeHost) ShowAuthentication(c Caller, r AuthRequest) {
	f.record(c, r)
}

func (f *fakeHost) Dismiss(handle string) {
	f.dismissed.Update(func(d []string) []string { return append(d, handle) })
}

func TestRoundTrip(t *testing.T) {
	bus := dbustest.Start(t)
	caller := Caller{Handle: "/r/1", AppID: "org.app", ParentWindow: "wayland:abc", Modal: true}
	for _, yes := range []bool{true, false} {
		host := &fakeHost{yes: yes, choice: map[bool]string{true: "org.app.desktop"}[yes]}
		release, err := NewDaemon(host).Export(bus.Conn(t))
		if err != nil {
			t.Fatal(err)
		}
		c, ctx := NewClient(bus.Conn(t)), context.Background()
		askedBy := func(method string, want any) {
			t.Helper()
			if got := host.caller.Load(); got != caller {
				t.Errorf("%s (yes=%v) asked by %+v, want %+v", method, yes, got, caller)
			}
			if got := host.asked.Load(); !reflect.DeepEqual(got[0], want) {
				t.Errorf("%s (yes=%v) asked %#v, want %#v", method, yes, got[0], want)
			}
		}

		access := AccessRequest{"T", "S", "B", "Allow", "Deny", "camera", []Choice{
			{ID: "c", Label: "C", Options: []Option{{"a", "A"}, {"b", "B"}}, Initial: "b"},
			{ID: "flag", Label: "Flag", Options: []Option{}, Initial: "true"},
		}}
		granted, picked, err := c.Access(ctx, caller, access)
		if err != nil || granted != yes {
			t.Errorf("Access (yes=%v) = %v, %v", yes, granted, err)
		}
		askedBy("Access", access)
		if want := []Selection{{ID: "picked", Value: "x"}}; yes && !slices.Equal(picked, want) || !yes && len(picked) != 0 {
			t.Errorf("Access (yes=%v) picked %v", yes, picked)
		}
		if _, _, err := c.Access(ctx, caller, AccessRequest{Title: "T"}); err != nil {
			t.Errorf("Access without choices: %v", err)
		}
		askedBy("Access", AccessRequest{Title: "T", Choices: []Choice{}})

		info := AccountInfo{Name: "Ada", Image: "/a.png"}
		shared, edited, err := c.Account(ctx, caller, "why", info)
		if err != nil || shared != yes || edited != (AccountInfo{Name: "Ada (edited)", Image: "/a.png"}) {
			t.Errorf("Account (yes=%v) = %v, %+v, %v", yes, shared, edited, err)
		}
		askedBy("Account", "why")
		if got := host.asked.Load()[1]; got != info {
			t.Errorf("Account (yes=%v) offered %+v, want %+v", yes, got, info)
		}

		if ok, err := c.ConfirmWallpaper(ctx, caller, "file:///a.png"); err != nil || ok != yes {
			t.Errorf("ConfirmWallpaper (yes=%v) = %v, %v", yes, ok, err)
		}
		askedBy("ConfirmWallpaper", "file:///a.png")

		chosen, err := c.ChooseApplication(ctx, caller, ChooserRequest{ContentType: "text/plain", Target: "x.txt"})
		if err != nil || chosen != host.choice {
			t.Errorf("ChooseApplication (yes=%v) = %q, %v", yes, chosen, err)
		}
		askedBy("ChooseApplication", ChooserRequest{Choices: []string{}, ContentType: "text/plain", Target: "x.txt"})
		chooser := ChooserRequest{Choices: []string{"a", "b"}, LastChoice: "b"}
		if _, err := c.ChooseApplication(ctx, caller, chooser); err != nil {
			t.Errorf("ChooseApplication (yes=%v): %v", yes, err)
		}
		askedBy("ChooseApplication", chooser)

		if found, err := c.UpdateChoices(ctx, "/r/1", []string{"c"}); err != nil || !found || !reflect.DeepEqual(host.asked.Load(), []any{"/r/1", []string{"c"}}) {
			t.Errorf("UpdateChoices = %v, %v (asked %v)", found, err, host.asked.Load())
		}
		if found, err := c.UpdateChoices(ctx, "/r/none", nil); err != nil || found || !reflect.DeepEqual(host.asked.Load(), []any{"/r/none", []string{}}) {
			t.Errorf("UpdateChoices without a chooser = %v, %v (asked %v)", found, err, host.asked.Load())
		}

		devices := []Device{{ID: "d0", Name: "Pad", Writable: true}, {ID: "d1", Name: "Key"}}
		allowed, ids, err := c.ChooseDevices(ctx, caller, devices)
		if err != nil || allowed != yes {
			t.Errorf("ChooseDevices (yes=%v) = %v, %v", yes, allowed, err)
		}
		askedBy("ChooseDevices", devices)
		if yes && !slices.Equal(ids, []string{"d0"}) || !yes && len(ids) != 0 {
			t.Errorf("ChooseDevices (yes=%v) picked %v", yes, ids)
		}

		launcher := LauncherRequest{Name: "App", Icon: []byte{0x89, 'P'}, Target: "https://example.org", EditableName: true}
		if ok, name, err := c.ConfirmInstall(ctx, caller, launcher); err != nil || ok != yes || name != "App (edited)" {
			t.Errorf("ConfirmInstall (yes=%v) = %v, %q, %v", yes, ok, name, err)
		}
		askedBy("ConfirmInstall", launcher)
		if _, _, err := c.ConfirmInstall(ctx, caller, LauncherRequest{Name: "App"}); err != nil {
			t.Errorf("ConfirmInstall without an icon: %v", err)
		}
		askedBy("ConfirmInstall", LauncherRequest{Name: "App", Icon: []byte{}})

		pw := PasswordRequest{
			Title: "Unlock", Body: "login", Confirm: true, Retry: true, AcceptLabel: "Update", CancelLabel: "Keep",
			Choices: []Choice{{ID: "remember", Label: "Remember", Options: []Option{}}},
		}
		ok, typed, picked, err := c.Password(ctx, caller, pw)
		if err != nil || ok != yes || typed != map[bool]string{true: "typed"}[yes] {
			t.Errorf("Password (yes=%v) = %v, %q, %v", yes, ok, typed, err)
		}
		if want := map[bool][]Selection{true: {{ID: "remember", Value: "true"}}}[yes]; !slices.Equal(picked, want) {
			t.Errorf("Password (yes=%v) picked %v, want %v", yes, picked, want)
		}
		askedBy("Password", pw)

		shortcuts := ShortcutsRequest{Title: "Assign", Note: "note", Rows: []ShortcutRow{
			{ID: "print", Description: "Print", Trigger: "Ctrl+Alt+P"},
			{ID: "talk", Description: "Talk", Trigger: "Super+F9", Refusal: "Super+F9 is bound"},
		}}
		bound, triggers, err := c.BindShortcuts(ctx, caller, shortcuts)
		if err != nil || bound != yes {
			t.Errorf("BindShortcuts (yes=%v) = %v, %v", yes, bound, err)
		}
		askedBy("BindShortcuts", shortcuts)
		if want := map[bool][]ShortcutTrigger{true: {{"print", "Ctrl+Alt+P"}, {"talk", "Super+F9"}}}[yes]; !slices.Equal(triggers, want) {
			t.Errorf("BindShortcuts (yes=%v) triggers %v, want %v", yes, triggers, want)
		}

		auth := AuthRequest{
			Message: "Authentication is needed to run a program as another user", Icon: "dialog-password",
			Details:    []AuthDetail{{"Program", "/usr/bin/true"}, {"Command line", "true --flag"}},
			Identities: []AuthIdentity{{0, "root"}, {1000, "Ada"}},
			Selected:   1000,
			Notes:      []AuthNote{{false, "Place your finger"}, {true, "Sorry, try again."}},
			Prompt:     "Password:",
		}
		for _, want := range []AuthReply{AuthAnswer{Response: NewResponse(map[bool]string{true: "hunter2"}[yes])}, AuthSwitch{UID: 0}, AuthCancel{}} {
			host.auth.Store(want)
			if got, err := c.Authenticate(ctx, caller, auth); err != nil || got != want {
				t.Errorf("Authenticate (yes=%v) = %#v, %v; want %#v", yes, got, err, want)
			}
			askedBy("Authenticate", auth)
		}
		echoed := AuthRequest{Identities: []AuthIdentity{{0, "root"}}, Prompt: "Username:", Echo: true}
		host.auth.Store(AuthAnswer{Response: NewResponse("root")})
		if got, err := c.Authenticate(ctx, caller, echoed); err != nil || got != host.auth.Load() {
			t.Errorf("Authenticate echoed (yes=%v) = %#v, %v", yes, got, err)
		}
		askedBy("Authenticate", AuthRequest{Details: []AuthDetail{}, Identities: echoed.Identities, Notes: []AuthNote{}, Prompt: "Username:", Echo: true})
		for _, bad := range []AuthReply{nil, AuthSwitch{UID: 1000}, AuthSwitch{UID: 42}} {
			host.auth.Store(bad)
			if got, err := c.Authenticate(ctx, caller, auth); err == nil {
				t.Errorf("Authenticate (yes=%v) with the host replying %#v = %#v", yes, bad, got)
			}
		}
		if got, err := c.Authenticate(ctx, caller, AuthRequest{Identities: auth.Identities, Selected: 42}); err == nil {
			t.Errorf("Authenticate for an unknown selected uid = %#v", got)
		}
		if got, err := c.Authenticate(ctx, caller, AuthRequest{}); err == nil {
			t.Errorf("Authenticate without identities = %#v", got)
		}

		shown := auth
		shown.Prompt = ""
		host.asked.Store(nil)
		if err := c.ShowAuthentication(ctx, caller, shown); err != nil {
			t.Errorf("ShowAuthentication (yes=%v): %v", yes, err)
		}
		askedBy("ShowAuthentication", shown)
		host.asked.Store([]any{"untouched"})
		if err := c.ShowAuthentication(ctx, caller, AuthRequest{Identities: auth.Identities, Selected: 42}); err == nil {
			t.Error("ShowAuthentication for an unknown selected uid succeeded")
		}
		if got := host.asked.Load(); !reflect.DeepEqual(got, []any{"untouched"}) {
			t.Errorf("ShowAuthentication for an unknown selected uid reached the host with %#v", got)
		}

		if err := c.Dismiss(ctx, "/r/1"); err != nil || !slices.Equal(host.dismissed.Load(), []string{"/r/1"}) {
			t.Errorf("Dismiss = %v, dismissed %v", err, host.dismissed.Load())
		}
		release()
	}
}

func TestCallsWithoutHostFail(t *testing.T) {
	c, ctx := NewClient(dbustest.Start(t).Conn(t)), context.Background()
	for _, tc := range []struct {
		method string
		call   func() error
	}{
		{"Access", func() error { _, _, err := c.Access(ctx, Caller{}, AccessRequest{}); return err }},
		{"Account", func() error { _, _, err := c.Account(ctx, Caller{}, "", AccountInfo{}); return err }},
		{"ConfirmWallpaper", func() error { _, err := c.ConfirmWallpaper(ctx, Caller{}, ""); return err }},
		{"ChooseApplication", func() error { _, err := c.ChooseApplication(ctx, Caller{}, ChooserRequest{}); return err }},
		{"UpdateChoices", func() error { _, err := c.UpdateChoices(ctx, "", nil); return err }},
		{"ChooseDevices", func() error { _, _, err := c.ChooseDevices(ctx, Caller{}, nil); return err }},
		{"ConfirmInstall", func() error { _, _, err := c.ConfirmInstall(ctx, Caller{}, LauncherRequest{}); return err }},
		{"Password", func() error { _, _, _, err := c.Password(ctx, Caller{}, PasswordRequest{}); return err }},
		{"BindShortcuts", func() error {
			_, _, err := c.BindShortcuts(ctx, Caller{}, ShortcutsRequest{Rows: []ShortcutRow{{ID: "a"}}})
			return err
		}},
		{"Authenticate", func() error { _, err := c.Authenticate(ctx, Caller{}, AuthRequest{}); return err }},
		{"ShowAuthentication", func() error { return c.ShowAuthentication(ctx, Caller{}, AuthRequest{}) }},
		{"Dismiss", func() error { return c.Dismiss(ctx, "") }},
	} {
		if err := tc.call(); err == nil {
			t.Errorf("%s answered without a host", tc.method)
		}
	}
}

func receive(t *testing.T, w *AuthWatch) (AuthAction, bool) {
	t.Helper()
	select {
	case a, open := <-w.Actions():
		return a, open
	case <-time.After(5 * time.Second):
		t.Fatal("no authentication action within 5s")
		return nil, false
	}
}

func ended(t *testing.T, w *AuthWatch) {
	t.Helper()
	if a, open := receive(t, w); open {
		t.Fatalf("watch delivered %#v, want it ended", a)
	}
}

type authFixture struct {
	bus     *dbustest.Bus
	conn    *dbus.Conn
	emitter *Emitter
	client  *Client
}

func startAuth(t *testing.T) authFixture {
	t.Helper()
	bus := dbustest.Start(t)
	conn := bus.Conn(t)
	release, err := NewDaemon(&fakeHost{}).Export(conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return authFixture{bus: bus, conn: conn, emitter: NewEmitter(conn), client: NewClient(bus.Conn(t))}
}

func (f authFixture) watch(t *testing.T, ctx context.Context, handle string) *AuthWatch {
	t.Helper()
	w, err := f.client.WatchAuthentication(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestAuthenticationSignals(t *testing.T) {
	f := startAuth(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mine, theirs := f.watch(t, ctx, "/r/1"), f.watch(t, ctx, "/r/2")

	for _, a := range []AuthAction{AuthSwitch{UID: 0}, AuthCancel{}} {
		if err := f.emitter.AuthAction("/r/1", a); err != nil {
			t.Fatalf("emit %#v: %v", a, err)
		}
	}
	if err := f.emitter.AuthAction("/r/1", nil); err == nil {
		t.Error("emitting no action succeeded")
	}
	if err := f.emitter.AuthAction("/r/2", AuthSwitch{UID: 7}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []AuthAction{AuthSwitch{UID: 0}, AuthCancel{}} {
		if got, _ := receive(t, mine); got != want {
			t.Errorf("/r/1 watch got %#v, want %#v", got, want)
		}
	}
	if got, _ := receive(t, theirs); got != (AuthSwitch{UID: 7}) {
		t.Errorf("/r/2 watch got %#v, want only its own switch", got)
	}

	cancel()
	for _, w := range []*AuthWatch{mine, theirs} {
		ended(t, w)
		if err := w.Close(); err != nil {
			t.Errorf("Close after the context ended = %v", err)
		}
	}
}

func TestAuthWatchCloseEndsIt(t *testing.T) {
	f := startAuth(t)
	w := f.watch(t, context.Background(), "/r/1")
	for range 2 {
		if err := w.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	}
	ended(t, w)
}

func TestAuthWatchFailsOnASignalOutsideTheContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		member string
		body   []any
	}{
		{"identity without a uid", "AuthenticationIdentity", []any{"/r/1"}},
		{"identity with a string uid", "AuthenticationIdentity", []any{"/r/1", "0"}},
		{"identity with a signed uid", "AuthenticationIdentity", []any{"/r/1", int32(0)}},
		{"cancel with an extra argument", "AuthenticationCancelled", []any{"/r/1", uint32(0)}},
		{"an undefined signal", "AuthenticationRestarted", []any{"/r/1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startAuth(t)
			mine, theirs := f.watch(t, context.Background(), "/r/1"), f.watch(t, context.Background(), "/r/2")
			defer theirs.Close()
			if err := f.conn.Emit(ServicePath, iface+"."+tc.member, tc.body...); err != nil {
				t.Fatal(err)
			}
			ended(t, mine)
			err := mine.Close()
			if err == nil || !strings.Contains(err.Error(), tc.member) || !strings.Contains(err.Error(), "/r/1") {
				t.Errorf("Close = %v, want an error naming %s and the handle", err, tc.member)
			}
			if again := mine.Close(); !errors.Is(again, err) {
				t.Errorf("Close again = %v, want %v", again, err)
			}
			if err := f.emitter.AuthAction("/r/2", AuthCancel{}); err != nil {
				t.Fatal(err)
			}
			if got, _ := receive(t, theirs); got != (AuthCancel{}) {
				t.Errorf("/r/2 watch got %#v, want its cancel after another request's bad signal", got)
			}
		})
	}
}

func TestAuthWatchReportsTheConnectionClosing(t *testing.T) {
	f := startAuth(t)
	conn := f.bus.Conn(t)
	w, err := NewClient(conn).WatchAuthentication(context.Background(), "/r/1")
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	ended(t, w)
	err = w.Close()
	if err == nil || !strings.Contains(err.Error(), "connection closed") || !strings.Contains(err.Error(), "/r/1") {
		t.Errorf("Close = %v, want the connection closing, for the handle", err)
	}
	if !errors.Is(err, dbusx.ErrDisconnected) {
		t.Errorf("Close = %v, want dbusx.ErrDisconnected", err)
	}
}

func TestIntrospectionDeclaresTheAuthenticationSignals(t *testing.T) {
	bus := dbustest.Start(t)
	release, err := NewDaemon(&fakeHost{}).Export(bus.Conn(t))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	node, err := introspect.Call(bus.Conn(t).Object(ServiceName, ServicePath))
	if err != nil {
		t.Fatal(err)
	}
	want := []introspect.Signal{
		{Name: "AuthenticationCancelled", Args: []introspect.Arg{{Name: "handle", Type: "s"}}},
		{Name: "AuthenticationIdentity", Args: []introspect.Arg{{Name: "handle", Type: "s"}, {Name: "uid", Type: "u"}}},
	}
	i := slices.IndexFunc(node.Interfaces, func(i introspect.Interface) bool { return i.Name == iface })
	if i < 0 {
		t.Fatalf("introspection lacks %s: %+v", iface, node.Interfaces)
	}
	if got := node.Interfaces[i].Signals; !reflect.DeepEqual(got, want) {
		t.Errorf("signals = %+v, want %+v", got, want)
	}
}

// forge sends a signal from conn the way the host would, both
// broadcast and straight to dest, which no match rule filters, and
// waits until the bus has passed both on.
func forge(t *testing.T, conn *dbus.Conn, dest string, path dbus.ObjectPath, name string, body ...any) {
	t.Helper()
	if err := conn.Emit(path, name, body...); err != nil {
		t.Fatal(err)
	}
	ifc, member, _ := strings.CutLast(name, ".")
	msg := &dbus.Message{Type: dbus.TypeSignal, Headers: map[dbus.HeaderField]dbus.Variant{
		dbus.FieldDestination: dbus.MakeVariant(dest),
		dbus.FieldPath:        dbus.MakeVariant(path),
		dbus.FieldInterface:   dbus.MakeVariant(ifc),
		dbus.FieldMember:      dbus.MakeVariant(member),
		dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(body...)),
	}, Body: body}
	if err := conn.Send(msg, nil).Err; err != nil {
		t.Fatal(err)
	}
	if err := conn.BusObject().Call("org.freedesktop.DBus.GetId", 0).Err; err != nil {
		t.Fatal(err)
	}
}

// A peer other than the host cannot act in its dialog, nor end the
// watch by claiming the host left; the host's own signal still arrives.
func TestAuthWatchHearsOnlyTheHost(t *testing.T) {
	f := startAuth(t)
	w := f.watch(t, context.Background(), "/r/1")
	defer func() {
		if err := w.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	}()
	forger := f.bus.Conn(t)
	client := f.client.conn.Names()[0]
	forge(t, forger, client, ServicePath, authCancelled.Name(), "/r/1")
	forge(t, forger, client, ServicePath, authIdentity.Name(), "/r/1", uint32(0))
	forge(t, forger, client, ServicePath, iface+".AuthenticationRestarted", "/r/1")
	forge(t, forger, client, "/org/freedesktop/DBus", "org.freedesktop.DBus.NameOwnerChanged", ServiceName, f.conn.Names()[0], "")
	if err := f.emitter.AuthAction("/r/1", AuthSwitch{UID: 3}); err != nil {
		t.Fatal(err)
	}
	if got, open := receive(t, w); !open || got != (AuthSwitch{UID: 3}) {
		t.Errorf("watch got %#v (open %v), want only the host's switch", got, open)
	}
}

// The host losing its name takes the dialog with it, whoever owns the
// name next: the watch ends with an error naming the host, and a new
// owner's signals are not delivered.
func TestAuthWatchFailsWhenTheHostGoes(t *testing.T) {
	for _, tc := range []struct {
		name string
		gone func(t *testing.T, f authFixture)
	}{
		{"released", func(t *testing.T, f authFixture) {
			if _, err := f.conn.ReleaseName(ServiceName); err != nil {
				t.Fatal(err)
			}
		}},
		{"disconnected", func(t *testing.T, f authFixture) {
			if err := f.conn.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startAuth(t)
			w := f.watch(t, context.Background(), "/r/1")
			host := f.conn.Names()[0]
			tc.gone(t, f)
			successor := f.bus.Conn(t)
			if _, err := NewDaemon(&fakeHost{}).Export(successor); err != nil {
				t.Fatal(err)
			}
			if err := NewEmitter(successor).AuthAction("/r/1", AuthCancel{}); err != nil {
				t.Fatal(err)
			}
			ended(t, w)
			err := w.Close()
			if err == nil || !strings.Contains(err.Error(), host) || !strings.Contains(err.Error(), "/r/1") {
				t.Errorf("Close = %v, want an error naming the host %s and the handle", err, host)
			}
		})
	}
}

// A signal the watch does not deliver, here the host's about another
// request sent straight to the client, has its descriptors closed: the
// watch's channel was handed copies of its own, which nobody else can
// close.
func TestAuthWatchClosesTheDescriptorsOfSignalsItDoesNotDeliver(t *testing.T) {
	f := startAuth(t)
	before := dbustest.MatchRules(t, f.client.conn)
	w := f.watch(t, context.Background(), "/r/1")
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	body := []any{"/r/2", dbus.UnixFD(wr.Fd())}
	msg := &dbus.Message{Type: dbus.TypeSignal, Headers: map[dbus.HeaderField]dbus.Variant{
		dbus.FieldDestination: dbus.MakeVariant(f.client.conn.Names()[0]),
		dbus.FieldPath:        dbus.MakeVariant(ServicePath),
		dbus.FieldInterface:   dbus.MakeVariant(iface),
		dbus.FieldMember:      dbus.MakeVariant("AuthenticationHandover"),
		dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(body...)),
	}, Body: body}
	if err := f.conn.Send(msg, nil).Err; err != nil {
		t.Fatal(err)
	}
	if err := f.emitter.AuthAction("/r/1", AuthCancel{}); err != nil {
		t.Fatal(err)
	}
	if got, open := receive(t, w); !open || got != (AuthCancel{}) {
		t.Fatalf("watch got %#v (open %v), want the cancel sent after the other request's signal", got, open)
	}
	if err := wr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("read the pipe = %d, %v; want end of file: the watch closed its copy of the descriptor", n, err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
	if got := dbustest.MatchRules(t, f.client.conn); !slices.Equal(got, before) {
		t.Errorf("after Close the connection holds %q, want %q", got, before)
	}
}

func TestWatchAuthenticationWithoutHostFails(t *testing.T) {
	w, err := NewClient(dbustest.Start(t).Conn(t)).WatchAuthentication(context.Background(), "/r/1")
	if err == nil {
		_ = w.Close()
		t.Fatal("watching without a host succeeded")
	}
	if !strings.Contains(err.Error(), "/r/1") || !strings.Contains(err.Error(), ServiceName) {
		t.Errorf("WatchAuthentication = %v, want an error naming the handle and %s", err, ServiceName)
	}
}

func TestBindShortcutsRefusesMalformedRequestsAndReplies(t *testing.T) {
	bus := dbustest.Start(t)
	host := &fakeHost{yes: true}
	release, err := NewDaemon(host).Export(bus.Conn(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	c, ctx := NewClient(bus.Conn(t)), context.Background()
	for name, r := range map[string]ShortcutsRequest{
		"no shortcuts": {Title: "T"},
		"an id twice":  {Rows: []ShortcutRow{{ID: "a"}, {ID: "a"}}},
	} {
		host.asked.Store([]any{"untouched"})
		if ok, _, err := c.BindShortcuts(ctx, Caller{}, r); err == nil || ok {
			t.Errorf("%s: BindShortcuts = %v, %v; want an error", name, ok, err)
		}
		if got := host.asked.Load(); !reflect.DeepEqual(got, []any{"untouched"}) {
			t.Errorf("%s reached the host with %#v", name, got)
		}
	}
	r := ShortcutsRequest{Rows: []ShortcutRow{{ID: "a"}, {ID: "b"}}}
	for name, reply := range map[string][]ShortcutTrigger{
		"a shortcut missing":  {{ID: "a"}},
		"a shortcut twice":    {{ID: "a"}, {ID: "a"}},
		"an unknown shortcut": {{ID: "a"}, {ID: "c"}},
	} {
		host.triggers.Store(reply)
		if ok, triggers, err := c.BindShortcuts(ctx, Caller{}, r); err == nil || ok || triggers != nil {
			t.Errorf("host replying %s: BindShortcuts = %v, %v, %v; want an error", name, ok, triggers, err)
		}
	}
	host.triggers.Store([]ShortcutTrigger{{ID: "b", Trigger: "Super+F9"}, {ID: "a"}})
	if ok, triggers, err := c.BindShortcuts(ctx, Caller{}, r); err != nil || !ok || len(triggers) != 2 {
		t.Errorf("a reply in another order or with an unassigned shortcut = %v, %v, %v", ok, triggers, err)
	}
}
