# lys-lib

The lys family's building blocks that are not widgets: what
[lys-ui](https://github.com/stubbedev/lys-ui) draws with and what
[lys-shell](https://github.com/stubbedev/lys-shell) talks to the system
through.

- **Rendering:** the 2D canvas and its CPU and Vulkan renderers.
- **GPU:** Vulkan devices, exportable dmabufs, and H.264/H.265 encoding.
- **Media:** MP4/Matroska muxing, the Opus encoder, PCM mixing and V4L2 webcam capture.
- **Wayland:** screen capture, virtual input and the protocol bindings they use.
- **System services:** the PipeWire native-protocol client, the EIS server, and D-Bus on a patched godbus.

Pure Go, `CGO_ENABLED=0`. Native libraries are dlopened at runtime only
where the system provides the API: libvulkan.

## Packages

| Package | What it is |
| --- | --- |
| `render` | The 2D canvas: paths, text (with `text`), images, SVG, filters and blends, on the CPU |
| `gpu` | Vulkan devices, the GPU renderer, exportable dmabufs, and H.264/H.265 encoding |
| `drm`, `fourcc`, `edid` | DRM ioctls in pure Go (KMS resources, atomic commits, dumb buffers, syncobjs), DRM pixel formats and modifiers, and a display's identity from its EDID |
| `dmabuf`, `dmabufwl`, `udmabuf` | dmabuf descriptions, their linux-dmabuf protocol plumbing, and memfd-backed udmabufs |
| `capture` | Screen and window capture: wlr-screencopy, ext-image-copy-capture, Hyprland's toplevel export |
| `dmabufprobe` | Picks the DRM modifier a screencopy into GPU memory uses, trying the compositor's in order until one takes a frame |
| `vinput` | Virtual pointer and keyboard input (with `keymapfd`) |
| `wlr` | Wayland protocol bindings beyond the core and xdg-shell |
| `media/mux`, `media/opus` | MP4 and Matroska muxing, and the Opus encoder |
| `audiomix` | Sums interleaved 16-bit PCM sources into one stream, with clipping |
| `v4l2` | Webcam capture from V4L2 nodes, as RGBA frames letterboxed to an aspect; the system's capture nodes |
| `pipewire` | The ScreenCast video producer: a node over PipeWire's native protocol, no libpipewire |
| `eis` | An EIS server: the receiving end of libei, replaying what a remote-desktop client emulates |
| `dbusx`, `third_party/godbus` | D-Bus services: interfaces with live properties, setters and on-demand introspection, well-known names and owner watches, on a patched godbus (`third_party/godbus/README.lys-lib.md`) |
| `portaldialogs` | The `dev.stubbe.lys.PortalDialogs1` contract: the shell's portal dialogs (Access, Account, app chooser, dynamic launcher, USB, wallpaper, password) as request types, a `Host` interface, its `Daemon` and a `Client`, plus the `Caller` and `Choice` every portal dialog is given |
| `clib` | dlopen over a candidate list and C string copies, for bindings made with purego |
| `intconv` | Integer conversions without silent wraparound: checked, panicking, saturating, durations, and two's-complement bit reinterpretation |
| `atomicfile` | Replaces a file whole and durably: a synced temporary renamed over the target, then the directory synced |
| `xdg` | The XDG base directories, an app's directory under each, and the user directories of `user-dirs.dirs` |
| `debug` | The compile-time trace facility (build tag `lysdebug`, categories in `LYS_DEBUG`) |
| `golden`, `gputest`, `wltest`, `pipewire/pwtest`, `dbustest`, `sockdir` | Test support: golden images, the GPU test driver, Wayland test clients, a private PipeWire daemon with libpipewire consumers, private D-Bus daemons, and directories short enough for Unix sockets |

## Development

`nix develop` (or direnv) enters the dev shell; `just` lists the
recipes. `just gates` runs every gate CI runs: formatting, then `just
check` (vet, lint, test, build), the race detector, `just gpu` (the GPU
tests on lavapipe) and `just headless` (the capture and virtual input
tests against a private headless sway). `just gpu-host` runs every GPU
path on the host's own GPU.

`just docker <recipe>` (default `gates`) runs a recipe in a container
that sees the dev shell's store paths, the checkout, the GPUs' render
and NVIDIA device nodes and the host's drivers, but no display socket,
session bus or runtime dir of the desktop session, with memory capped.
`nix fmt` formats the nix files with alejandra.

## CI

`.github/workflows/ci.yml` runs `just gates` in the dev shell.
`.github/workflows/security.yml` runs CodeQL (Go and GitHub Actions),
Grype and govulncheck, uploading findings to code scanning; dependency
review gates pull requests against a licence allow-list. Dependabot
keeps the module and the workflows current. Every action is pinned to
a commit.

The packages moved here from lys-ui and lys-shell with their history;
issue numbers in that history name their repository
(`stubbedev/lys-ui#N`, `stubbedev/lys-shell#N`).

