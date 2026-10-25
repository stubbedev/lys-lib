package gpu

import (
	"image"
)

// imageIdleFrames is how many frames an image texture outlives its
// last draw.
const imageIdleFrames = 120

// imageTexture is an uploaded *image.RGBA and the frame it last drew.
type imageTexture struct {
	s    *surface
	used uint64
}

// images are the device's uploaded still images (render.StillImage),
// keyed by the pixels they hold, so a still image's sub-image drawn
// anew each frame finds its texture.
type images struct {
	byImage map[imageKey]*imageTexture
	frames  uint64
}

// imageKey is a raster's identity: its first pixel's memory, its rect
// and its stride.
type imageKey struct {
	pix    *uint8
	rect   image.Rectangle
	stride int
}

// imageTexture is the still image img on the GPU, uploading it on its
// first draw.
func (f *frame) imageTexture(img *image.RGBA) (*surface, bool) {
	b := img.Rect
	if b.Empty() || len(img.Pix) == 0 {
		return nil, false
	}
	ims := &f.d.images
	key := imageKey{pix: &img.Pix[img.PixOffset(b.Min.X, b.Min.Y)], rect: b, stride: img.Stride}
	if t, ok := ims.byImage[key]; ok {
		t.used = ims.frames
		return t.s, true
	}
	s, err := f.d.newSurface(b.Dx(), b.Dy())
	if err == nil {
		err = s.ready()
	}
	if err != nil {
		f.fail(err)
		return nil, false
	}
	if ims.byImage == nil {
		ims.byImage = map[imageKey]*imageTexture{}
	}
	ims.byImage[key] = &imageTexture{s: s, used: ims.frames}
	f.uploads = append(f.uploads, upload{dst: s, region: image.Rect(0, 0, b.Dx(), b.Dy()), offset: len(f.staging), rowLength: b.Dx()})
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := img.Pix[img.PixOffset(b.Min.X, y):img.PixOffset(b.Max.X, y)]
		for x := 0; x < len(row); x += 4 {
			f.staging = append(f.staging, row[x+2], row[x+1], row[x], row[x+3])
		}
	}
	return s, true
}

// retireImages frees the textures no frame drew for imageIdleFrames,
// at a frame's start, when no work is pending.
func (d *Device) retireImages() {
	ims := &d.images
	ims.frames++
	var idle []imageKey
	for key, t := range ims.byImage {
		if ims.frames-t.used > imageIdleFrames {
			idle = append(idle, key)
		}
	}
	if len(idle) == 0 {
		return
	}
	d.cmds.DeviceWaitIdle(d.device)
	for _, key := range idle {
		ims.byImage[key].s.close()
		delete(ims.byImage, key)
	}
}

func (d *Device) closeImages() {
	for _, t := range d.images.byImage {
		t.s.close()
	}
	d.images = images{}
}
