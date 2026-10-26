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
| `debug` | The compile-time trace facility (build tag `lysdebug`, categories in `LYS_DEBUG`) |
| `golden`, `gputest`, `wltest` | Test support: golden images, the GPU test driver, Wayland test clients |

## Development

`nix develop` (or direnv) enters the dev shell; `just` lists the
recipes. `just check` runs the release gates, `just gpu` the GPU tests
on lavapipe, `just headless` the capture and virtual input tests
against a private headless sway, and `just gpu-host` every GPU path on
the host's own GPU.

The packages moved here from lys-ui with their history; issue numbers
in that history are lys-ui's.

