package render

import (
	"image"
	"math"

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
	// aspect ratio kept, cropping the overflow: the largest crop of the
	// source with the box's aspect ratio, centered unless an ImagePos
	// places it, resampled to the full box.
	ImageCover
	// ImageNone draws the image at its natural size, one source pixel
	// per device pixel, placed in the box (centered by default) and
	// clipped to it. Nothing
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

// ImagePos places an image's concrete object inside its box, CSS's
// object-position: per axis the object's origin sits at X (or Y) of the
// free space — the box less the object, negative where the object
// overflows — plus OffX (or OffY) pixels, in the units of the box.
type ImagePos struct {
	X, Y       float64
	OffX, OffY int
}

// ImageCentered is the default placement, 50% 50%.
var ImageCentered = ImagePos{X: 0.5, Y: 0.5}

// PlaceImage resolves a scaling policy for a srcW x srcH image into a
// boxW x boxH box, the object placed by pos. Both sizes are in the same
// units - widget.Image feeds device pixels so the result stays crisp at
// fractional scales. It returns the source rectangle to sample and the
// destination rect, relative to the box, to resample it into: the part
// of the placed object the box shows. An empty result means a
// zero-sized input or an object placed wholly outside the box.
func PlaceImage(srcW, srcH, boxW, boxH int, s ImageScale, pos ImagePos) (src image.Rectangle, dst Rect) {
	if srcW <= 0 || srcH <= 0 || boxW <= 0 || boxH <= 0 {
		return image.Rectangle{}, Rect{}
	}
	if s == ImageCover {
		return placeCover(srcW, srcH, boxW, boxH, pos)
	}
	objW, objH := objectSize(srcW, srcH, boxW, boxH, s)
	x0, x1, sx0, sx1, okX := placeAxis(srcW, objW, boxW, pos.X, pos.OffX)
	y0, y1, sy0, sy1, okY := placeAxis(srcH, objH, boxH, pos.Y, pos.OffY)
	if !okX || !okY {
		return image.Rectangle{}, Rect{}
	}
	return image.Rect(sx0, sy0, sx1, sy1), Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// objectSize is the concrete object size of every policy but cover.
func objectSize(srcW, srcH, boxW, boxH int, s ImageScale) (w, h int) {
	switch s {
	case ImageStretch:
		return boxW, boxH
	case ImageScaleDown:
		if srcW <= boxW && srcH <= boxH {
			return srcW, srcH
		}
		return objectSize(srcW, srcH, boxW, boxH, ImageFit)
	case ImageNone:
		return srcW, srcH
	default: // ImageFit
		// scale = min(boxW/srcW, boxH/srcH), rounded; the rounding
		// cannot push the result past the box because the real value
		// lies strictly inside it on the non-binding axis. The floor at
		// 1 keeps a thin source visible in a wide box.
		if boxW*srcH < boxH*srcW {
			return boxW, min(boxH, max(1, roundDiv(srcH*boxW, srcW)))
		}
		return min(boxW, max(1, roundDiv(srcW*boxH, srcH))), boxH
	}
}

// placeAxis places an obj-long object over a box-long axis at frac of
// the free space plus off, and returns the box span it shows, [d0, d1),
// with the source span of a src-long source it samples, [s0, s1); ok
// is false when nothing shows. The origin truncates toward zero, so a
// centered overflow crops the source's floor half.
func placeAxis(src, obj, box int, frac float64, off int) (d0, d1, s0, s1 int, ok bool) {
	o := int(frac*float64(box-obj)) + off
	d0, d1 = max(o, 0), min(o+obj, box)
	if d1 <= d0 {
		return 0, 0, 0, 0, false
	}
	return d0, d1, (d0 - o) * src / obj, (d1 - o) * src / obj, true
}

// placeCover is PlaceImage's cover policy, worked in source pixels: the
// visible crop of the source is the box divided by the scale
// max(boxW/srcW, boxH/srcH). The branch decides which axis binds; the
// other side's crop follows from the box ratio, and the floor at 1
// keeps extreme aspect ratios from rounding the crop away entirely.
// The position moves the crop across the source's slack, an offset
// scaled into source pixels.
func placeCover(srcW, srcH, boxW, boxH int, pos ImagePos) (image.Rectangle, Rect) {
	cropW, cropH := srcW, srcH
	if boxW*srcH >= boxH*srcW {
		cropH = min(srcH, max(1, roundDiv(srcW*boxH, boxW)))
	} else {
		cropW = min(srcW, max(1, roundDiv(srcH*boxW, boxH)))
	}
	x0, x1, sx0, sx1, okX := coverAxis(srcW, cropW, boxW, pos.X, pos.OffX)
	y0, y1, sy0, sy1, okY := coverAxis(srcH, cropH, boxH, pos.Y, pos.OffY)
	if !okX || !okY {
		return image.Rectangle{}, Rect{}
	}
	return image.Rect(sx0, sy0, sx1, sy1), Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// coverAxis places a crop-long source window over a src-long source
// at frac of the slack, shifted against off box pixels, and returns
// the box span it fills, [d0, d1), with the source span it samples.
func coverAxis(src, crop, box int, frac float64, off int) (d0, d1, s0, s1 int, ok bool) {
	c := int(frac*float64(src-crop)) - int(math.Round(float64(off*crop)/float64(box)))
	s0, s1 = max(c, 0), min(c+crop, src)
	if s1 <= s0 {
		return 0, 0, 0, 0, false
	}
	return (s0 - c) * box / crop, (s1 - c) * box / crop, s0, s1, true
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
	if c.rec != nil {
		c.record(op{kind: opImageDevice, img: img, i: [5]int{x, y}})
		return
	}
	b := img.Bounds()
	if b.Empty() {
		return
	}
	r := image.Rect(x, y, x+b.Dx(), y+b.Dy()).Intersect(
		image.Rect(c.clip.X, c.clip.Y, c.clip.X+c.clip.W, c.clip.Y+c.clip.H))
	if r.Empty() {
		return
	}
	if rgba := rgbaOf(img); rgba != nil {
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

// DrawImageRect blends img onto the canvas scaled into the logical
// rect r: nearest-neighbor sampling, the mask-image rasterizer's own.
func (c *Canvas) DrawImageRect(img image.Image, r Rect) {
	if c.rec != nil {
		c.record(op{kind: opImageRect, img: img, r: r})
		return
	}
	b := img.Bounds()
	full := c.MapRect(r)
	dst := c.clip.Intersect(full)
	if dst.Empty() || b.Empty() {
		return
	}
	for py := dst.Y; py < dst.Y+dst.H; py++ {
		for px := dst.X; px < dst.X+dst.W; px++ {
			sx := b.Min.X + (px-full.X)*b.Dx()/full.W
			sy := b.Min.Y + (py-full.Y)*b.Dy()/full.H
			sr, sg, sb, sa := img.At(sx, sy).RGBA()
			c.blend(px, py, Color(sa>>8<<24|sr>>8<<16|sg>>8<<8|sb>>8))
		}
	}
}

// StillImage is an *image.RGBA its owner never changes once wrapped: a
// decoded picture, a cached raster, an icon. A GPU renderer uploads it
// once and keeps the texture while it is drawn; any other image it
// draws from its visible pixels each time, so changing an image's
// pixels between draws (a video frame resampled into the same raster)
// is always seen. Its sub-images are still too.
type StillImage struct{ *image.RGBA }

// SubImage is the still image's part within r.
func (s StillImage) SubImage(r image.Rectangle) image.Image {
	sub, ok := s.RGBA.SubImage(r).(*image.RGBA)
	if !ok {
		panic("render: an *image.RGBA's SubImage is not an *image.RGBA")
	}
	return StillImage{sub}
}

// rgbaOf is img's pixels as an *image.RGBA when it holds them so: the
// image itself, or a still image's; nil otherwise.
func rgbaOf(img image.Image) *image.RGBA {
	switch v := img.(type) {
	case *image.RGBA:
		return v
	case StillImage:
		return v.RGBA
	}
	return nil
}
