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

A D-Bus method is an exported method whose last result is `*dbus.Error`;
`Failed` and `InvalidArgs` build common errors. Introspection is generated on
demand.

## pulse/native

`pulse/native` is a pure-Go client for PulseAudio's native protocol, served by
PulseAudio and by `pipewire-pulse`: server, card, sink and source introspection,
volume and mute, subscriptions, record streams, the sample cache, and playback
streams. A playback `Write` blocks until the server asks for audio, never
allocates, and `Cork` pauses it; each stream gets its own sync group.

## logind

`logind` is the one client for `org.freedesktop.login1`. Every call is bounded
by the caller's context and by `CallTimeout`, and its error names the method and
the object.

| Need | Use |
| --- | --- |
| The caller's session | `NewManager(conn).GetSession(ctx, logind.AutoSession)` |
| The session of a process | `Manager.GetSessionByPID` |
| Suspend, reboot, power off | `Manager.Power` with a `PowerAction` |
| Delay or block sleep, idle, shutdown, lid | `Manager.Inhibit` with an `InhibitWhat` set and an `InhibitMode` |
| Lock state | `Session.Lock`, `LockedHint`, `SetLockedHint`, `WaitLocked` |
| Device access for a compositor | `Session.TakeControl`, `TakeDevice`, `PauseDeviceComplete`, `ReleaseDevice` |
| Backlight through logind | `Session.SetBrightness` |
| Sleep, lock, device and property signals | `Session.Watch`, which takes signals only from logind's owner |

`logind/logindtest` is a fake logind on a private bus that records every call.

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
| `xdg` | `Env` resolves base directories (`ConfigHome`, `StateDir`, …), an app's directory under each, and `user-dirs.dirs` via `UserDir`. `xdg.OS` reads the process environment |
| `peercred` | `UID` of the process at a Unix socket's other end (`SO_PEERCRED`) |
| `debug` | `Log` traces only in builds with the `lysdebug` tag; otherwise it compiles out with its arguments. `LYS_DEBUG` selects categories: unset, empty or `*` for all, a comma-separated list for those, `-name` to exclude one |
| `logutil` | The injectable `*slog.Logger` the libraries emit through; a discarding sink until `Set` |
| `testbuild` | `Race`, `AllocBudgetsHold`, `SkipAllocBudget` and `GoroutineID`, for tests that depend on how the binary was built or which goroutine runs |
