# Rendering and GPU

## render

`render` is the 2D canvas. Nothing in it touches Wayland, and everything runs
without a GPU, so it is what a program draws with when `gpu.Open` fails.

A `Canvas` paints into a buffer the caller owns: rows of `Stride(width)` bytes
of premultiplied ARGB8888 in `wl_shm` byte order. `Color` is the same layout,
`0xAARRGGBB`, and `RGB` and `RGBA` make one.

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

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	face, err := render.LoadFont(data)
	if err != nil {
		log.Fatal(err)
	}
	face.DrawAligned(cv, "lys-lib", cv.Rect(), 32, render.RGB(0xff, 0xb4, 0x54), render.AlignCenter)

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

| Area | Where to look |
| --- | --- |
| Device scale | `NewScaled` takes a fractional `num/denom` scale |
| Shapes | `Canvas.FillRect`, `FillPath`, `Arc`, `Line`, `RoundedBorder`, `BoxShadow`, with `Path` and `ParsePathData` |
| Paint | `Gradient`, CSS's sixteen `BlendMode`s, `Filters` |
| Layers | `Canvas.Layer`, `DrawLayer`, `Composite`, `CompositeProjective` |
| Clip and opacity | `PushClip`, `PushAlpha`, `PushBrightness` |
| Images | `LoadPNG`, `LoadJPEG`, `LoadSVG` and `IconFromImage` make an `Icon`; `DecodeImage` also gives an `Animation` for GIF and APNG |
| Text | `LoadFont` makes a `Typeface`; `NewChain` falls back across faces for mixed scripts; `Variation` sets a variable font's axes; `WrapText` and `EllipsizeText` fit text to a width |
| Damage | `Rect.Subtract`, `Rect.Union`, `UnionAll` |

Fonts are shaped by [go-text/typesetting](/third-party/typesetting), and SVG
goes through [oksvg](/third-party/oksvg), both patched copies. `LoadSVG`
returns an error for an element it does not support rather than dropping it.
`LoadFont` reads its tables in place, so the bytes it is given must stay valid
and unchanged for as long as the typeface is used.

### Recording

`NewRecorder` makes a canvas that records a `List` instead of painting. The
same drawing code then serves both renderers: `gpu.Device.Render` draws the
list on the GPU, and `List.Paint` rasterizes it onto a canvas exactly as painting
directly would have. `List.Play` hands its calls to any other `Player`. A list
refers to what its calls were given rather than copying it, so play it within the
frame that recorded it.

## text

`text` holds the editing rules that do not depend on a widget: `WordStart`,
`WordEnd` and `WordRun` for word motion, `NextCluster` and `PrevCluster` for
grapheme clusters, and `BidiRuns`, `VisualOrder` and `VisualStep` for
bidirectional text. A word is a run of letters, digits and underscores, by
grapheme cluster. It is deliberately not UAX #29, which would stop a
word-motion caret on every dot in `server.hostname.example`.

## gpu

`gpu` renders through Vulkan into memory shared by file descriptor, with no
copy. lys-ui's windows, lys-shell's surfaces and lys-wm's output all draw
through it.

```go
package main

import (
	"errors"
	"image/png"
	"log"
	"os"

	"github.com/stubbedev/lys-lib/gpu"
	"github.com/stubbedev/lys-lib/render"
)

func main() {
	dev, err := gpu.Open()
	if errors.Is(err, gpu.ErrNoVulkan) || errors.Is(err, gpu.ErrNoDevice) {
		log.Fatalf("no GPU here, so draw on the CPU with render.New: %v", err)
	}
	if err != nil {
		log.Fatal(err)
	}
	defer dev.Close()

	target, err := dev.NewOffscreen(320, 120)
	if err != nil {
		log.Fatal(err)
	}
	defer target.Close()

	cv := render.NewRecorder(320, 120, 1, 1)
	cv.Clear(cv.Rect(), render.RGB(0x1f, 0x21, 0x36))
	cv.FillRect(render.Rect{X: 40, Y: 40, W: 240, H: 40}, render.RGB(0xff, 0xb4, 0x54))
	if err := dev.Render(cv.Recording(), target); err != nil {
		log.Fatal(err)
	}

	img, err := target.ReadPixels()
	if err != nil {
		log.Fatal(err)
	}
	out, err := os.Create("out.png")
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, img); err != nil {
		log.Fatal(err)
	}
}
```

