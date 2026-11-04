package gpu

import (
	"cmp"
	"image"
	"slices"

	"github.com/stubbedev/lys-lib/render"
)

// maxImages bounds the image textures kept, well inside the descriptor
// sets (maxSurfaces) the device's layers and targets share.
const maxImages = maxSurfaces / 2

// imageTexture is an uploaded *image.RGBA, the version its pixels were
// uploaded at and the frame it last drew.
type imageTexture struct {
	s       *surface
	version uint64
	used    uint64
	// readyAfter is the submission that uploads the pixels of a large still,
	// which draws once it has finished.
	readyAfter uint64
}

// images are the device's uploaded still and versioned images
// (render.StillImage, render.VersionedImage), keyed by their raster's
// identity and rect, so a sub-image drawn anew each frame finds its
// texture.
type images struct {
	byImage map[imageKey]*imageTexture
	stills  stillUploads
	frames  uint64
	swept   uint64
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
func (f *frame) imageTexture(img *image.RGBA, id render.ImageID, version, dirtySince uint64, dirty image.Rectangle) (*surface, bool) {
	b := img.Rect
	if b.Empty() || len(img.Pix) == 0 {
		return nil, false
	}
	ims := &f.d.images
	key := imageKeyOf(img, id)
	if t, ok := ims.byImage[key]; ok {
		if t.readyAfter > f.d.completed {
			f.d.incomplete = true
			return nil, true
		}
		if t.version != version {
			if t.used == ims.frames {
				return nil, false
			}
			area := b
			if t.version == dirtySince && dirty.In(b) && !dirty.Empty() {
				area = dirty
			}
			t.version = version
			f.stageImage(t.s, img, area)
		}
		t.used = ims.frames
		return t.s, true
	}
	if version == 0 && id != 0 && b.Dx()*b.Dy()*4 >= stillWorkerBytes {
		f.d.incomplete = true
		f.d.startStillUpload(key, img)
		return nil, true
	}
	if ims.limit == 0 {
		ims.limit = maxImages
	}
	if len(ims.byImage) >= ims.limit {
		f.d.evictLRU()
	}
	s, err := f.d.newImageSurface(b.Dx(), b.Dy())
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
	f.stageImage(s, img, b)
	return s, true
}

// stageImage uploads the pixels of area, within img's rect, into its texture s.
func (f *frame) stageImage(s *surface, img *image.RGBA, area image.Rectangle) {
	f.stats.textureUploads++
	n := area.Dx() * area.Dy() * 4
	f.stats.imageBytes += n
	out, off := f.stage(n)
	if out == nil {
		return
	}
	f.uploads = append(f.uploads, upload{dst: s, region: area.Sub(img.Rect.Min), offset: off, rowLength: area.Dx()})
	copyRows(out, img, area)
}

func copyRows(out []byte, img *image.RGBA, area image.Rectangle) {
	row := area.Dx() * 4
	for y := area.Min.Y; y < area.Max.Y; y++ {
		at := img.PixOffset(area.Min.X, y)
		copy(out[(y-area.Min.Y)*row:], img.Pix[at:at+row])
	}
}

// evictLRU frees the least recently drawn quarter of the textures no
// draw of this frame refers to.
func (d *Device) evictLRU() {
	ims := &d.images
	keys := make([]imageKey, 0, len(ims.byImage))
	for key, t := range ims.byImage {
		if t.used != ims.frames {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b imageKey) int { return cmp.Compare(ims.byImage[a].used, ims.byImage[b].used) })
	for _, key := range keys[:min(len(keys), max(1, ims.limit/4))] {
		d.retire(ims.byImage[key].s)
		delete(ims.byImage, key)
	}
}

// Forget drops what the device keeps for the raster of identity id,
// its textures and its mask atlas slot, once the frame the device is
// drawing, or else its next one, is done: an owner that frees the
// raster (render.Retire) frees the GPU copy with it rather than leaving
// it to the maxImages bound or Trim, and a List recorded before the eviction still draws
// from the texture in that frame. A List still open after that frame
// that draws the raster uploads it again, from pixels Retire keeps
// until that List ends. It is safe from
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

// forgetImages frees, once the work pending has finished, the
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
	d.images.stills.forget(gone)
	d.dropImages(func(key imageKey, _ *imageTexture) bool {
		_, ok := gone[key.id]
		return ok
	})
}

// sweepImages frees the textures no frame drew since the last sweep. A
// frame draws only what its damage touches, so frames alone say nothing
// about whether a raster is still on screen: this runs only from Trim.
func (d *Device) sweepImages() {
	ims := &d.images
	d.dropImages(func(_ imageKey, t *imageTexture) bool { return t.used <= ims.swept })
	ims.swept = ims.frames
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
	for _, key := range picked {
		d.retire(ims.byImage[key].s)
		delete(ims.byImage, key)
	}
}

func (d *Device) closeImages() {
	for _, t := range d.images.byImage {
		t.s.close()
	}
	d.images = images{}
}
