package portaldialogs

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
)

type fakeHost struct {
	yes       bool
	choice    string
	asked     dbustest.Var[[]any]
	caller    dbustest.Var[Caller]
	dismissed dbustest.Var[[]string]
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
		{"Dismiss", func() error { return c.Dismiss(ctx, "") }},
	} {
		if err := tc.call(); err == nil {
			t.Errorf("%s answered without a host", tc.method)
		}
	}
}
