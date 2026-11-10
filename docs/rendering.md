# Rendering and GPU

## render

`render` is the 2D canvas. It needs neither Wayland nor a GPU, so it is the
fallback when `gpu.Open` fails.

A `Canvas` paints into a caller-owned buffer: rows of `Stride(width)` bytes of
premultiplied ARGB8888 in `wl_shm` byte order. `Color` uses the same layout,
`0xAARRGGBB`; `RGB` and `RGBA` build one.

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

| Area | API |
| --- | --- |
| Device scale | `NewScaled` takes a fractional `num/denom` scale |
| Shapes | `Canvas.FillRect`, `FillPath`, `Arc`, `Line`, `RoundedBorder`, `BoxShadow`; `Path`, `ParsePathData` |
| Paint | `Gradient`, the sixteen CSS `BlendMode`s, `Filters` |
| Layers | `Canvas.Layer`, `DrawLayer`, `Composite`, `CompositeProjective` |
| Clip and opacity | `PushClip`, `PushAlpha`, `PushBrightness` |
| Images | `LoadPNG`, `LoadJPEG`, `LoadSVG`, `IconFromImage` make an `Icon`; `DecodeImage` also returns an `Animation` for GIF and APNG |
| Text | `LoadFont` makes a `Typeface`; `NewChain` falls back across faces; `Variation` sets variable-font axes; `WrapText`, `EllipsizeText` |
| Damage | `Rect.Subtract`, `Rect.Union`, `UnionAll` |

- Fonts are shaped by [typesetting](/third-party/typesetting); SVG uses
  [oksvg](/third-party/oksvg) and [rasterx](/third-party/rasterx).
- `LoadSVG` returns an error for an unsupported element.
- `LoadFont` reads tables in place: keep its bytes valid and unchanged while
  the typeface is in use.

### Recording

`NewRecorder` returns a canvas that records a `List` instead of painting, so
one drawing routine serves both renderers:

- `gpu.Device.Render` draws the list on the GPU.
- `List.Paint` rasterizes it onto a canvas, identical to painting directly.
- `List.Play` hands its calls to any `Player`.

A list references its inputs without copying them; play it within the frame
that recorded it.

## text

Editing rules independent of any widget. Positions are rune indexes.

