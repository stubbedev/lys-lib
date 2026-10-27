package gpu

import (
	"cmp"
	"image"
	"slices"

	"github.com/stubbedev/lys-lib/render"
)

// imageIdleFrames is how many frames an image texture outlives its
// last draw.
const imageIdleFrames = 120

// maxImages bounds the image textures kept, well inside the descriptor
// sets (maxSurfaces) the device's layers and targets share.
const maxImages = maxSurfaces / 2

// imageTexture is an uploaded *image.RGBA, the version its pixels were
// uploaded at and the frame it last drew.
type imageTexture struct {
	s       *surface
	version uint64
	used    uint64
}

// images are the device's uploaded still and versioned images
// (render.StillImage, render.VersionedImage), keyed by their raster's
// identity and rect, so a sub-image drawn anew each frame finds its
// texture.
type images struct {
	byImage map[imageKey]*imageTexture
	frames  uint64
	// limit is the most textures kept, maxImages unless a test lowers
	// it.
	limit int
}

// imageKey is which raster's pixels within rect a texture holds: the
// raster's ImageID, or for a raster without one its first pixel's
// memory and its stride, which the key keeps reachable.
type imageKey struct {
	id     render.ImageID
	pix    *uint8
	stride int
	rect   image.Rectangle
}

// imageKeyOf is the key of img's pixels, of raster identity id.
func imageKeyOf(img *image.RGBA, id render.ImageID) imageKey {
	b := img.Rect
	if id != 0 {
		return imageKey{id: id, rect: b}
	}
	return imageKey{pix: &img.Pix[img.PixOffset(b.Min.X, b.Min.Y)], stride: img.Stride, rect: b}
}

// imageTexture is img's pixels at version on the GPU, uploading them on
// their first draw and again when the version moves. id is the raster's
// identity, zero for one identified by its pixels' address. A texture
// this frame already drew at another version cannot change under that
// draw, so the image then has none.
func (f *frame) imageTexture(img *image.RGBA, id render.ImageID, version uint64) (*surface, bool) {
	b := img.Rect
	if b.Empty() || len(img.Pix) == 0 {
		return nil, false
	}
	ims := &f.d.images
	key := imageKeyOf(img, id)
	if t, ok := ims.byImage[key]; ok {
		if t.version != version {
			if t.used == ims.frames {
				return nil, false
			}
			t.version = version
			f.stageImage(t.s, img)
		}
		t.used = ims.frames
		return t.s, true
	}
	if ims.limit == 0 {
		ims.limit = maxImages
	}
	if len(ims.byImage) >= ims.limit {
		ims.evictLRU()
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
	ims.byImage[key] = &imageTexture{s: s, version: version, used: ims.frames}
	f.stageImage(s, img)
	return s, true
}

// stageImage uploads img's pixels into its texture s.
func (f *frame) stageImage(s *surface, img *image.RGBA) {
	b := img.Rect
	f.stats.textureUploads++
	out, off := f.stage(b.Dx() * b.Dy() * 4)
	if out == nil {
		return
	}
	f.uploads = append(f.uploads, upload{dst: s, region: image.Rect(0, 0, b.Dx(), b.Dy()), offset: off, rowLength: b.Dx()})
	o := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := img.Pix[img.PixOffset(b.Min.X, y):img.PixOffset(b.Max.X, y)]
		for x := 0; x < len(row); x += 4 {
			out[o], out[o+1], out[o+2], out[o+3] = row[x+2], row[x+1], row[x], row[x+3]
			o += 4
		}
	}
}

// evictLRU frees the least recently drawn quarter of the textures no
// draw of this frame refers to. Earlier frames' work has finished (each
// frame waits for its own), so they are free to go.
func (ims *images) evictLRU() {
	keys := make([]imageKey, 0, len(ims.byImage))
	for key, t := range ims.byImage {
		if t.used != ims.frames {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b imageKey) int { return cmp.Compare(ims.byImage[a].used, ims.byImage[b].used) })
	for _, key := range keys[:min(len(keys), max(1, ims.limit/4))] {
		ims.byImage[key].s.close()
		delete(ims.byImage, key)
	}
}

// Forget drops what the device keeps for the raster of identity id,
// its textures and its mask atlas slot, once the frame the device is
// drawing, or else its next one, is done: an owner that frees the
// raster (render.Retire) frees the GPU copy with it rather than after
// imageIdleFrames, and a List recorded before the eviction still draws
// from the texture in that frame. A List still open after that frame
// that draws the raster uploads it again, from pixels Retire keeps
// until that List ends, and the texture then idles out. It is safe from
// any goroutine. Zero, no identity, panics: a raster without one is
// kept by its pixels' address and is the garbage collector's to free.
func (d *Device) Forget(id render.ImageID) {
	if id == 0 {
		panic("gpu: Forget of a raster without an ImageID")
	}
	d.forgetMu.Lock()
	d.forgotten = append(d.forgotten, id)
	d.forgetMu.Unlock()
}

// forgetImages frees, after a frame when no work is pending, the
// textures and mask slots of the rasters Forget named.
func (d *Device) forgetImages() {
	d.forgetMu.Lock()
	forgotten := d.forgotten
	d.forgotten = nil
	d.forgetMu.Unlock()
	if len(forgotten) == 0 {
		return
	}
	gone := make(map[render.ImageID]struct{}, len(forgotten))
	for _, id := range forgotten {
		gone[id] = struct{}{}
		delete(d.masks.slots, id)
	}
	d.dropImages(func(key imageKey, _ *imageTexture) bool {
		_, ok := gone[key.id]
		return ok
	})
}

// retireImages frees the textures no frame drew for imageIdleFrames,
// at a frame's start, when no work is pending.
func (d *Device) retireImages() {
	ims := &d.images
	ims.frames++
	d.dropImages(func(_ imageKey, t *imageTexture) bool { return ims.frames-t.used > imageIdleFrames })
}

// dropImages frees the textures drop picks.
func (d *Device) dropImages(drop func(imageKey, *imageTexture) bool) {
	ims := &d.images
	var picked []imageKey
	for key, t := range ims.byImage {
		if drop(key, t) {
			picked = append(picked, key)
		}
	}
	if len(picked) == 0 {
		return
	}
	d.cmds.DeviceWaitIdle(d.device)
	for _, key := range picked {
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
