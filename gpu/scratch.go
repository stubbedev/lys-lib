package gpu

import (
	"fmt"
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

// newScratchCanvas allocates a w x h scratch canvas in pixels.
func newScratchCanvas(pixels memory.Allocator, w, h, num, denom int) (*scratchCanvas, error) {
	mem, err := memory.MakeSlice[byte](pixels, render.Stride(w)*h)
	if err != nil {
		return nil, fmt.Errorf("gpu: a %dx%d scratch canvas: %w", w, h, err)
	}
	buf := mem.Elems()
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
	filterTemp, filterTemp2         *surface
	prims, words, staging, readback *buffer
	scratchW, scratchH              int
	filterW, filterH                int
	freeLayers                      []*layerSurface
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
		for _, b := range []**buffer{&d.prims, &d.words, &d.staging, &d.readback} {
			switch b {
			case &d.staging:
				*b, err = d.newBuffer(1<<16, vk.BufferUsageTransferSrcBit)
			case &d.readback:
				*b, err = d.newReadBuffer(1<<16, vk.BufferUsageTransferDstBit, nil)
			default:
				*b, err = d.newBuffer(1<<16, vk.BufferUsageStorageBufferBit)
			}
			if err != nil {
				return err
			}
		}
	}
	if w <= d.scratchW && h <= d.scratchH {
		return nil
	}
	d.scratchW, d.scratchH = max(w, d.scratchW), max(h, d.scratchH)
	if d.page != nil {
		d.page.close()
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

// stagingMin is the staging buffer a device starts with and shrinks
// back to.
const stagingMin = 1 << 16

// fitStaging shrinks the staging buffer back to twice what a frame
// needed, at least stagingMin, once it is more than four times that:
// one large upload (a wallpaper, a 4K sprite) must not keep its
// buffer for good, while a steady stream of them keeps one.
func (d *Device) fitStaging(needed int) error {
	keep := max(2*needed, stagingMin)
	if len(d.staging.mapped) <= 2*keep {
		return nil
	}
	shrunk, err := d.newBuffer(keep, vk.BufferUsageTransferSrcBit)
	if err != nil {
		return fmt.Errorf("gpu: staging buffer of %d bytes: %w", keep, err)
	}
	d.staging.close()
	d.staging = shrunk
	return nil
}

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
// scratch canvases, freed off the Go heap and unmapped, and a staging
// buffer above its minimum. A device that draws again allocates them
// anew. Call it when the device has been idle a while, between frames,
// on the device's goroutine.
func (d *Device) Trim() error {
	if err := d.dropScratches(); err != nil {
		return err
	}
	if err := d.pixels.Trim(); err != nil {
		return fmt.Errorf("gpu: trim the scratch canvases: %w", err)
	}
	if d.staging == nil {
		return nil
	}
	return d.fitStaging(0)
}

// fitFilters grows the filter surfaces to hold w x h. Nothing pending
// may still use them.
func (d *Device) fitFilters(w, h int) error {
	if w <= d.filterW && h <= d.filterH {
		return nil
	}
	d.filterW, d.filterH = max(w, d.filterW), max(h, d.filterH)
	for _, t := range []**surface{&d.filterTemp, &d.filterTemp2} {
		if *t != nil {
			(*t).close()
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
		grown, err := newScratchCanvas(&d.pixels, w, h, num, denom)
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

// layerSurface is a recorded layer's GPU surface. A kept layer's is
// freed when the layer is collected; a scratch layer's goes back to
// the pool on Release.
type layerSurface struct {
	s       *surface
	cleanup runtime.Cleanup
}

// takeLayer backs l with a w x h surface: a pooled one for a scratch
// layer, else a new one freed with l.
func (d *Device) takeLayer(l *render.Layer, w, h int, scratch bool) (*layerSurface, error) {
	if scratch {
		if i := slices.IndexFunc(d.freeLayers, func(ls *layerSurface) bool { return ls.s.w == w && ls.s.h == h }); i >= 0 {
			ls := d.freeLayers[i]
			d.freeLayers = slices.Delete(d.freeLayers, i, i+1)
			l.SetBacking(ls)
			return ls, nil
		}
	}
	s, err := d.newSurface(w, h)
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

// poolLayer returns a released scratch layer's surface to the pool.
func (d *Device) poolLayer(ls *layerSurface) {
	ls.cleanup.Stop()
	d.freeLayers = append(d.freeLayers, ls)
}

// dropLayer frees a surface its layer outgrew.
func (d *Device) dropLayer(ls *layerSurface) {
	ls.cleanup.Stop()
	d.cmds.DeviceWaitIdle(d.device)
	delete(d.layers, ls)
	ls.s.close()
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
	if len(dead) == 0 {
		return
	}
	d.cmds.DeviceWaitIdle(d.device)
	for _, ls := range dead {
		delete(d.layers, ls)
		ls.s.close()
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
	for _, b := range []*buffer{d.prims, d.words, d.staging, d.readback} {
		if b != nil {
			b.close()
		}
	}
	d.scratch = scratch{}
	if err := d.pixels.Close(); err != nil {
		panic(fmt.Sprintf("gpu: unmap the scratch canvases: %v", err))
	}
}
