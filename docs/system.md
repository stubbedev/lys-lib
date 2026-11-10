# System services

## pipewire

`pipewire` is a ScreenCast video producer: a client-node exported over
PipeWire's native protocol, in Go, without libpipewire. The producer drives its
own graph; a cycle runs when the source has a frame, stamped with header,
transform and damage metas.

```go
package main

import (
	"log"

	"github.com/stubbedev/lys-lib/pipewire"
)

type gray struct{}

func (gray) Fill(dst []byte) ([]pipewire.Rect, bool) {
	for i := range dst {
		dst[i] = 0x80
	}
	return nil, true
}

func main() {
	p, err := pipewire.Start(pipewire.Config{
		Application: "example",
		Name:        "example-screen",
		Width:       640,
		Height:      480,
		Stride:      640 * 4,
		Format:      pipewire.VideoBGRx,
		FPS:         30,
		Source:      gray{},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer p.Close()

	log.Printf("node %d", p.NodeID())
	<-p.Done()
	if err := p.Err(); err != nil {
		log.Fatal(err)
	}
}
```

`Start` returns once the server registered the node, so a consumer can link to
`NodeID`.

`Config`:

| Field | Effect |
| --- | --- |
| `Application` | Client `application.name`. Required |
| `Name` | Node name consumers link to. Required |
| `Width`, `Height`, `Stride`, `Format` | Frame geometry and `VideoFormat` |
| `FPS` | Maximum frame rate, advertised and paced; frames go out when the source has one. Required |
| `Transform` | Constant `spa_meta_videotransform` value (the output's transform; 0 for a window) |
| `Source` | Fills frames |
| `Cursor` | Optional; carries the pointer as `SPA_META_Cursor` metadata instead of in the frame |

A `Source.Fill` copies one frame and returns the damage (nil is the whole frame);
returning false skips the cycle. Optional interfaces:

| Interface | Effect |
| --- | --- |
| `DmabufSource` | Fills GPU buffers in place. Its modifier's format is offered first; consumers of the plain format get shared memory through `Fill` |
| `DmabufPreparer` | Starts filling the next cycle's dmabuf early (`PrepareDmabuf`) |
| `FrameNotifier` | Signals when a GPU frame is ready instead of being polled |
| `Resizable` | Frames change size; the producer renegotiates and holds frames until the consumer follows |
| `Restarter` | Told when the consumer's buffers are replaced, so the next fill produces a frame |

## eis

`eis` is an EIS server, the receiving end of libei. A portal's `ConnectToEIS`
hands the app one end of a socket pair.

- `Pair` makes the pair, serves a `Handler` on one end and returns the client's
  end. `Serve` runs on an existing connection.
- Both advertise a named seat with a pointer and a keyboard, plus an absolute
  pointer and a touchscreen over the given `Region`s, and replay emulated input
  onto the `Handler`.
- `NewReceiver` serves a libei receiver context instead: the client binds a
  keyboard and pointer, and the caller emulates input on them.

## dbusx

`dbusx` is shared D-Bus plumbing on the [patched godbus](/third-party/godbus).
Import that copy, not upstream: `dbusx` takes its `Conn`.

```go
package main

import (
	"log"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"

	"github.com/stubbedev/lys-lib/dbusx"
)

type greeter struct{}

func (greeter) Hello(name string) (string, *dbus.Error) {
	if name == "" {
		return "", dbusx.InvalidArgs("name is empty")
	}
	return "hello " + name, nil
}

func main() {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	release, err := dbusx.Serve(conn, dbusx.Service{
		Name:      "org.example.Greeter",
		Path:      "/org/example/Greeter",
		Interface: "org.example.Greeter",
		Methods:   greeter{},
		Properties: dbusx.Getters{
			"Version": func() any { return uint32(1) },
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer release()

	select {}
}
```

| Need | Use |
| --- | --- |
| One interface under a well-known name | `Serve` with a `Service` |
| Several interfaces at one path, all or nothing | `Export` with `Interface`s |
| A well-known name alone | `Own`; `ErrNameTaken` if owned |
| Computed and writable properties | `Getters`, `Setters` |
| A signal declared once | `NewSignal`; `Signal[T]` declares, emits and decodes it |
| Methods run one at a time in arrival order | `Interface.Ordered` |
| Following a service that starts or restarts | `WatchOwner`, `NameOwner` |
| One peer's signals, decoded | `WatchSignals` with an `On` route per signal, from `FromOwner(name)` or `FromBus()` |
| The system bus, without hanging on a silent one | `DialSystem` with a timeout |

A D-Bus method is an exported method whose last result is `*dbus.Error`;
`Failed` and `InvalidArgs` build common errors. Introspection is generated on
demand.

`WatchSignals` runs each route's handler in arrival order on one goroutine and
tells `failed` of a signal that does not decode. It takes signals only from the
sender it resolved when it started; one from any other peer is dropped. A signal
carrying descriptors reaches every watch of a connection with duplicates of its
own, so several watches may route the same signal: the handler that gets it owns
them, and the watch closes those of every signal no handler gets and of the
signals still unread when it ends (`dbus.Signal.CloseFDs`, on the
[patched godbus](/third-party/godbus)). The watch holds one match rule per signal, or one for the whole
interface where several signals of an object share it, and removes them when its
context ends, `Watch.Stop` is called, or the connection closes
(`ErrDisconnected`). `Route.WithArg0` narrows a route to one first argument, as
for `PropertiesChanged` of one interface. `Dispatch` is the watch without the
bus: it routes one signal, for a caller that reads its own signal channel, and
closes the descriptors of a signal it hands to no handler.

## pulse/native

`pulse/native` is a pure-Go client for PulseAudio's native protocol, served by
PulseAudio and by `pipewire-pulse`: server, card, sink and source introspection,
volume and mute, subscriptions, record streams, the sample cache, and playback
streams. A playback `Write` blocks until the server asks for audio, never
allocates, and `Cork` pauses it; each stream gets its own sync group.

`pulse/pulsetest` is a fake server speaking the same protocol. Like a real one
it drops a client's record and playback streams when the client disconnects:
they leave the stream listings and subscribers are told of the removals.

## logind

`logind` is the one client for `org.freedesktop.login1`. Every call is bounded
by the caller's context and by `CallTimeout`, and its error names the method and
the object. `Dial`, which connects to the system bus, is bounded the same way.

| Need | Use |
| --- | --- |
| The caller's session | `NewManager(conn).GetSession(ctx, logind.AutoSession)` |
| The session of a process | `Manager.GetSessionByPID` |
| Suspend, reboot, power off | `Manager.Power` with a `PowerAction` |
| Delay or block sleep, idle, shutdown, lid | `Manager.Inhibit` with an `InhibitWhat` set and an `InhibitMode` |
| Lock state | `Session.Lock`, `LockedHint`, `SetLockedHint`, `WaitLocked` |
| Device access for a compositor | `Session.TakeControl`, `TakeDevice`, `PauseDeviceComplete`, `ReleaseDevice` |
| Backlight through logind | `Session.SetBrightness` |
| Sleep, lock, device and property signals | `Session.Watch`, which takes signals only from logind's owner; each watch owns the descriptor of a device it is told resumed |

`logind/logindtest` is a fake logind on a private bus that records every call.
The methods that have it announce something (`SetLockedHint`, `Lock`,
`PrepareForSleep`, …) report a failure with `t.Errorf` and return false, so they
are safe on any goroutine of the test, an `OnLock` hook included; the fake waits
for hooks still running before its connection closes.

## polkit

`polkit` is the one client for polkitd's Authority
(`org.freedesktop.PolicyKit1`). Every call but `CheckAuthorization` is bounded
by the caller's context and by `CallTimeout`; a check may wait for a person to
authenticate, so its context alone bounds it. Errors name the action and the
subject.

```go
func mayApply(ctx context.Context, conn *net.UnixConn) (polkit.Verdict, error) {
	pidfd, err := peercred.PIDFDOf(conn)
	if err != nil {
		return 0, err
	}
	defer pidfd.Close()
	subject, err := polkit.ProcessOfPIDFD(pidfd)
	if err != nil {
		return 0, err
	}
	system, err := polkit.Dial(ctx)
	if err != nil {
		return 0, err
	}
	defer system.Close()
	r, err := polkit.NewAuthority(system).CheckAuthorization(ctx, subject, "org.example.apply", polkit.AllowUserInteraction)
	return r.Verdict(), err
}
```

| Need | Use |
| --- | --- |
| The subject for a socket peer | `ProcessOfPIDFD` with its `peercred.PIDFD`; `ProcessOf` with its `peercred.Credentials` where the kernel has no `SO_PEERPIDFD` |
| The subject for a logind session | `UnixSession` |
| Whether a subject may do something | `Authority.CheckAuthorization`, then `AuthorizationResult.Verdict`: `Authorized`, `Denied`, `Dismissed` or `Challenge` |
| An authentication agent's registration | `Authority.RegisterAuthenticationAgent`, `UnregisterAuthenticationAgent`, with `AgentInterface` |
| The registered actions | `Authority.EnumerateActions` |
| Retained authorizations | `Authority.EnumerateTemporaryAuthorizations`, `RevokeTemporaryAuthorizations`, `RevokeTemporaryAuthorizationByID` |
| polkitd's `Changed` signal | `Changed`, `ChangedMatch` |

A `UnixProcess` always carries pid, start time and uid; with a pidfd, polkit 124
and later decide about that very process, and an older polkitd ignores it.
`ProcessOfPIDFD` reads the start time and then checks that the process is still
running, so the start time cannot be a later process's with the same pid. When
a check's context ends first, the check is cancelled at polkitd
(`CancelCheckAuthorization`), which takes the dialog down.

`polkit/polkittest` is a fake Authority: it answers checks with the result the
test sets, or holds them until they are cancelled, and records registrations,
checks (with the process a pidfd names) and cancels.

## portaldialogs

`portaldialogs` is the `dev.stubbe.lys.PortalDialogs1` contract: native dialogs
lys-shell shows for the desktop-portal backend (Access, Account, AppChooser,
DynamicLauncher, GlobalShortcuts, Usb, Wallpaper preview), plus the password
prompt and administrator authentication.

| Type | Role |
| --- | --- |
| `Host` | Implemented by a shell; each call blocks until the user answers or the request is dismissed |
| `Daemon` | Serves a `Host`: `NewDaemon`, then `Export` |
| `Client` | Calls the service, bounded only by its `ctx`; `WatchAuthentication` follows an authentication |
| `Emitter` | Sends the authentication signals |
| `Caller`, `Choice` | The request context every dialog receives |

## wmipc

`wmipc` is the wire contract of lys-wm's IPC socket: the socket variable, request
and event names, and the small types both sides share. `Output`, `Workspace` and
`Window` are the views clients decode, a subset of what the compositor sends.
lys-wm's tests hold those views to its own JSON.

## pam

`pam` binds libpam through purego.

- `Start(service, user, confdir, converse)` opens a `Txn`. `confdir`, if set,
  reads the service from that directory (`pam_start_confdir`) instead of
  `/etc/pam.d`.
- A `Converse` function answers each batch of `Message`s with `Reply`s or a PAM
  error code.
- `Txn` wraps `Authenticate`, `AcctMgmt`, `Chauthtok`, `Setcred`, `OpenSession`,
  `CloseSession`, `Putenv`, `Getenvlist`, `Strerror` and `End`. `Authtok`
  returns a copy of `PAM_AUTHTOK`.
- `CheckLibrary` reports whether libpam and libc load.

## appearance, portalsettings

`appearance` holds the color scheme, accent and contrast as values.
`portalsettings.Monitor` follows them, the icon theme and the fonts over the
xdg-desktop-portal Settings interface. Without a session bus every value reads
unknown. Bus and portal restarts reconnect with backoff.

## icons, icontheme

`icons` is the freedesktop icon theme spec in pure Go: `index.theme`
inheritance, directory lookup with hicolor fallback, a raster cache per name,
size and scale, and bundled [Lucide](https://lucide.dev) icons as the last tier.
`icontheme` is its narrow form for programs that only rasterize named icons,
such as a compositor.

## i18n

`i18n` reads GNU `.mo` and `.po` catalogs with plural forms and message
contexts, and resolves locales from the environment.

## glibclocale

`glibclocale` reads glibc's compiled locale data, the locale-archive and
per-locale `LC_*` directories, without libc. `System(env)` searches where glibc
does (`LOCPATH`, NixOS's `LOCALE_ARCHIVE`, `/usr/lib/locale`); `List` replaces
`locale -a`, and `Numeric`, `Monetary` and `Time` decode a locale's categories,
with `Time.Format` covering the strftime conversions glibc's own formats use. A
missing locale or category is an error naming where it looked, never C. musl
has no compiled data, so only C and POSIX exist there. `glibclocaletest` holds
real `localedef` output for tests.

## Small helpers

| Package | Purpose |
| --- | --- |
| `clib` | `Open` dlopens the first loadable candidate, joining all failures. `SystemCandidates` lists a soname's usual paths, including the NixOS system profile. `GoString` copies a C string |
| `intconv` | Integer conversion without silent wraparound: `Exact` errors, `Must` panics, `Clamp` saturates, `Duration` scales a count. `Uint32Bits`, `Uint64Bits`, `Int32Bits`, `Int64Bits` reinterpret two's-complement bits |
| `atomicfile` | `Write`, `WriteFile`, `WriteSeekable` replace a file whole and durably: synced temporary, rename, directory sync |
| `assetfile` | `Open` opens an asset without blocking and rejects anything but a regular file within a size limit |
| `xdg` | `Env` resolves base directories (`ConfigHome`, `StateDir`, …), an app's directory under each, the system directories (`DataDirs`, `ConfigDirs`), the search paths files are looked up along (`DataSearchPath`, `ConfigSearchPath`: the home directory, then the system ones, relative entries ignored), and `user-dirs.dirs` via `UserDir`. `xdg.OS` reads the process environment |
| `peercred` | Who is at a Unix socket's other end: `Of` reads its `Credentials` (uid, gid, pid, from `SO_PEERCRED`), `PIDFDOf` opens a `PIDFD` of that process (`SO_PEERPIDFD`), which keeps naming it after its pid is reused and tells whether it has `Exited`. A kernel before Linux 6.5 has none: `ErrNoPIDFD` |
| `debug` | `Log` traces only in builds with the `lysdebug` tag; otherwise it compiles out with its arguments. `LYS_DEBUG` selects categories: unset, empty or `*` for all, a comma-separated list for those, `-name` to exclude one |
| `logutil` | The injectable `*slog.Logger` the libraries emit through; a discarding sink until `Set` |
| `testbuild` | `Race`, `AllocBudgetsHold`, `SkipAllocBudget` and `GoroutineID`, for tests that depend on how the binary was built or which goroutine runs |