| Area | API |
| --- | --- |
| Words | `WordStart`, `WordEnd`, `WordRun`, `WordBoundaries`, `IsWordRune` |
| Grapheme clusters | `NextCluster`, `PrevCluster`, `ClusterRun`, `ClusterStarts`, `SnapCluster`, `SnapClusterForward` |
| Bidirectional text (UAX #9) | `BidiRuns`, `VisualOrder`, `VisualStep`, `VisualWordStep`, `RTL` |

A word is a run of same-class grapheme clusters, the class being letters,
digits and underscore. This is not UAX #29 word segmentation: word motion
across `server.hostname.example` skips the dots.

## gpu

`gpu` renders through Vulkan into memory shared by file descriptor, without
copies.

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

- A `Device` and everything made from it belong to one goroutine.
- `Open` picks the first device with a graphics queue that exports dmabufs,
  else the first with a graphics queue. `OpenNode` opens the device behind a
  DRM node.
- `Prepare` builds pipelines on a worker; a pass that meets an unbuilt pipeline
  skips that draw and reports `Incomplete`.

| Error | Meaning |
| --- | --- |
| `ErrNoVulkan` | No Vulkan loader |
| `ErrNoDevice` | No device that draws |
| `ErrNoDmabuf` | Device renders offscreen only; `Exports` reports it, `NewTarget` fails with it |
| `ErrNoSyncFile` | No `sync_file` semaphores; `SyncFiles` reports it |
| `ErrNoFrameTime` | No calibrated timestamps |
| `ErrNoVideoEncoder` | See [video encoding](/media#video-encoding) |
| `ErrBusy` | `Render` refused a frame because too many are in flight; retry later |
| `ErrDeviceLost` | Device lost; close it and open a new one. `LossLimit` losses within `LossWindow` is the recovery bound |
| `ErrDeviceClosed` | The device is closed |

Image kinds are distinct types:

| Type | Made by | Purpose |
| --- | --- | --- |
| `Target` | `NewTarget`, `NewOffscreen`, `NewOffscreenFloat`, `ImportTarget` | Rendered into: a window frame, a scanout buffer, offscreen pixels |
| `Texture` | `NewTexture`, `ImportTexture`, `ImportTextureYCbCr` | Sampled across frames; CPU-uploaded or imported from a dmabuf |
| `Buffer` | `NewBuffer` | A dmabuf others write, such as a screencast frame; never drawn into, so non-renderable layouts are allowed |
| `Linear` | `ImportLinear` | An imported linear dmabuf; `CopyToLinear` copies a target into it |

Drawing:

- `Render` draws a recorded `List`. `RenderExplicit` does the same and returns a
  `sync_file` instead of waiting.
- `Begin` opens a `Pass` clipped to damage: `Clear`, `FillRect`,
  `FillGradient`, `DrawTexture`, `DrawFrame` (a [decoded video
  frame](/media#video-decoding)), `DrawGlass`, `DrawFragmentShader`, `FillGoo`,
  `Kawase`, `SetCut`, `SetColor`. The first failing call fails the pass; `End`
  reports it and records nothing.
- `End` records without submitting. A frame's passes go to the queue in one
  submission when `SyncFile` exports, a CPU read or `Finish` needs them,
  `FrameDone` asks, or `Flush` is called. `Submissions` counts submissions.

Vulkan comes from the patched [vk](/third-party/vk) bindings. Shaders are
embedded SPIR-V; `just generate` rebuilds them.

## drm, fourcc, edid

- `drm` issues DRM ioctls in pure Go: KMS resources, connectors, planes,
  properties and blobs, dumb buffers, framebuffers, atomic commits (`Request`,
  `Commit`), capabilities, CRTC vblank sequences (`CRTCSequence`), timeline
  syncobjs and sync files.
- `fourcc` names DRM pixel formats and modifiers.
- `edid` parses make, model and serial (`Parse`), colour and HDR data
  (`ParseHDR`) and the variable refresh range (`ParseVRR`). `LoadVendors` reads
  the PNP ID database from `PNPPath`: `LYS_PNP_IDS` if set, else hwdata's
  `pnp.ids`.

## dmabufs

- `dmabuf.Buffer` describes a dmabuf: size, format, modifier, planes. Keep the
  plane fds open while the compositor-side buffer lives; importing does not dup
  them. `BeginCPUWrite`, `EndCPUWrite` and `ImportSyncFile` handle CPU access
  and implicit-sync fences.
- `dmabufwl` builds `zwp_linux_buffer_params_v1` objects (`Params`) and reads
  `zwp_linux_dmabuf_feedback_v1`: the default feedback (`DefaultFeedback`) or a
  surface's (`WatchSurfaceFeedback`), parsed into tranches with target devices
  and scanout flags. `Feedback.Preferred` picks the tranche to allocate from,
  scanout first.
- `udmabuf` allocates linear, CPU-visible dmabufs from a sealed memfd through
  `/dev/udmabuf`, without a GPU library.
- `dmabufprobe` picks the modifier for a screencopy into GPU memory. `Layouts`
  orders the compositor's import modifiers: linear, then device-renderable, then
  export-only. `First` tries each until a buffer allocates, imports and
  captures a frame; `ErrNoLayout` means none is shared.

## motion

Animation maths, bit-identical across architectures.

- `SpringStep` and `SpringPosition`: closed-form damped harmonic oscillator
  from any position and velocity, so a retargeted spring keeps its motion.
- `NewCubicBezier`: the CSS timing function, clamping time controls as CSS does.
