# lys-lib

The lys family's building blocks that are not widgets: what
[lys-ui](https://github.com/stubbedev/lys-ui) draws with and what
[lys-shell](https://github.com/stubbedev/lys-shell) talks to the system
through.

- **Rendering:** the 2D canvas and its CPU and Vulkan renderers.
- **GPU:** Vulkan devices, exportable dmabufs, and H.264/H.265 encoding.
- **Media:** MP4/Matroska muxing and the Opus encoder.
- **Wayland:** screen capture, virtual input and the protocol bindings they use.
- **System services:** the PipeWire native-protocol client and the EIS server.

Pure Go, `CGO_ENABLED=0`. Native libraries are dlopened at runtime only
where the system provides the API: libvulkan.

## Packages

| Package | What it is |
| --- | --- |
| `render` | The 2D canvas: paths, text (with `text`), images, SVG, filters and blends, on the CPU |
| `gpu` | Vulkan devices, the GPU renderer, exportable dmabufs, and H.264/H.265 encoding |
| `dmabuf`, `dmabufwl`, `udmabuf` | dmabuf descriptions, their linux-dmabuf protocol plumbing, and memfd-backed udmabufs |
| `capture` | Screen and window capture: wlr-screencopy, ext-image-copy-capture, Hyprland's toplevel export |
| `vinput` | Virtual pointer and keyboard input (with `keymapfd`) |
| `wlr` | Wayland protocol bindings beyond the core and xdg-shell |
| `media/mux`, `media/opus` | MP4 and Matroska muxing, and the Opus encoder |
| `pipewire` | The ScreenCast video producer: a node over PipeWire's native protocol, no libpipewire |
| `eis` | An EIS server: the receiving end of libei, replaying what a remote-desktop client emulates |
| `clib` | dlopen over a candidate list and C string copies, for bindings made with purego |
| `debug` | The compile-time trace facility (build tag `lysdebug`, categories in `LYS_DEBUG`) |
| `golden`, `gputest`, `wltest`, `pipewire/pwtest`, `sockdir` | Test support: golden images, the GPU test driver, Wayland test clients, a private PipeWire daemon with libpipewire consumers, and directories short enough for Unix sockets |

## Development

`nix develop` (or direnv) enters the dev shell; `just` lists the
recipes. `just check` runs the release gates, `just gpu` the GPU tests
on lavapipe, `just headless` the capture and virtual input tests
against a private headless sway, and `just gpu-host` every GPU path on
the host's own GPU.

The packages moved here from lys-ui and lys-shell with their history;
issue numbers in that history name their repository
(`stubbedev/lys-ui#N`, `stubbedev/lys-shell#N`).

