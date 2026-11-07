# System services

## pipewire

`pipewire` is the video producer a ScreenCast portal streams through: a node
exported over PipeWire's native protocol, a client-node as libpipewire's
`pw_stream` exports one, fed one captured frame per cycle the graph drives. The
protocol, the shared memory and the realtime cycle are all in Go, and
libpipewire is not involved.

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

`Start` returns once the server has registered the node, so a consumer can link
to `NodeID`. A `Source` fills one frame and reports what changed, nil being the
whole frame, and returning false skips the cycle. Two optional interfaces extend
it:

- `DmabufSource` also fills GPU buffers. The producer offers its modifier's
  format first and fills each buffer in place. A consumer that takes the plain
  format gets shared memory through `Fill`.
- `Resizable` is for frames that change size, such as a window being resized. The
  producer offers the new size and holds frames back until the consumer has
  renegotiated.

## eis

`eis` is an EIS server: the receiving end of libei, which remote-desktop clients
prefer over the portal's `Notify*` D-Bus calls. A portal's `ConnectToEIS` hands
the app one end of a socket pair. `Pair` makes that pair and returns the client's
end, and `Serve` runs the protocol on an existing connection. Both advertise a
named seat with a pointer and a keyboard, and replay what the client emulates
onto a `Handler`.

## dbusx

`dbusx` is the D-Bus plumbing every lys process shares, on the
[patched godbus](/third-party/godbus). Import that copy, not upstream's module:
its `Conn` is the one `dbusx` takes.

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
| Several interfaces at a path, as zbus's `object_server().at` does | `Export` with `Interface`s, all or nothing |
| A well-known name alone | `Own`, with `ErrNameTaken` when it is owned |
| Properties computed on every read, and writable ones | `Getters`, `Setters` |
| A signal whose name and signature exist once | `NewSignal`, whose `Signal[T]` declares, emits and decodes it |
| Methods run one at a time in arrival order | `Interface.Ordered`, for a caller that does not wait for each reply |
| A service that starts or restarts after its client did | `WatchOwner`, `NameOwner` |

Introspection data is built on demand. A method is an exported method whose last
result is a `*dbus.Error`, and `Failed` and `InvalidArgs` make the common ones.

## portaldialogs

`portaldialogs` is the `dev.stubbe.lys.PortalDialogs1` contract: the native
dialogs lys-shell shows behind the desktop-portal backend's Access, Account,
AppChooser, DynamicLauncher, GlobalShortcuts (`BindShortcuts`, a trigger per
shortcut), Usb and Wallpaper preview, and the password prompt and administrator
authentication other lys processes ask through it.

- `Host` is what a shell implements. Each call blocks until the user answers or
  the request is dismissed.
- `Daemon` serves a `Host` on the bus: `NewDaemon`, then `Export`.
- `Client` calls it, bounded only by the `ctx` it is given, since a dialog waits
  on the user. `WatchAuthentication` follows a running authentication.
- `Emitter` sends the authentication signals.
- `Caller` and `Choice` are what every portal dialog is told about the request it
  serves.

## Small helpers

| Package | What it does |
| --- | --- |
| `clib` | `Open` dlopens the first of a candidate list, joining every failure when none loads. `SystemCandidates` lists a soname's usual locations, the NixOS system profile included. `GoString` copies a C string |
| `intconv` | Integer conversions without silent wraparound: `Exact` returns an error, `Must` panics, `Clamp` saturates, `Duration` scales a count. `Uint32Bits`, `Uint64Bits`, `Int32Bits` and `Int64Bits` reinterpret two's-complement bits |
| `atomicfile` | `Write` and `WriteFile` replace a file whole and durably: a synced temporary renamed over the target, then the directory synced |
| `xdg` | `Env` resolves the base directories (`ConfigHome`, `StateDir` and so on), an app's directory under each, and `user-dirs.dirs` through `UserDir`. `xdg.OS` reads the process environment, and tests pass their own |
| `peercred` | `UID` is the uid of the process at a Unix socket's other end, from `SO_PEERCRED` |
| `debug` | `Log` is a trace call that compiles out, arguments included, without the `lysdebug` build tag. In a tagged build `LYS_DEBUG` selects categories: unset or `*` for all, a comma-separated list for those, `-name` to exclude one |
