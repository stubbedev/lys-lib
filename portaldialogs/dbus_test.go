package portaldialogs

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
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

func (f *fakeHost) Password(c Caller, r PasswordRequest) (bool, string) {
	f.record(c, r)
	if !f.yes {
		return false, ""
	}
	return true, "typed"
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

		pw := PasswordRequest{Title: "Unlock", Body: "login", Confirm: true, Retry: true}
		if ok, typed, err := c.Password(ctx, caller, pw); err != nil || ok != yes || typed != map[bool]string{true: "typed"}[yes] {
			t.Errorf("Password (yes=%v) = %v, %q, %v", yes, ok, typed, err)
		}
		askedBy("Password", pw)

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
		{"Password", func() error { _, _, err := c.Password(ctx, Caller{}, PasswordRequest{}); return err }},
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
	if err == nil || !strings.Contains(err.Error(), "connection closed") {
		t.Errorf("Close = %v, want the connection closing", err)
	}
	if !strings.Contains(fmt.Sprint(err), "remove match rule") || !errors.Is(err, dbus.ErrClosed) {
		t.Errorf("Close = %v, want the failure to remove the match rule", err)
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
