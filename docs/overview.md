# Overview and package map

lys-lib is a Go library of non-widget building blocks for the lys family. It
has no binary; import each package on its own. It targets Linux.

```sh
go get github.com/stubbedev/lys-lib@latest
```

The Go version is the one in `go.mod`. Signatures are in the
[API reference](https://pkg.go.dev/github.com/stubbedev/lys-lib).

## Native libraries

Everything builds with `CGO_ENABLED=0`. These libraries are dlopened at run time
through [purego](https://github.com/ebitengine/purego):

| Library | Loaded by | If missing |
| --- | --- | --- |
| `libvulkan.so.1` | `third_party/vk`, for `gpu` | `gpu.Open` fails with `ErrNoVulkan`; draw on the CPU with `render` |
| `libva.so.2`, `libva-drm.so.2` | `internal/va`, for `gpu.Encoder` | Optional; the encoder needs the device's Vulkan video queue instead |
| `libpam.so.0`, libc | `pam` | `pam.Start` and `pam.CheckLibrary` fail |

`LYS_LIBVULKAN` names another Vulkan loader, and a build can pin one
(see [vk](/third-party/vk#the-loader-path-can-be-pinned)).

Test helpers load `libpipewire-0.3.so.0` (`pipewire/pwtest`) and `libei.so.1`
(`eis/eistest`) as reference clients. `pipewire` and `eis` link neither.

## Package map

### Rendering and GPU

| Package | Purpose |
| --- | --- |
| [`render`](/rendering#render) | 2D canvas and CPU rasterizer |
| [`text`](/rendering#text) | Word and grapheme motion, bidirectional runs |
| [`gpu`](/rendering#gpu) | Vulkan devices, GPU renderer, dmabuf export |
| [`motion`](/rendering#motion) | Spring and cubic-bezier solvers |
| [`drm`, `fourcc`, `edid`](/rendering#drm-fourcc-edid) | DRM ioctls, pixel formats and modifiers, EDID parsing |
| [`dmabuf`, `dmabufwl`, `udmabuf`, `dmabufprobe`](/rendering#dmabufs) | dmabuf descriptions, linux-dmabuf plumbing, memfd-backed allocation, modifier probing |

### Wayland

| Package | Purpose |
| --- | --- |
| [`capture`](/wayland#capture) | Screen, window and cursor capture |
| [`vinput`, `keymapfd`](/wayland#vinput-keymapfd) | Virtual pointer, keyboard and touch; reading a keymap fd |
| [`xcursor`](/wayland#xcursor) | Xcursor files and themes |
| [`xkb`](/xkb) | The XKB keyboard model, plus `xkb/registry` and `xkb/x11` |
| [`wlr`](/wayland#wlr) | Protocol bindings beyond the core and xdg-shell |
| [`third_party/wayland`](/third-party/wayland) | Core client library and scanner |
| `protocols` | The Wayland protocol XML every lys binding is generated from |

### Media

| Package | Purpose |
| --- | --- |
| [`gpu.Encoder`](/media#video-encoding) | H.264 and H.265 hardware encoding |
| [`media/mux`](/media#media-mux) | Fragmented MP4 and Matroska muxing |
| [`media/demux`](/media#media-demux) | MP4 and Matroska demuxing with keyframe seeking |
| [`media/opus`](/media#media-opus) | Opus encoder and decoder (CELT, SILK, hybrid, multistream) |
| [`media/aac`](/media#media-aac) | AAC-LC decoder |
| [`media/vorbis`](/media#media-vorbis) | Vorbis decoder, Ogg files or packets |
| [`audiomix`](/media#audiomix) | 16-bit PCM mixer |
| [`v4l2`](/media#v4l2) | Webcam capture |

### System services

| Package | Purpose |
| --- | --- |
| [`pipewire`](/system#pipewire) | ScreenCast video producer over PipeWire's native protocol |
| [`eis`](/system#eis) | EIS server (the receiving end of libei) |
| [`dbusx`](/system#dbusx) | D-Bus services, signal watches and the system-bus dial on [patched godbus](/third-party/godbus) |
| [`pulse/native`](/system#pulse-native) | PulseAudio native-protocol client: introspection, control, subscriptions, record and playback streams, sample cache |
| [`logind`](/system#logind) | systemd-logind client: sessions, power, inhibitors, devices, lock signals |
| [`polkit`](/system#polkit) | polkitd Authority client: typed subjects, authorization checks with cancellation, agent registration |
| [`portaldialogs`](/system#portaldialogs) | The `dev.stubbe.lys.PortalDialogs1` contract |
| [`wmipc`](/system#wmipc) | The wire contract of lys-wm's IPC socket |
| [`pam`](/system#pam) | libpam transactions |
| [`appearance`, `portalsettings`](/system#appearance-portalsettings) | Desktop preferences as xdg-desktop-portal publishes them |
| [`icons`, `icontheme`](/system#icons-icontheme) | Freedesktop icon theme lookup and raster cache |
| [`i18n`](/system#i18n) | gettext catalogs in pure Go |
| [`glibclocale`](/system#glibclocale) | glibc's compiled locale data and strftime in pure Go |
| `vaultserver` | A validated Bitwarden server address |
| [`clib`, `intconv`, `atomicfile`, `assetfile`, `xdg`, `peercred`, `debug`, `logutil`, `testbuild`](/system#small-helpers) | Shared helpers |

### Test support

[`golden`, `gputest`, `wltest`, `pipewire/pwtest`, `eis/eistest`, `dbustest`, `pulse/pulsetest`, `logind/logindtest`, `polkit/polkittest`, `peercred/peercredtest`, `sockdir`](/development#test-support)
can be imported from other modules' tests.

### Third party

[`third_party/`](/third-party/) holds patched copies of godbus, Go's `math`,
oksvg, rasterx, go-text/typesetting, x/image/vector, the Vulkan bindings and the
Wayland client library.
