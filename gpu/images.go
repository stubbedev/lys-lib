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

// images are the device's uploaded images, keyed by the raster they
// hold: render's images are immutable once drawn, as its caches
// assume.
type images struct {
	byImage map[*image.RGBA]*imageTexture
	frames  uint64
}

// imageTexture is img on the GPU, uploading it on its first draw.
func (f *frame) imageTexture(img *image.RGBA) (*surface, bool) {
	ims := &f.d.images
	if t, ok := ims.byImage[img]; ok {
		t.used = ims.frames
		return t.s, true
	}
	b := img.Rect
	if b.Empty() {
		return nil, false
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
		ims.byImage = map[*image.RGBA]*imageTexture{}
	}
	ims.byImage[img] = &imageTexture{s: s, used: ims.frames}
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
	var idle []*image.RGBA
	for img, t := range ims.byImage {
		if ims.frames-t.used > imageIdleFrames {
			idle = append(idle, img)
		}
	}
	if len(idle) == 0 {
		return
	}
	d.cmds.DeviceWaitIdle(d.device)
	for _, img := range idle {
		ims.byImage[img].s.close()
		delete(ims.byImage, img)
	}
}

func (d *Device) closeImages() {
	for _, t := range d.images.byImage {
		t.s.close()
	}
	d.images = images{}
}