A `Device` and everything made from it belong to one goroutine.

`Open` picks the first device with a graphics queue that exports dmabufs, else
the first with a graphics queue. `OpenNode` opens the device behind a DRM node,
the one a dmabuf consumer negotiated.

| Error | Meaning |
| --- | --- |
| `ErrNoVulkan` | No Vulkan loader |
| `ErrNoDevice` | No device that draws |
| `ErrNoDmabuf` | The device renders offscreen only; `Exports` reports this ahead of time, and `NewTarget` fails with it |
| `ErrNoSyncFile` | The device has no `sync_file` semaphores; `SyncFiles` reports this ahead of time |
| `ErrNoFrameTime` | The device has no calibrated timestamps |
| `ErrNoVideoEncoder` | See [video encoding](/media#video-encoding) |

The kinds of image are distinct types:

| Type | Made by | What it is |
| --- | --- | --- |
| `Target` | `NewTarget`, `NewOffscreen`, `ImportTarget` | Rendered into. A window's frame, an output's scanout buffer, or offscreen pixels |
| `Texture` | `NewTexture`, `ImportTexture`, `ImportTextureYCbCr` | Sampled across frames, uploaded by the CPU or imported from a dmabuf |
| `Buffer` | `NewBuffer` | A dmabuf for others to write, such as a screencast frame; never drawn into, so layouts a device cannot render to are on offer |
| `Linear` | `ImportLinear` | An imported linear dmabuf that `CopyToLinear` copies a target into |

There are two ways to draw. `Render` draws a recorded `List`. `Begin` opens a
`Pass` for a compositor's frame, clipped to its damage, with `Clear`,
`FillRect`, `FillGradient`, `DrawTexture`, `FillGoo` and `Kawase`. The first
bad call fails the pass: `End` reports it and draws nothing.

Vulkan comes from the patched [vk](/third-party/vk) bindings. The shaders are
compiled to SPIR-V and embedded, and `just generate` rebuilds them.

## drm, fourcc, edid

`drm` issues DRM ioctls on a device node in pure Go: KMS resources, connectors,
planes, properties and blobs, dumb buffers, framebuffers, atomic commits
(`Request`, `Commit`), capabilities, and timeline syncobjs and sync files for
explicit synchronization.

`fourcc` names DRM pixel formats and format modifiers, as shared by the kernel,
GBM, EGL and linux-dmabuf.

`edid` reads a display's make, model and serial from its EDID. `LoadVendors`
loads the PNP ID database that names manufacturers, from `PNPPath`:
`LYS_PNP_IDS` when set, else hwdata's `pnp.ids`.

## dmabufs

`dmabuf.Buffer` describes a dmabuf: its size, format, modifier and planes. The
caller allocates it and keeps the plane fds open for as long as the
compositor-side buffer lives, since importing dups nothing.

`dmabufwl` builds `zwp_linux_buffer_params_v1` objects from those descriptions
and reads `zwp_linux_dmabuf_feedback_v1`'s default feedback.

`udmabuf` allocates linear, CPU-visible dmabufs through `/dev/udmabuf`, a sealed
memfd wrapped as a dmabuf. Tests and demos use it to hand the compositor dmabufs
without a GPU library.

`dmabufprobe` picks the modifier a screencopy into GPU memory uses. `Layouts`
orders the modifiers the compositor imports, linear first, then the ones the
device renders in, then the ones it only exports. `First` tries them one at a
time until a buffer allocates, imports and takes a frame, since a compositor may
advertise a tiling it then fails to import.
