# Wayland

Toolkit windows and surfaces are in [lys-ui](https://github.com/stubbedev/lys-ui).
These packages need no toolkit. The keyboard model is on its [own page](/xkb).

## capture

`capture` copies screen contents on a private Wayland connection, independent of
the application's own event loop.

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

- Capture calls block until the compositor finishes the copy; keep them off the
  UI goroutine.
- A `Client` is safe for concurrent use, but its calls serialize. Use separate
  clients for overlapping captures.
- `Connect` (or `ConnectTo` with a socket) succeeds on any compositor. The
  `Has*` methods report what is offered; a capture whose protocol is missing
  fails with `ErrUnsupported`.

| Capture | API | Protocol |
| --- | --- | --- |
| Output or region, to memory | `CaptureOutput`, `CaptureOutputRegion` | wlr-screencopy |
| One output frame, to memory | `CaptureOutputOnce` | ext-image-copy-capture |
| Window, to memory | `Toplevels`, `CaptureToplevel` | ext-foreign-toplevel-list, ext-image-copy-capture |
| Window on Hyprland | `CaptureHyprlandWindow` | hyprland-toplevel-export |
| Compositor's cast target | `CaptureDynamic`, `Dynamic` | lys-dynamic-capture |
| Output, continuous and damage-driven | `OpenOutputStream` | ext-image-copy-capture |
| Output or region, to a dmabuf | `ImportDmabuf`, `DmabufFormat`, `CaptureOutputDmabuf`, `CopyOutputDmabuf`, `CopyOutputRegionDmabuf` | wlr-screencopy, linux-dmabuf |
| Output, window or cast target, to a dmabuf, as a session | `OpenDmabufSession` | ext-image-copy-capture, linux-dmabuf |
| Pointer position and image | `OpenOutputCursor`, `OpenToplevelCursor`, `OpenDynamicCursor` | ext-image-copy-capture |

- The dmabuf paths avoid shared-memory copies;
  [`dmabufprobe`](/rendering#dmabufs) picks the modifier.
- A window takes one session per `Client`. While a `DmabufSession` is open on it,
  `CaptureToplevel` and another `OpenDmabufSession` fail with `ErrToplevelBusy`.
  `Dynamic` sessions may overlap; a size change reports `ErrReconfigured`.
- `Frame.Image` converts the 8-bit and 10-bit packed formats to `image.RGBA`.

`Options`:

| Field | Effect |
| --- | --- |
| `Cursor` | Composite the pointer into the frame |
| `Intent` | `Screencast` (default) or `Screenshot`; `Screenshot` is declared through lys-capture-hint so the compositor may show surfaces it hides from screen sharing |
| `Dst` | Reuse this buffer for the frame's pixels when large enough |
| `AwaitDamage` | Wait for the source to change and report the damage in `Frame.Damage` |

Streams and dmabuf sessions track per-buffer damage and ask the compositor to
copy only what changed since that buffer was last filled.

## vinput, keymapfd

`vinput` injects input on a private connection through
`zwlr_virtual_pointer_v1`, `zwp_virtual_keyboard_v1` and `lys_virtual_touch_v1`.

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
for `WAYLAND_DISPLAY`. A `Device` is safe to use from any goroutine.

| Option | Effect |
| --- | --- |
| `Keyboard` | Also create a virtual keyboard. The seat's keymap is relayed to it, so key codes match the real keyboard |
| `Touch` | Also create a touchscreen spanning the output layout; needs `lys_virtual_touch_manager_v1` (`TouchAvailable` checks, `ErrNoTouch` otherwise) |
| `FallbackKeymap` | xkb_v1 keymap used when the seat announces none, such as on a headless compositor |
| `Keymap` | xkb_v1 keymap the keyboard carries instead of the seat's; overrides `FallbackKeymap` |

`keymapfd.Read` reads the keymap a `wl_keyboard.keymap` event carries. It maps
the fd and never calls `read()`, which would move a shared memfd's offset for
every client holding it.

## wlr

`wlr` holds generated bindings for protocols beyond the core and xdg-shell, in
one package. The XML lives beside the code; `just generate` regenerates it with
`third_party/wayland/cmd/wayland-scanner`.

| Area | Protocols |
| --- | --- |
| Shell and windows | wlr-layer-shell, ext-session-lock, wlr-foreign-toplevel-management, ext-foreign-toplevel-list, xdg-activation, xdg-dialog, xdg-foreign, xdg-output, xdg-system-bell, xdg-toplevel-icon |
| Outputs | wlr-output-management, wlr-output-power-management |
| Capture | wlr-screencopy, ext-image-capture-source, ext-image-copy-capture, hyprland-toplevel-export, lys-capture-hint, lys-dynamic-capture |
| Buffers and presentation | linux-dmabuf, linux-drm-syncobj, viewporter, fractional-scale, presentation-time, alpha-modifier, color-representation, content-type, ext-background-effect |
| Input | pointer-constraints, pointer-gestures, relative-pointer, tablet, cursor-shape, keyboard-shortcuts-inhibit, hyprland-global-shortcuts, xx-hotkey, zwlr-virtual-pointer, zwp-virtual-keyboard, lys-virtual-touch, lys-escape |
| Clipboard | ext-data-control, wlr-data-control, primary-selection |
| Session | ext-idle-notify, idle-inhibit, wlr-gamma-control, hyprland-ctm-control |

The core protocol and xdg-shell come from
[`third_party/wayland`](/third-party/wayland) as `wl` and `xdg`, with the
client helper `wlclient` and the `text-input-v3` and `xdg-decoration-v1`
bindings under `unstable`.

## xcursor

`xcursor` reads Xcursor files and resolves themes through `XCURSOR_PATH`
(`SearchPath`), `Inherits` and nominal sizes. Malformed files are an error.

| API | Returns |
| --- | --- |
| `Open` | A `Theme` |
| `Nearest` | The image nearest a size, reading only that image |
| `Widest`, `ReadWidest` | The widest image |
| `NearestFrames`, `Theme.Frames` | An animated cursor's frames at one size |
| `Builtin` | A built-in arrow for systems without a theme |
| `Shapes` | cursor-shape-v1 shapes in protocol order, with their Xcursor names |
| `Names` | Xcursor names to try for a CSS cursor name, best first |

`Image.Pixels` holds the pixels as the file stores them, which is `wl_shm`
`argb8888`.
