<p align="center">
  <img src="docs/public/lys-lib.svg" width="160" alt="lys-lib">
</p>

<h1 align="center">lys-lib</h1>

<p align="center">
  The lys family's building blocks that are not widgets: what
  <a href="https://github.com/stubbedev/lys-ui">lys-ui</a> draws with and what
  <a href="https://github.com/stubbedev/lys-shell">lys-shell</a> talks to the
  system through.
</p>

<p align="center">
  <a href="https://stubbedev.github.io/lys-lib/">Documentation</a>
</p>

- **Rendering:** the 2D canvas and its CPU and Vulkan renderers.
- **GPU:** Vulkan devices, exportable dmabufs, and H.264/H.265 encoding.
- **Media:** MP4/Matroska muxing, the Opus encoder, PCM mixing and V4L2 webcam capture.
- **Wayland:** screen capture, virtual input and the protocol bindings they use.
- **System services:** the PipeWire native-protocol client, the EIS server, and D-Bus on a patched godbus.

Pure Go, `CGO_ENABLED=0`. Native libraries are dlopened at run time only where
the system provides the API: libvulkan, and optionally libva and libva-drm for
video encoding.

## Install

```sh
go get github.com/stubbedev/lys-lib@main
```

## Usage

Each package is imported on its own. This draws on the CPU and writes a PNG:

```go
package main

import (
	"image/png"
	"log"
	"os"

	"github.com/stubbedev/lys-lib/render"
)

func main() {
	const width, height = 320, 120
	stride := render.Stride(width)
	pixels := make([]byte, stride*height)

	cv := render.New(pixels, stride, width, height)
	cv.Clear(cv.Rect(), render.RGB(0x1f, 0x21, 0x36))
	cv.FillRect(render.Rect{X: 40, Y: 40, W: 240, H: 40}, render.RGB(0xff, 0xb4, 0x54))

	out, err := os.Create("out.png")
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, render.NRGBA(pixels, stride, width, height)); err != nil {
		log.Fatal(err)
	}
}
```

## Documentation

[stubbedev.github.io/lys-lib](https://stubbedev.github.io/lys-lib/) has the
package map, a page per domain, the third-party patch policy and the development
gates. The API reference is on
[pkg.go.dev](https://pkg.go.dev/github.com/stubbedev/lys-lib).

## The lys family

[lys-shell](https://stubbedev.github.io/lys-shell/),
[lys-wm](https://stubbedev.github.io/lys-wm/),
[lys-ui](https://stubbedev.github.io/lys-ui/),
[lys-login](https://stubbedev.github.io/lys-login/),
[lys-splash](https://stubbedev.github.io/lys-splash/),
[lys-polkit](https://stubbedev.github.io/lys-polkit/) and
[lys-secrets](https://stubbedev.github.io/lys-secrets/).
