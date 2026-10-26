package gpu

import (
	"image"

	"github.com/stubbedev/lys-ui/render"
)

// atlasSize is a mask atlas page's width and height.
const atlasSize = 2048

// maskAtlas is the page of coverage masks the device keeps across
// frames: each mask is uploaded once and drawn from where it was
// placed. A full page empties and refills.
type maskAtlas struct {
	// size is the page's width and height, atlasSize unless a test
	// shrinks it before the first glyph.
	size          int
	page          *surface
	slots         map[render.CoverageMask]image.Point
	px, py, shelf int
}

// place packs a w x h mask, false when the page is full.
func (a *maskAtlas) place(w, h int) (image.Point, bool) {
	if a.px+w > a.size {
		a.px, a.py, a.shelf = 0, a.py+a.shelf, 0
	}
	if a.py+h > a.size {
		return image.Point{}, false
	}
	at := image.Pt(a.px, a.py)
	a.px += w
	a.shelf = max(a.shelf, h)
	return at, true
}

// reset empties the page; nothing drawn from it may still be pending.
func (a *maskAtlas) reset() {
	clear(a.slots)
	a.px, a.py, a.shelf = 0, 0, 0
}

// maskSlot is where m sits on the atlas page, uploading it first if
// it is new; false for a mask no page holds or after an error.
func (f *frame) maskSlot(m render.CoverageMask) (image.Point, bool) {
	a := &f.d.masks
	if a.page == nil {
		if a.size == 0 {
			a.size = atlasSize
		}
		s, err := f.d.newSurface(a.size, a.size)
		if err == nil {
			err = s.ready()
		}
		if err != nil {
			f.fail(err)
			return image.Point{}, false
		}
		a.page, a.slots = s, map[render.CoverageMask]image.Point{}
	}
	if at, ok := a.slots[m]; ok {
		return at, true
	}
	w, h := m.Size()
	if w > a.size || h > a.size {
		return image.Point{}, false
	}
	at, ok := a.place(w, h)
	if !ok {
		if f.err = f.flush(false); f.err != nil {
			return image.Point{}, false
		}
		a.reset()
		at, _ = a.place(w, h)
	}
	a.slots[m] = at
	f.uploads = append(f.uploads, upload{dst: a.page, region: image.Rect(at.X, at.Y, at.X+w, at.Y+h), offset: len(f.staging), rowLength: w})
	for _, c := range m.Pix() {
		f.staging = append(f.staging, c, c, c, c)
	}
	return at, true
}
