// Package portaldialogs is the dev.stubbe.lys.PortalDialogs1 contract:
// the native dialogs lys-shell shows behind the desktop-portal backend's
// Access, Account, AppChooser, DynamicLauncher, Usb and Wallpaper
// preview, in place of xdg-desktop-portal-gtk, and the password prompt
// and administrator authentication other lys processes ask through it. It holds the requests and replies,
// the Host a shell implements, the Daemon serving one, the Client
// calling it, and what every portal dialog is told about the request it
// serves: its Caller and the Choices it carries.
package portaldialogs

import (
	"context"
	"fmt"
	"time"

	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// D-Bus identity (wayle-ipc portal_dialogs.rs).
const (
	ServiceName = "dev.stubbe.lys.PortalDialogs1"
	ServicePath = dbus.ObjectPath("/dev/stubbe/lys/PortalDialogs")
	iface       = "dev.stubbe.lys.PortalDialogs1"
)

// authCancelledArgs is AuthenticationCancelled(s handle): the user
// cancelled the authentication for the request at handle.
type authCancelledArgs struct {
	Handle string `dbus:"handle"`
}

// authIdentityArgs is AuthenticationIdentity(s handle, u uid): the user
// chose to authenticate as uid instead.
type authIdentityArgs struct {
	Handle string `dbus:"handle"`
	UID    uint32 `dbus:"uid"`
}

// The signals an Emitter sends while no Authenticate call waits.
var (
	authCancelled = dbusx.NewSignal[authCancelledArgs](iface, "AuthenticationCancelled")
	authIdentity  = dbusx.NewSignal[authIdentityArgs](iface, "AuthenticationIdentity")
)

// unwatchTimeout bounds removing a watch's match rule once its context
// has ended.
const unwatchTimeout = 5 * time.Second

// AccessRequest is one grant/deny prompt.
type AccessRequest struct {
	Title, Subtitle, Body string
	GrantLabel, DenyLabel string
	// Icon is a themed icon name for the permission, "" for none.
	Icon string
	// Choices are extra questions shown with the prompt.
	Choices []Choice
}

// AccountInfo is what the Account dialog shares, (ss): the name and an
// avatar image path ("" for none).
type AccountInfo struct{ Name, Image string }

// ChooserRequest is one app chooser, (asss). App ids are desktop ids
// without ".desktop", as the portal speaks them.
type ChooserRequest struct {
	// Choices are the app ids offered; none offers the apps for
	// ContentType.
	Choices []string
	// LastChoice is the app id preselected, "" for none.
	LastChoice string
	// ContentType is the type opened, "" when unknown.
	ContentType string
	// Target names what is opened (a file's basename or a URI), ""
	// for nothing to name.
	Target string
}

// LauncherRequest is one dynamic launcher to confirm, (saysb).
type LauncherRequest struct {
	// Name is the launcher's default name.
	Name string
	// Icon is the launcher's image (PNG, JPEG or SVG), empty for none.
	Icon []byte
	// Target is the URL a web app opens, "" for an application.
	Target string
	// EditableName lets the user change the name.
	EditableName bool
}

// Device is one USB device an app asks to open, (ssb).
type Device struct {
	// ID is the frontend's device id; Name is how the user knows it.
	ID, Name string
	// Writable is whether the app asks to write to it.
	Writable bool
}

// PasswordRequest asks for a password, (ssbb): Confirm asks for a new
// one, typed twice; Retry says the previous answer was wrong.
type PasswordRequest struct {
	Title, Body    string
	Confirm, Retry bool
}

// Host shows the dialogs; each call blocks until the user answers or
// the request is dismissed, which answers no ("" for a chooser).
type Host interface {
	// Access replies whether the user granted and, when granted, what
	// they picked for each choice.
	Access(Caller, AccessRequest) (bool, []Selection)
	// Account offers info for sharing and replies whether the user
	// shares it, and what they left of it after editing.
	Account(c Caller, reason string, info AccountInfo) (bool, AccountInfo)
	// ConfirmWallpaper previews a file:// image and replies whether to
	// set it.
	ConfirmWallpaper(c Caller, uri string) bool
	// ChooseApplication replies the chosen app id, "" on cancel.
	ChooseApplication(Caller, ChooserRequest) string
	// UpdateChoices replaces the app ids offered by the app chooser
	// for the request at handle, showing or waiting; false when no
	// chooser is open for it.
	UpdateChoices(handle string, choices []string) bool
	// ChooseDevices replies whether the user allowed the app USB
	// devices and the ids of the ones they picked.
	ChooseDevices(Caller, []Device) (bool, []string)
	// ConfirmInstall replies whether to install a dynamic launcher,
	// and under what name.
	ConfirmInstall(Caller, LauncherRequest) (bool, string)
	// Password replies whether the user entered a password, and it.
	Password(Caller, PasswordRequest) (bool, string)
	// Authenticate shows the authentication dialog for the request at
	// the caller's handle, or updates the one showing, and replies once
	// the user answers its prompt, cancels or picks another identity.
	// The dialog stays up until Dismiss.
	Authenticate(Caller, AuthRequest) AuthReply
	// ShowAuthentication shows the authentication dialog for the
	// request at the caller's handle, or updates the one showing,
	// without a prompt: its notes only. What the user does meanwhile
	// goes out through an Emitter.
	ShowAuthentication(Caller, AuthRequest)
	// Dismiss takes down the dialog showing for the request at handle,
	// when one is; the app closed the request.
	Dismiss(handle string)
}

// Daemon serves a Host as dev.stubbe.lys.PortalDialogs1.
type Daemon struct{ host Host }

// NewDaemon wraps the host for export.
func NewDaemon(host Host) *Daemon { return &Daemon{host: host} }

// Export serves the interface under its name; the returned release
// drops the name.
func (d *Daemon) Export(conn *dbus.Conn) (func(), error) {
	return dbusx.Serve(conn, dbusx.Service{
		Name: ServiceName, Path: ServicePath, Interface: iface, Methods: d,
		Signals: []dbusx.SignalDecl{authCancelled, authIdentity},
	})
}

// Access is the grant/deny prompt.
func (d *Daemon) Access(c Caller, title, subtitle, body, grantLabel, denyLabel, icon string, choices []Choice) (bool, []Selection, *dbus.Error) {
	granted, picked := d.host.Access(c, AccessRequest{title, subtitle, body, grantLabel, denyLabel, icon, choices})
	return granted, picked, nil
}

// Account is the account-info consent.
func (d *Daemon) Account(c Caller, reason string, info AccountInfo) (bool, AccountInfo, *dbus.Error) {
	shared, edited := d.host.Account(c, reason, info)
	return shared, edited, nil
}

// ConfirmWallpaper is the wallpaper preview confirmation.
func (d *Daemon) ConfirmWallpaper(c Caller, uri string) (bool, *dbus.Error) {
	return d.host.ConfirmWallpaper(c, uri), nil
}

// ChooseApplication is the app chooser.
func (d *Daemon) ChooseApplication(c Caller, r ChooserRequest) (string, *dbus.Error) {
	return d.host.ChooseApplication(c, r), nil
}

// UpdateChoices updates the app chooser open for handle.
func (d *Daemon) UpdateChoices(handle string, choices []string) (bool, *dbus.Error) {
	return d.host.UpdateChoices(handle, choices), nil
}

// ChooseDevices is the USB device consent.
func (d *Daemon) ChooseDevices(c Caller, devices []Device) (bool, []string, *dbus.Error) {
	allowed, picked := d.host.ChooseDevices(c, devices)
	return allowed, picked, nil
}

// ConfirmInstall is the dynamic-launcher install confirmation.
func (d *Daemon) ConfirmInstall(c Caller, r LauncherRequest) (bool, string, *dbus.Error) {
	ok, name := d.host.ConfirmInstall(c, r)
	return ok, name, nil
}

// Password is the password prompt.
func (d *Daemon) Password(c Caller, r PasswordRequest) (bool, string, *dbus.Error) {
	ok, password := d.host.Password(c, r)
	return ok, password, nil
}

// Authenticate is one prompt of an administrator authentication.
func (d *Daemon) Authenticate(c Caller, r AuthRequest) (bool, uint32, string, *dbus.Error) {
	if err := r.check(); err != nil {
		return false, 0, "", dbusx.InvalidArgs("Authenticate: " + err.Error())
	}
	ok, uid, response, err := encodeReply(r, d.host.Authenticate(c, r))
	if err != nil {
		return false, 0, "", dbusx.Failed("Authenticate: host replied a " + err.Error())
	}
	return ok, uid, response, nil
}

// ShowAuthentication updates an administrator authentication without a
// prompt.
func (d *Daemon) ShowAuthentication(c Caller, r AuthRequest) *dbus.Error {
	if err := r.check(); err != nil {
		return dbusx.InvalidArgs("ShowAuthentication: " + err.Error())
	}
	d.host.ShowAuthentication(c, r)
	return nil
}

// Dismiss takes down the dialog showing for the request at handle.
func (d *Daemon) Dismiss(handle string) *dbus.Error {
	d.host.Dismiss(handle)
	return nil
}

// Emitter signals what the user does in an authentication dialog while
// no Authenticate call waits.
type Emitter struct{ conn *dbus.Conn }

// NewEmitter signals over conn, the one the Daemon is exported on.
func NewEmitter(conn *dbus.Conn) *Emitter { return &Emitter{conn: conn} }

// AuthAction signals the action taken in the dialog for the request at
// handle: AuthenticationCancelled(s) or AuthenticationIdentity(su).
func (e *Emitter) AuthAction(handle string, a AuthAction) error {
	var err error
	switch a := a.(type) {
	case AuthCancel:
		err = authCancelled.Emit(e.conn, ServicePath, authCancelledArgs{Handle: handle})
	case AuthSwitch:
		err = authIdentity.Emit(e.conn, ServicePath, authIdentityArgs{Handle: handle, UID: a.UID})
	default:
		return fmt.Errorf("signal %T for %s: unknown action", a, handle)
	}
	if err != nil {
		return fmt.Errorf("signal %T for %s: %w", a, handle, err)
	}
	return nil
}

// Client calls the shell's dialogs; a dialog waits on the user, so the
// calls are bounded only by ctx.
type Client struct {
	conn *dbus.Conn
	obj  dbus.BusObject
}

// NewClient addresses the host over conn.
func NewClient(conn *dbus.Conn) *Client {
	return &Client{conn: conn, obj: conn.Object(ServiceName, ServicePath)}
}

func (c *Client) call(ctx context.Context, method string, args ...any) *dbus.Call {
	return c.obj.CallWithContext(ctx, iface+"."+method, 0, args...)
}

// Access shows the grant/deny prompt.
func (c *Client) Access(ctx context.Context, caller Caller, r AccessRequest) (granted bool, picked []Selection, err error) {
	err = c.call(ctx, "Access", caller, r.Title, r.Subtitle, r.Body, r.GrantLabel, r.DenyLabel, r.Icon, r.Choices).Store(&granted, &picked)
	return granted, picked, err
}

// Account asks to share info, which the user may edit first.
func (c *Client) Account(ctx context.Context, caller Caller, reason string, info AccountInfo) (shared bool, edited AccountInfo, err error) {
	err = c.call(ctx, "Account", caller, reason, info).Store(&shared, &edited)
	return shared, edited, err
}

// ConfirmWallpaper previews uri and asks to set it.
func (c *Client) ConfirmWallpaper(ctx context.Context, caller Caller, uri string) (ok bool, err error) {
	err = c.call(ctx, "ConfirmWallpaper", caller, uri).Store(&ok)
	return ok, err
}

// ChooseApplication picks a handler's app id; "" is a cancel.
func (c *Client) ChooseApplication(ctx context.Context, caller Caller, r ChooserRequest) (id string, err error) {
	err = c.call(ctx, "ChooseApplication", caller, r).Store(&id)
	return id, err
}

// UpdateChoices replaces the app ids the chooser open for handle
// offers; false when none is open for it.
func (c *Client) UpdateChoices(ctx context.Context, handle string, choices []string) (found bool, err error) {
	err = c.call(ctx, "UpdateChoices", handle, choices).Store(&found)
	return found, err
}

// ChooseDevices asks which USB devices an app may open, replying the
// ids picked.
func (c *Client) ChooseDevices(ctx context.Context, caller Caller, devices []Device) (allowed bool, picked []string, err error) {
	err = c.call(ctx, "ChooseDevices", caller, devices).Store(&allowed, &picked)
	return allowed, picked, err
}

// ConfirmInstall asks to install a dynamic launcher, replying the name
// the user chose.
func (c *Client) ConfirmInstall(ctx context.Context, caller Caller, r LauncherRequest) (ok bool, name string, err error) {
	err = c.call(ctx, "ConfirmInstall", caller, r).Store(&ok, &name)
	return ok, name, err
}

// Password asks for a password; ok is false when the user cancelled.
func (c *Client) Password(ctx context.Context, caller Caller, r PasswordRequest) (ok bool, password string, err error) {
	err = c.call(ctx, "Password", caller, r).Store(&ok, &password)
	return ok, password, err
}

// Dismiss takes down the dialog showing for the request at handle.
func (c *Client) Dismiss(ctx context.Context, handle string) error {
	return c.call(ctx, "Dismiss", handle).Err
}

// Authenticate shows one prompt of an administrator authentication and
// waits for the user: an AuthAnswer for r.Selected, an AuthSwitch to
// another of r.Identities, or an AuthCancel.
func (c *Client) Authenticate(ctx context.Context, caller Caller, r AuthRequest) (AuthReply, error) {
	var (
		ok       bool
		uid      uint32
		response string
	)
	if err := c.call(ctx, "Authenticate", caller, r).Store(&ok, &uid, &response); err != nil {
		return nil, err
	}
	reply, err := decodeReply(r, ok, uid, response)
	if err != nil {
		return nil, fmt.Errorf("authenticate %s: %w", caller.Handle, err)
	}
	return reply, nil
}

// ShowAuthentication updates an administrator authentication without a
// prompt; WatchAuthentication reports what the user does meanwhile.
func (c *Client) ShowAuthentication(ctx context.Context, caller Caller, r AuthRequest) error {
	return c.call(ctx, "ShowAuthentication", caller, r).Err
}

func authMatch(handle string) []dbus.MatchOption {
	return []dbus.MatchOption{
		dbus.WithMatchSender(ServiceName),
		dbus.WithMatchObjectPath(ServicePath),
		dbus.WithMatchInterface(iface),
		dbus.WithMatchArg(0, handle),
	}
}

// authAction reads a signal as the action it reports for handle, and
// whether it is one.
func authAction(sig *dbus.Signal, handle string) (AuthAction, bool) {
	if sig.Path != ServicePath || len(sig.Body) == 0 {
		return nil, false
	}
	if h, _ := sig.Body[0].(string); h != handle {
		return nil, false
	}
	switch sig.Name {
	case authCancelled.Name():
		return AuthCancel{}, len(sig.Body) == 1
	case authIdentity.Name():
		if len(sig.Body) != 2 {
			return nil, false
		}
		uid, ok := sig.Body[1].(uint32)
		return AuthSwitch{UID: uid}, ok
	default:
		return nil, false
	}
}

// WatchAuthentication delivers what the user does in the authentication
// dialog for the request at handle while no Authenticate call waits,
// from when it returns until ctx ends, which closes the channel.
func (c *Client) WatchAuthentication(ctx context.Context, handle string) (<-chan AuthAction, error) {
	signals := make(chan *dbus.Signal, 16)
	c.conn.Signal(signals)
	match := authMatch(handle)
	if err := c.conn.AddMatchSignalContext(ctx, match...); err != nil {
		c.conn.RemoveSignal(signals)
		return nil, fmt.Errorf("watch authentication %s: %w", handle, err)
	}
	actions := make(chan AuthAction)
	go func() {
		defer close(actions)
		defer func() {
			unwatch, cancel := context.WithTimeout(context.WithoutCancel(ctx), unwatchTimeout)
			defer cancel()
			_ = c.conn.RemoveMatchSignalContext(unwatch, match...)
		}()
		defer c.conn.RemoveSignal(signals)
		for {
			select {
			case <-ctx.Done():
				return
			case sig, open := <-signals:
				if !open {
					return
				}
				a, ok := authAction(sig, handle)
				if !ok {
					continue
				}
				select {
				case actions <- a:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return actions, nil
}
