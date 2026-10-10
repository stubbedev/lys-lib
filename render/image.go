package render

import (
	"image"

	xdraw "golang.org/x/image/draw"
)

// ImageScale picks how an image fills the box its widget was arranged
// into.
type ImageScale uint8

const (
	// ImageFit scales the image to the largest size that fits the box
	// with the aspect ratio kept (contain); the rest of the box stays
	// transparent. The default.
	ImageFit ImageScale = iota
	// ImageCover scales the image until it fills the box completely,
	// aspect ratio kept, cropping the overflow: the largest centered
	// crop of the source with the box's aspect ratio, resampled to the
	// full box.
	ImageCover
	// ImageNone draws the image at its natural size, one source pixel
	// per device pixel, centered in the box and clipped to it. Nothing
	// is resampled, so the pixels stay exactly as decoded - at a 2x
	// device scale an image covers half the logical size it would at
	// 1x.
	ImageNone
	// ImageStretch resamples the whole image to exactly the box,
	// ignoring the aspect ratio.
	ImageStretch
	// ImageScaleDown is ImageNone for an image that fits the box and
	// ImageFit for one that does not: never enlarged, only shrunk (aspect
	// kept) until it fits - GTK's ContentFit.SCALE_DOWN, with the natural
	// size measured in device pixels like ImageNone.
	ImageScaleDown
)

// roundDiv divides a by b, rounding halves up on the positive values
// the scaling arithmetic produces.
func roundDiv(a, b int) int { return (a + b/2) / b }

// ScaleRect resolves a scaling policy for a srcW x srcH image into a
// boxW x boxH box. Both sizes are in the same units - widget.Image
// feeds device pixels so the result stays crisp at fractional scales.
// It returns the source rectangle to sample and the size to resample
// it into; drawing that resample centered in the box is the whole
// policy. An empty result means a zero-sized input.
func ScaleRect(srcW, srcH, boxW, boxH int, s ImageScale) (src image.Rectangle, dstW, dstH int) {
	if srcW <= 0 || srcH <= 0 || boxW <= 0 || boxH <= 0 {
		return image.Rectangle{}, 0, 0
	}
	switch s {
	case ImageCover:
		// scale = max(boxW/srcW, boxH/srcH): the visible crop of the
		// source is the box divided by the scale, centered. The branch
		// decides which axis binds; the other side's crop follows from
		// the box ratio. The floor at 1 keeps extreme aspect ratios from
		// rounding the crop away entirely.
		cropW, cropH := srcW, srcH
		if boxW*srcH >= boxH*srcW {
			cropH = min(srcH, max(1, roundDiv(srcW*boxH, boxW)))
		} else {
			cropW = min(srcW, max(1, roundDiv(srcH*boxW, boxH)))
		}
		return image.Rect((srcW-cropW)/2, (srcH-cropH)/2, (srcW-cropW)/2+cropW, (srcH-cropH)/2+cropH), boxW, boxH
	case ImageStretch:
		return image.Rect(0, 0, srcW, srcH), boxW, boxH
	case ImageScaleDown:
		if srcW <= boxW && srcH <= boxH {
			return image.Rect(0, 0, srcW, srcH), srcW, srcH
		}
		return ScaleRect(srcW, srcH, boxW, boxH, ImageFit)
	case ImageNone:
		// No scaling: the box shows the centered natural-size pixels,
		// clipped where the source is bigger than the box.
		w, h := min(srcW, boxW), min(srcH, boxH)
		x, y := (srcW-w)/2, (srcH-h)/2
		return image.Rect(x, y, x+w, y+h), w, h
	default: // ImageFit
		// scale = min(boxW/srcW, boxH/srcH), rounded; the rounding
		// cannot push the result past the box because the real value
		// lies strictly inside it on the non-binding axis. The floor at
		// 1 keeps a thin source visible in a wide box.
		if boxW*srcH < boxH*srcW {
			return image.Rect(0, 0, srcW, srcH), boxW, min(boxH, max(1, roundDiv(srcH*boxW, srcW)))
		}
		return image.Rect(0, 0, srcW, srcH), min(boxW, max(1, roundDiv(srcW*boxH, srcH))), boxH
	}
}

