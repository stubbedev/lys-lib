package gpu

import (
	"image"

	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// atlasSize is a mask atlas page's width and height.
const atlasSize = 2048

// shelfStep rounds a shelf's height, so masks of nearby heights share one.
const shelfStep = 4

// shelf is a row of the atlas page: masks packed left to right, the ones it
// holds, and the segment that last drew from it.
type shelf struct {
	y, h, x int
	ids     []render.ImageID
	used    uint64
}

type atlasSlot struct {
	at    image.Point
	shelf int
}

// maskAtlas is the page of coverage masks the device keeps across
// frames: each mask is uploaded once and drawn from where it was
// placed, keyed by its ID. A full page gives up its least recently drawn
// shelf, not everything on it. It holds one byte a pixel, which the
// shaders read as red.
type maskAtlas struct {
	// size is the page's width and height, atlasSize unless a test
	// shrinks it before the first glyph.
	size    int
	page    *surface
	slots   map[render.ImageID]atlasSlot
	shelves []shelf
	bottom  int
	// segment numbers the segments recorded, for shelves' use; evictions
	// and resets count what gave way.
	segment           uint64
	evictions, resets int
}

// place packs a w x h mask, in a shelf of its height with room, a new one,
// or the least recently drawn one that fits, which it empties first; false
// when no shelf can take it. flushed says whether the shelf it empties was
// drawn from by the segment recording, which must go first.
func (a *maskAtlas) place(id render.ImageID, w, h int, flush func() bool) (image.Point, bool) {
	sh := (h + shelfStep - 1) / shelfStep * shelfStep
	for i := range a.shelves {
		s := &a.shelves[i]
		if s.h == sh && s.x+w <= a.size {
			return a.put(i, id, w), true
		}
	}
	if a.bottom+sh <= a.size {
		a.shelves = append(a.shelves, shelf{y: a.bottom, h: sh})
		a.bottom += sh
		return a.put(len(a.shelves)-1, id, w), true
	}
	victim := -1
	for i := range a.shelves {
		s := &a.shelves[i]
		if s.h >= sh && s.h <= 2*sh && (victim < 0 || s.used < a.shelves[victim].used) {
			victim = i
		}
	}
	if victim < 0 {
		return image.Point{}, false
	}
	if a.segment == a.shelves[victim].used && !flush() {
		return image.Point{}, false
	}
	s := &a.shelves[victim]
	for _, gone := range s.ids {
		delete(a.slots, gone)
	}
	s.ids, s.x = s.ids[:0], 0
	a.evictions++
	return a.put(victim, id, w), true
}

func (a *maskAtlas) put(i int, id render.ImageID, w int) image.Point {
	s := &a.shelves[i]
	at := image.Pt(s.x, s.y)
	s.x += w
	s.ids = append(s.ids, id)
	s.used = a.segment
	a.slots[id] = atlasSlot{at: at, shelf: i}
	return at
}

// reset empties the page; nothing drawn from it may still be pending.
func (a *maskAtlas) reset() {
	a.resets++
	clear(a.slots)
	a.shelves, a.bottom = a.shelves[:0], 0
}

func (a *maskAtlas) forget(id render.ImageID) {
	slot, ok := a.slots[id]
	if !ok {
		return
	}
	delete(a.slots, id)
	s := &a.shelves[slot.shelf]
	for i, have := range s.ids {
		if have == id {
			s.ids[i] = s.ids[len(s.ids)-1]
			s.ids = s.ids[:len(s.ids)-1]
			break
		}
	}
}

// maskSlot is where m sits on the atlas page, uploading it first if
// it is new; false for a mask no page holds or after an error.
func (f *frame) maskSlot(m render.CoverageMask) (image.Point, bool) {
	a := &f.d.masks
	if a.page == nil {
		if a.size == 0 {
			a.size = atlasSize
		}
		s, err := f.d.newSampledSurface(a.size, a.size, vk.FormatR8Unorm)
		if err == nil {
			err = s.ready()
		}
		if err != nil {
			f.fail(err)
			return image.Point{}, false
		}
		a.page, a.slots = s, map[render.ImageID]atlasSlot{}
	}
	id := m.ID()
	if slot, ok := a.slots[id]; ok {
		a.shelves[slot.shelf].used = a.segment
		return slot.at, true
	}
	w, h := m.Size()
	if w > a.size || h > a.size {
		return image.Point{}, false
	}
	flush := func() bool {
		f.err = f.flush(false)
		return f.err == nil
	}
	at, ok := a.place(id, w, h, flush)
	if !ok {
		if f.err != nil || !flush() {
			return image.Point{}, false
		}
		a.reset()
		if at, ok = a.place(id, w, h, flush); !ok {
			return image.Point{}, false
		}
	}
	f.stats.textureUploads++
	out, off := f.stage(w * h)
	if out == nil {
		return image.Point{}, false
	}
	f.stats.maskBytes += w * h
	f.uploads = append(f.uploads, upload{dst: a.page, region: image.Rect(at.X, at.Y, at.X+w, at.Y+h), offset: off, rowLength: w})
	copy(out, m.Pix())
	return at, true
}
