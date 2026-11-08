<p align="center">
  <img src="docs/public/lys-lib.svg" width="160" alt="lys-lib">
</p>

<h1 align="center">lys-lib</h1>

<p align="center">
  Non-widget building blocks for the lys family, in pure Go.
</p>

<p align="center">
  <a href="https://stubbedev.github.io/lys-lib/">Documentation</a>
</p>

- **Rendering and GPU:** a 2D canvas with CPU and Vulkan renderers, dmabufs, DRM.
- **Wayland:** screen capture, virtual input, XKB, protocol bindings.
- **Media:** H.264/H.265 hardware encoding, MP4/Matroska muxing, Opus, Vorbis, V4L2.
- **System:** a PipeWire video producer, an EIS server, D-Bus helpers, PAM.

Builds with `CGO_ENABLED=0`. libvulkan, libva and libpam are dlopened at run
time where used.

## Install

```sh
go get github.com/stubbedev/lys-lib@latest
```

## Example

```go
stride := render.Stride(320)
pixels := make([]byte, stride*120)
cv := render.New(pixels, stride, 320, 120)
cv.Clear(cv.Rect(), render.RGB(0x1f, 0x21, 0x36))
cv.FillRect(render.Rect{X: 40, Y: 40, W: 240, H: 40}, render.RGB(0xff, 0xb4, 0x54))
img := render.NRGBA(pixels, stride, 320, 120)
```

## Documentation

- [stubbedev.github.io/lys-lib](https://stubbedev.github.io/lys-lib/): package map, a page per domain, patch policy, development.
- [pkg.go.dev](https://pkg.go.dev/github.com/stubbedev/lys-lib): API reference.

## The lys family

[lys-shell](https://stubbedev.github.io/lys-shell/),
[lys-wm](https://stubbedev.github.io/lys-wm/),
[lys-ui](https://stubbedev.github.io/lys-ui/),
[lys-login](https://stubbedev.github.io/lys-login/),
[lys-splash](https://stubbedev.github.io/lys-splash/),
[lys-polkit](https://stubbedev.github.io/lys-polkit/),
[lys-secrets](https://stubbedev.github.io/lys-secrets/).