// Resample scales srcRect of src into a fresh dstW x dstH RGBA with the
// CatmullRom kernel. Cubic resampling is what keeps fractional and
// non-integer down- and upscales from the blockiness of a
// nearest-neighbor pass (stubbedev/gelm#14); the canvas's own image
// drawing is nearest-neighbor and must never see unresampled pixels.
func Resample(src image.Image, srcRect image.Rectangle, dstW, dstH int) *image.RGBA {
	if dstW <= 0 || dstH <= 0 {
		return image.NewRGBA(image.Rectangle{})
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	r := srcRect.Intersect(src.Bounds())
	if r.Empty() {
		return dst // a fully transparent raster of the requested size
	}
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, r, xdraw.Src, nil)
	return dst
}

// ResampleInto scales srcRect of src over all of dst bilinearly,
// reusing dst's pixels: the per-frame path for video, where a cubic
// kernel and a fresh raster per frame would cost more than the frame
// budget allows.
func ResampleInto(dst, src *image.RGBA, srcRect image.Rectangle) {
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, srcRect.Intersect(src.Bounds()), xdraw.Src, nil)
}

// DrawImageDevice blends img onto the canvas one-to-one in device
// pixels, its top-left corner at (x, y). It is the device-space
// counterpart of DrawImage: rasters that were already resampled for the
// device rect they cover - widget.Image's cache - must land without a
// second scale, which DrawImage's nearest-neighbor logical mapping
// would apply at any device scale other than 1.
//
// Opacity (PushAlpha) modulates here, at blit time, and nowhere earlier:
// the cached rasters are premultiplied and shared by every consumer, so
// baking a factor into them would double-multiply the next frame and
// bleed into widgets fading independently. Multiplying the premultiplied
// channels together (blend then modulate) keeps a modulated raster
// premultiplied, exactly like the AA coverage ramps.
func (c *Canvas) DrawImageDevice(img image.Image, x, y int) {
	b := img.Bounds()
	if b.Empty() {
		return
	}
	r := image.Rect(x, y, x+b.Dx(), y+b.Dy()).Intersect(
		image.Rect(c.clip.X, c.clip.Y, c.clip.X+c.clip.W, c.clip.Y+c.clip.H))
	if r.Empty() {
		return
	}
	if rgba, ok := img.(*image.RGBA); ok {
		c.drawRGBADevice(rgba, x, y, r)
		return
	}
	for py := r.Min.Y; py < r.Max.Y; py++ {
		for px := r.Min.X; px < r.Max.X; px++ {
			sr, sg, sb, sa := img.At(b.Min.X+px-x, b.Min.Y+py-y).RGBA()
			src := Color(sa>>8<<24 | sr>>8<<16 | sg>>8<<8 | sb>>8)
			c.blend(px, py, src)
		}
	}
}

// drawRGBADevice is DrawImageDevice's fast path for *image.RGBA, the
// raster every resample produces: it reads the premultiplied bytes
// straight from Pix instead of an At call and color conversion per
// pixel, and writes opaque pixels without the blend when no opacity
// modulates them - a full-screen raster (a frozen screenshot behind an
// overlay) repaints at memory speed. r is the destination rect, already
// clipped.
func (c *Canvas) drawRGBADevice(img *image.RGBA, x, y int, r image.Rectangle) {
	b := img.Bounds()
	for py := r.Min.Y; py < r.Max.Y; py++ {
		src := img.Pix[img.PixOffset(b.Min.X+r.Min.X-x, b.Min.Y+py-y):]
		for px := r.Min.X; px < r.Max.X; px++ {
			i := (px - r.Min.X) * 4
			col := Color(uint32(src[i+3])<<24 | uint32(src[i])<<16 | uint32(src[i+1])<<8 | uint32(src[i+2]))
			if src[i+3] == 0xff && c.alphaScale == 255 {
				c.set(px, py, col)
				continue
			}
			c.blend(px, py, col)
		}
	}
}
