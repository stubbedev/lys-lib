# Overview and package map

lys-lib holds the lys family's building blocks that are not widgets:
what [lys-ui](https://github.com/stubbedev/lys-ui) draws with and what
[lys-shell](https://github.com/stubbedev/lys-shell) talks to the system
through. The other family members import it too. It is a library: there is
no binary to run, and each package is imported on its own.

## Install

```sh
go get github.com/stubbedev/lys-lib@main
```

The family tracks `main`, and the `v0.1.0` tag is far behind it. The Go
version is the one in `go.mod`. The packages are written for Linux: DRM,
dmabufs, Wayland, PipeWire and V4L2.

Signatures live in the
[API reference](https://pkg.go.dev/github.com/stubbedev/lys-lib) and in
`go doc`. These pages say what each package is for and how the pieces fit.

## Pure Go

Everything builds with `CGO_ENABLED=0`. A native library loads at run time
through [purego](https://github.com/ebitengine/purego) only where the system
is the one that provides the API:

| Library | Loaded by | If it is missing |
| --- | --- | --- |
| `libvulkan.so.1` | `third_party/vk`, for `gpu` | `gpu.Open` fails with `ErrNoVulkan`, and the caller draws on the CPU with `render` |
| `libva.so.2`, `libva-drm.so.2` | `internal/va`, for the `gpu` encoder | Optional. VA-API stays off, and `gpu.Encoder` needs the device's Vulkan video queue instead |

`LYS_LIBVULKAN` names another Vulkan loader, and a build can pin one
(see [vk](/third-party/vk#the-loader-path-can-be-pinned)).

Test support loads two more through `clib`: `libpipewire-0.3.so.0` in
`pipewire/pwtest` and `libei.so.1` in `eis`'s tests. They are the reference
clients the code is checked against, and `pipewire` and `eis` themselves link
neither.

## Package map

### Rendering and GPU

| Package | What it is |
| --- | --- |
| [`render`](/rendering#render) | The 2D canvas and its CPU rasterizer |
| [`text`](/rendering#text) | Word and grapheme motion, bidirectional runs |
| [`gpu`](/rendering#gpu) | Vulkan devices, the GPU renderer, exportable dmabufs |
| [`motion`](/rendering#motion) | The spring and cubic-bezier solvers both animation engines share |
| [`drm`, `fourcc`, `edid`](/rendering#drm-fourcc-edid) | DRM ioctls, pixel formats and modifiers, a display's identity |
| [`dmabuf`, `dmabufwl`, `udmabuf`, `dmabufprobe`](/rendering#dmabufs) | dmabuf descriptions, their protocol plumbing, memfd-backed allocation, modifier probing |

### Wayland

| Package | What it is |
| --- | --- |
| [`capture`](/wayland#capture) | Screen and window capture on a private connection |
| [`vinput`, `keymapfd`](/wayland#vinput-keymapfd) | Virtual pointer and keyboard input, reading a keymap fd |
| [`xcursor`](/wayland#xcursor) | Xcursor files and cursor themes resolved through `XCURSOR_PATH` and `Inherits` |
| [`xkb`](/xkb) | The XKB keyboard model in pure Go: keymaps in both text formats, keyboard state, keysyms, Compose, X11 keymap import and the ruleset registry |
| [`wlr`](/wayland#wlr) | Protocol bindings beyond the core and xdg-shell |
| [`third_party/wayland`](/third-party/wayland) | The core client library and the scanner |

### Media

| Package | What it is |
| --- | --- |
| [`gpu.Encoder`](/media#video-encoding) | H.264 and H.265 hardware encoding |
| [`media/mux`](/media#media-mux) | MP4 and Matroska muxing |
| [`media/opus`](/media#media-opus) | The Opus encoder |
| [`media/vorbis`](/media#media-vorbis) | The Ogg Vorbis decoder |
| [`audiomix`](/media#audiomix) | Sums 16-bit PCM sources into one stream |
| [`v4l2`](/media#v4l2) | Webcam capture |

### System services

| Package | What it is |
| --- | --- |
| [`pipewire`](/system#pipewire) | A ScreenCast video producer over PipeWire's native protocol |
| [`eis`](/system#eis) | An EIS server, the receiving end of libei |
| [`dbusx`](/system#dbusx) | D-Bus services on [patched godbus](/third-party/godbus) |
| [`portaldialogs`](/system#portaldialogs) | The `dev.stubbe.lys.PortalDialogs1` contract |
| [`clib`, `intconv`, `atomicfile`, `xdg`, `peercred`, `debug`](/system#small-helpers) | Small helpers every process shares |

### Test support

[`golden`, `gputest`, `wltest`, `pipewire/pwtest`, `dbustest`, `sockdir`](/development#test-support)
are importable from other modules' tests.

### Third party

[`third_party/`](/third-party/) carries patched copies of godbus, Go's `math`, oksvg, rasterx,
go-text/typesetting, x/image/vector, the Vulkan bindings and the Wayland client library, as
packages of this module.
