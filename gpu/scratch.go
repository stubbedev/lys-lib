package gpu

import (
	"fmt"
	"image"
	"runtime"
	"slices"
	"sync"

	memory "github.com/stubbedev/memory-go"

	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// scratchCanvas is a target-sized CPU canvas the renderer rasterizes
// into: a sprite, or a layer's pixels read back. Outside a call it is
// transparent throughout, so a call's pixels are its own.
type scratchCanvas struct {
	// mem holds buf, off the Go heap in the device's pixels.
	mem memory.Slice[byte]
	buf []byte
	cv  *render.Canvas
}

// newScratchCanvas allocates a w x h scratch canvas in pixels with room
// for a capW x capH one.
func newScratchCanvas(pixels memory.Allocator, w, h, capW, capH, num, denom int) (*scratchCanvas, error) {
	mem, err := memory.MakeSlice[byte](pixels, render.Stride(capW)*capH)
	if err != nil {
		return nil, fmt.Errorf("gpu: a %dx%d scratch canvas: %w", capW, capH, err)
	}
	buf := mem.Elems()[:render.Stride(w)*h]
	return &scratchCanvas{mem: mem, buf: buf, cv: render.NewScaled(buf, render.Stride(w), w, h, num, denom)}, nil
}

// take copies r's rows into dst, tight, and clears them.
func (sc *scratchCanvas) take(dst []byte, r render.Rect) {
	stride := render.Stride(sc.cv.Rect().W)
	for y := r.Y; y < r.Y+r.H; y++ {
		row := sc.buf[y*stride+r.X*4 : y*stride+(r.X+r.W)*4]
		copy(dst[(y-r.Y)*r.W*4:], row)
		clear(row)
	}
}

// put copies tight rows of src into r.
func (sc *scratchCanvas) put(r render.Rect, src []byte) {
	stride := render.Stride(sc.cv.Rect().W)
	for y := range r.H {
		copy(sc.buf[(r.Y+y)*stride+r.X*4:(r.Y+y)*stride+(r.X+r.W)*4], src[y*r.W*4:(y+1)*r.W*4])
	}
}

// clear zeroes r.
func (sc *scratchCanvas) clear(r render.Rect) {
	stride := render.Stride(sc.cv.Rect().W)
	for y := r.Y; y < r.Y+r.H; y++ {
		clear(sc.buf[y*stride+r.X*4 : y*stride+(r.X+r.W)*4])
	}
}

// scratch is the renderer's per-device working set, sized to the
// largest target drawn so far.
type scratch struct {
	// scratches are the CPU canvases; the first rasterizes sprites.
	scratches []*scratchCanvas
	// page holds a segment's sprites; blank is sampled by draws that
	// sample nothing.
	page, blank *surface
	// filterTemp and filterTemp2 hold a layer region an in-place
	// call reads while it writes the layer, a blur's passes and a
	// shadow's silhouette, filterW x filterH: at least the target, more
	// when a silhouette needed it.
	filterTemp, filterTemp2 *surface
	// current is the buffers of the segment recording, spareFrameBuffers
	// those of finished segments, and frameBufferCount how many exist.
	current           *frameBuffers
	spareFrameBuffers []*frameBuffers
	frameBufferCount  int
	// readback holds the downloads of a segment, which is waited for
	// before they are read.
	readback           *buffer
	scratchW, scratchH int
	filterW, filterH   int
	freeLayers         []*layerSurface
	// layers are the live layer surfaces, pooled ones included, freed
	// with the device.
	layers map[*layerSurface]struct{}
	masks  maskAtlas
	images images
	// frame is the frame Render plays into, kept so its slices keep
	// their capacity from frame to frame.
	frame  frame
	deadMu sync.Mutex
	dead   []*layerSurface
	// forgotten are the raster identities Forget named, dropped at the
	// next frame's start.
	forgetMu  sync.Mutex
	forgotten []render.ImageID
}

func (d *Device) grown(have, need int) int {
	if need <= have {
		return have
	}
	return max(need, min(have*3/2, d.maxDim))
}

// ensureScratch sizes the working set for a w x h target.
func (d *Device) ensureScratch(w, h int) error {
	if d.blank == nil {
		var err error
		if d.blank, err = d.newSurface(1, 1); err != nil {
			return err
		}
		if err := d.blank.ready(); err != nil {
			return err
		}
		if d.readback, err = d.newReadBuffer(1<<16, vk.BufferUsageTransferDstBit, nil); err != nil {
			return err
		}
	}
	if w <= d.scratchW && h <= d.scratchH {
		return nil
	}
	d.scratchW, d.scratchH = d.grown(d.scratchW, w), d.grown(d.scratchH, h)
	if d.page != nil {
		d.retire(d.page)
	}
	var err error
	if d.page, err = d.newSurface(d.scratchW, d.scratchH); err != nil {
		return err
	}
	if err := d.fitFilters(w, h); err != nil {
		return err
	}
	if err := d.dropScratches(); err != nil {
		return err
	}
	return d.page.ready()
}

// stagingMin is the staging buffer a segment starts with and shrinks
// back to.
const stagingMin = 1 << 16

// dropScratches frees the scratch canvases; the next frame that needs
// one allocates it again.
func (d *Device) dropScratches() error {
	for _, sc := range d.scratches {
		if sc == nil {
			continue
		}
		if err := memory.FreeSlice(&d.pixels, sc.mem); err != nil {
			return fmt.Errorf("gpu: free a scratch canvas: %w", err)
		}
	}
	d.scratches = nil
	return nil
}

// Trim hands back what the device keeps only for speed: the CPU
// scratch canvases, freed off the Go heap and unmapped, the buffers of
// finished segments, and the image textures not drawn since the last
// Trim. A device that draws again allocates them
// anew. Call it when the device has been idle a while, between frames,
// on the device's goroutine.
func (d *Device) Trim() error {
	if err := d.dropScratches(); err != nil {
		return err
	}
	d.trimLayers()
	if err := d.pixels.Trim(); err != nil {
		return fmt.Errorf("gpu: trim the scratch canvases: %w", err)
	}
	if err := d.idle(); err != nil {
		return err
	}
	d.closeFrameBuffers()
	d.sweepImages()
	return nil
}

// fitFilters grows the filter surfaces to hold w x h; the old ones go
// once nothing pending uses them.
func (d *Device) fitFilters(w, h int) error {
	if w <= d.filterW && h <= d.filterH {
		return nil
	}
	d.filterW, d.filterH = d.grown(d.filterW, w), d.grown(d.filterH, h)
	for _, t := range []**surface{&d.filterTemp, &d.filterTemp2} {
		if *t != nil {
			d.retire(*t)
		}
		var err error
		if *t, err = d.newSurface(d.filterW, d.filterH); err != nil {
			return fmt.Errorf("gpu: filter surface %dx%d: %w", d.filterW, d.filterH, err)
		}
		if err := (*t).ready(); err != nil {
			return err
		}
	}
	return nil
}

// scratchCanvas is scratch canvas i for a w x h canvas at num/denom.
// Its buffer is kept by capacity: transparent between calls, a bigger
// one serves any size, so a resize allocates only when it grows.
func (d *Device) scratchCanvas(i, w, h, num, denom int) (*scratchCanvas, error) {
	for len(d.scratches) <= i {
		d.scratches = append(d.scratches, nil)
	}
	sc := d.scratches[i]
	need := render.Stride(w) * h
	switch {
	case sc == nil || cap(sc.buf) < need:
		if sc != nil {
			if err := memory.FreeSlice(&d.pixels, sc.mem); err != nil {
				return nil, fmt.Errorf("gpu: free a scratch canvas: %w", err)
			}
		}
		grown, err := newScratchCanvas(&d.pixels, w, h, max(w, d.scratchW), max(h, d.scratchH), num, denom)
		if err != nil {
			d.scratches[i] = nil
			return nil, err
		}
		sc = grown
		d.scratches[i] = sc
	case len(sc.buf) != need || sc.cv.Rect().W != w || sc.cv.Rect().H != h:
		sc.buf = sc.buf[:need]
		sc.cv.Reset(sc.buf, render.Stride(w), w, h, num, denom)
	}
	if n, dn := sc.cv.DeviceScale(); n != num || dn != denom {
		sc.cv.Reset(sc.buf, render.Stride(w), w, h, num, denom)
	}
	return sc, nil
}

// layerSurface is a recorded layer's GPU surface, at least the w x h of
// the target it last served: a resize reuses it until it outgrows it. A
// kept layer's is freed when the layer is collected; a scratch layer's
// goes back to the pool on Release.
type layerSurface struct {
	s       *surface
	w, h    int
	cleanup runtime.Cleanup
}

func (ls *layerSurface) fits(w, h int) bool {
	const maxWaste = 4
	return ls.s.w >= w && ls.s.h >= h && ls.s.w*ls.s.h <= maxWaste*w*h
}

// takeLayer backs l with a surface of at least w x h: the smallest that
// fits in the pool for a scratch layer, else a new one freed with l, 1.5
// times what have or the pool it replaces held along a side that grows,
// so a resize makes a logarithmic number of them. Pooled surfaces that
// no longer fit go: the new one serves all they served.
func (d *Device) takeLayer(l *render.Layer, w, h int, have image.Point, scratch bool) (*layerSurface, error) {
	if scratch {
		best := -1
		for i, ls := range d.freeLayers {
			if ls.s.w >= w && ls.s.h >= h && (best < 0 || ls.s.w*ls.s.h < d.freeLayers[best].s.w*d.freeLayers[best].s.h) {
				best = i
			}
		}
		if best >= 0 {
			ls := d.freeLayers[best]
			d.freeLayers = slices.Delete(d.freeLayers, best, best+1)
			l.SetBacking(ls)
			return ls, nil
		}
		for _, ls := range d.freeLayers {
			have = image.Pt(max(have.X, ls.s.w), max(have.Y, ls.s.h))
			d.dropLayer(ls)
		}
		d.freeLayers = d.freeLayers[:0]
	}
	s, err := d.newSurface(d.grown(have.X, w), d.grown(have.Y, h))
	if err != nil {
		return nil, err
	}
	ls := &layerSurface{s: s}
	if !scratch {
		ls.cleanup = runtime.AddCleanup(l, d.buryLayer, ls)
	}
	if d.layers == nil {
		d.layers = map[*layerSurface]struct{}{}
	}
	d.layers[ls] = struct{}{}
	l.SetBacking(ls)
	return ls, nil
}

// poolLayer returns a released scratch layer's surface to the pool; a
// full pool keeps its largest.
func (d *Device) poolLayer(ls *layerSurface) {
	ls.cleanup.Stop()
	d.freeLayers = append(d.freeLayers, ls)
	if len(d.freeLayers) <= render.MaxPooledLayers {
		return
	}
	smallest := 0
	for i, f := range d.freeLayers {
		if f.s.w*f.s.h < d.freeLayers[smallest].s.w*d.freeLayers[smallest].s.h {
			smallest = i
		}
	}
	d.dropLayer(d.freeLayers[smallest])
	d.freeLayers = slices.Delete(d.freeLayers, smallest, smallest+1)
}

func (d *Device) trimLayers() {
	for _, ls := range d.freeLayers {
		d.dropLayer(ls)
	}
	d.freeLayers = nil
}

// dropLayer frees a surface its layer outgrew.
func (d *Device) dropLayer(ls *layerSurface) {
	ls.cleanup.Stop()
	delete(d.layers, ls)
	d.retire(ls.s)
}

// buryLayer queues a collected layer's surface for freeing on the
// device's goroutine; it runs on the runtime's.
func (d *Device) buryLayer(ls *layerSurface) {
	d.deadMu.Lock()
	d.dead = append(d.dead, ls)
	d.deadMu.Unlock()
}

// freeDeadLayers frees the surfaces of collected layers.
func (d *Device) freeDeadLayers() {
	d.deadMu.Lock()
	dead := d.dead
	d.dead = nil
	d.deadMu.Unlock()
	for _, ls := range dead {
		delete(d.layers, ls)
		d.retire(ls.s)
	}
}

func (d *Device) closeScratch() {
	d.freeDeadLayers()
	d.closeImages()
	for ls := range d.layers {
		ls.cleanup.Stop()
		ls.s.close()
	}
	for _, s := range []*surface{d.page, d.blank, d.masks.page, d.filterTemp, d.filterTemp2} {
		if s != nil {
			s.close()
		}
	}
	if d.current != nil {
		d.spareFrameBuffers = append(d.spareFrameBuffers, d.current)
		d.current = nil
	}
	d.closeFrameBuffers()
	if d.readback != nil {
		d.readback.close()
	}
	d.scratch = scratch{}
	if err := d.pixels.Close(); err != nil {
		panic(fmt.Sprintf("gpu: unmap the scratch canvases: %v", err))
	}
}
