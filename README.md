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
