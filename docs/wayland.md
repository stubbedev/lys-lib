# Wayland

Toolkit-level windows and surfaces are in
[lys-ui](https://github.com/stubbedev/lys-ui). These packages are the parts
that do not need a toolkit.

## capture

`capture` copies screen contents out of the compositor on a private Wayland
connection, independent of the application's own session and event loop.

```go
package main

import (
	"image/png"
	"log"
	"os"

	"github.com/stubbedev/lys-lib/capture"
)

func main() {
	client, err := capture.Connect()
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	outputs := client.Outputs()
	if len(outputs) == 0 {
		log.Fatal("the compositor lists no outputs")
	}
	frame, err := client.CaptureOutput(outputs[0], capture.Options{Cursor: true})
	if err != nil {
		log.Fatal(err)
	}
	img, err := frame.Image()
	if err != nil {
		log.Fatal(err)
	}
	out, err := os.Create("screen.png")
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, img); err != nil {
		log.Fatal(err)
	}
}
```

Every capture call blocks until the compositor finished the copy, so run
captures off the UI goroutine. A `Client` is safe for use from several
goroutines, but its calls serialize: captures that must overlap need separate
clients.

Protocols are feature-detected. `Connect` succeeds on any compositor, the `Has*`
methods report what is offered, and a capture whose protocol is missing fails
with `ErrUnsupported` naming it.

| Capture | Call | Protocol |
| --- | --- | --- |
| An output or a region of one, into memory | `CaptureOutput`, `CaptureOutputRegion` | wlr-screencopy |
| One output frame, into memory | `CaptureOutputOnce` | ext-image-copy-capture |
| A window, into memory | `Toplevels`, `CaptureToplevel` | ext-foreign-toplevel-list, ext-image-copy-capture |
| A window on Hyprland | `CaptureHyprlandWindow` | hyprland-toplevel-export |
| An output, continuously and driven by damage | `OpenOutputStream` | ext-image-copy-capture |
| An output, into a dmabuf | `ImportDmabuf`, `CaptureOutputDmabuf`, `CopyOutputDmabuf` | wlr-screencopy, linux-dmabuf |
| A window or an output, into a dmabuf, as a session | `OpenDmabufSession` | ext-image-copy-capture, linux-dmabuf |

The dmabuf paths are what a screencast producer wants: no copy through shared
memory. [`dmabufprobe`](/rendering#dmabufs) picks the modifier to allocate.

A window takes one session at a time on a `Client`. While a `DmabufSession` is
open, `CaptureToplevel` and another `OpenDmabufSession` on that window fail with
`ErrToplevelBusy`.

`Frame.Image` converts to `image.RGBA` for the 8-bit and 10-bit packed formats
compositors hand out. `Options.Dst` reuses a buffer between frames, and
`Options.AwaitDamage` waits for the source to change.

## vinput, keymapfd

`vinput` injects pointer and keyboard input through `zwlr_virtual_pointer_v1` and
`zwp_virtual_keyboard_v1` on a private connection. It is the input half of a
remote-desktop bridge, and of lys-ui's headless test harness.

```go
package main

import (
	"log"

	"github.com/stubbedev/lys-lib/vinput"
)

const btnLeft = 0x110

func main() {
	dev, err := vinput.Dial("", vinput.Options{Keyboard: true})
	if err != nil {
		log.Fatal(err)
	}
	defer dev.Close()

	if err := dev.Motion(10, 0); err != nil {
		log.Fatal(err)
	}
	if err := dev.Button(btnLeft, true); err != nil {
		log.Fatal(err)
	}
	if err := dev.Button(btnLeft, false); err != nil {
		log.Fatal(err)
	}
	if err := dev.Roundtrip(); err != nil {
		log.Fatal(err)
	}
}
```

`Dial` takes an absolute socket path, a name inside `XDG_RUNTIME_DIR`, or `""`
for `WAYLAND_DISPLAY`. The seat's keymap is relayed to the virtual keyboard, so
key codes mean what they mean on the real keyboard. A seat that announces none,
such as a headless compositor's, needs `Options.FallbackKeymap`, since the
protocol refuses key events before any keymap is set. `Options.Keymap` makes the
keyboard carry a keymap of its own instead, the way wtype does. A `Device`'s methods are
safe to call from any goroutine.

`keymapfd.Read` reads the xkb keymap a `wl_keyboard.keymap` event carries. It maps
the fd and never reads it: a compositor may hand every client a dup of one shared
memfd, whose file offset a `read()` would move for all of them. Hyprland does.

## xkb

`xkb` is the XKB keyboard model in pure Go, held to libxkbcommon by differential
tests and linking no C. [Its own page](/xkb) maps every libxkbcommon function to
Go. A `Context` finds its include files the way libxkbcommon
does: `$XDG_CONFIG_HOME/xkb` or `~/.config/xkb`, `~/.xkb`, `$XKB_CONFIG_EXTRA_PATH` or
`/etc/xkb`, then `$XKB_CONFIG_ROOT` or the system's xkeyboard-config data.

```go
ctx := xkb.NewContext(os.Getenv)
km, err := ctx.NewKeymapFromString(text)
if err != nil {
	return fmt.Errorf("compile the seat keymap: %w", err)
}
state := km.NewState()
state.UpdateMask(depressed, latched, locked, 0, 0, group)
sym := state.KeyGetOneSym(xkb.Keycode(evdevCode + 8))
```

Keymap text is untrusted: a keymap that fails to compile returns an error and no
keymap, and a caller must stop reading keys through its old one. `UpdateKey`,
`KeyGetOneSym`, `SerializeMods` and `KeyGetUTF8` allocate nothing, so they are
safe on a per-key path. Compiling reads each file at most once, and a build for a
distribution sets `xkb.defaultRoot` and `xkb.defaultXLocaleDir` with `-X` where the
usual paths do not hold its data.

## wlr

`wlr` holds the generated bindings for protocols beyond the core and xdg-shell,
all in one package. The XML sits beside the generated code, and `just generate`
regenerates it with `third_party/wayland/cmd/wayland-scanner`.

| Area | Protocols |
| --- | --- |
| Shell and windows | wlr-layer-shell, ext-session-lock, wlr-foreign-toplevel-management, ext-foreign-toplevel-list, xdg-activation, xdg-dialog, xdg-foreign, xdg-output, xdg-system-bell, xdg-toplevel-icon |
| Capture | wlr-screencopy, ext-image-capture-source, ext-image-copy-capture, hyprland-toplevel-export |
| Buffers and presentation | linux-dmabuf, viewporter, fractional-scale, presentation-time, alpha-modifier, content-type, ext-background-effect |
| Input | pointer-constraints, pointer-gestures, relative-pointer, tablet, cursor-shape, keyboard-shortcuts-inhibit, hyprland-global-shortcuts, zwlr-virtual-pointer, zwp-virtual-keyboard, lys-escape |
| Clipboard | ext-data-control, wlr-data-control, primary-selection |
| Session | ext-idle-notify, idle-inhibit, wlr-gamma-control, hyprland-ctm-control |

The core protocol and xdg-shell come from
[`third_party/wayland`](/third-party/wayland), as `wl` and `xdg`, with the client
helper `wlclient` and the `text-input-v3` and `xdg-decoration-v1`
bindings under `unstable`. Every family member shares those `wl` types.

## xcursor

`xcursor` reads Xcursor files and resolves themes through `XCURSOR_PATH`, `Inherits` and
nominal sizes, refusing malformed files with an error. `Nearest` reads one image and
leaves the rest of the file unread; `NearestFrames` and `Theme.Frames` read an animated
cursor at one size. `Pixels` is the file's own bytes, which are `wl_shm`'s `argb8888`.
lys-wm draws its cursors from it and lys-ui's client sessions theirs. `Shapes`, `Names`
and `Builtin` hold cursor-shape-v1's names and the arrow for a machine without a theme.
