package gpu

import (
	"runtime"
	"slices"
	"sync"

	"github.com/stubbedev/lys-ui/render"
	"github.com/stubbedev/lys-ui/third_party/vk"
)

// scratchCanvas is a target-sized CPU canvas the renderer rasterizes
// into: a sprite, or a layer's pixels read back. Outside a call it is
// transparent throughout, so a call's pixels are its own.
type scratchCanvas struct {
	buf []byte
	cv  *render.Canvas
}

func newScratchCanvas(w, h, num, denom int) *scratchCanvas {
	buf := make([]byte, render.Stride(w)*h)
	return &scratchCanvas{buf: buf, cv: render.NewScaled(buf, render.Stride(w), w, h, num, denom)}
}

// take appends r's rows to dst, tight, and clears them.
func (sc *scratchCanvas) take(dst []byte, r render.Rect) []byte {
	stride := render.Stride(sc.cv.Rect().W)
	for y := r.Y; y < r.Y+r.H; y++ {
		row := sc.buf[y*stride+r.X*4 : y*stride+(r.X+r.W)*4]
		dst = append(dst, row...)
		clear(row)
	}
	return dst
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
	page, blank              *surface
	prims, staging, readback *buffer
	scratchW, scratchH       int
	freeLayers               []*layerSurface
	masks                    maskAtlas
	images                   images
	// frame is the frame Render plays into, kept so its slices keep
	// their capacity from frame to frame.
	frame  frame
	deadMu sync.Mutex
	dead   []*layerSurface
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
		for _, b := range []**buffer{&d.prims, &d.staging, &d.readback} {
			usage := vk.BufferUsageStorageBufferBit
			switch b {
			case &d.staging:
				usage = vk.BufferUsageTransferSrcBit
			case &d.readback:
				usage = vk.BufferUsageTransferDstBit
			}
			if *b, err = d.newBuffer(1<<16, usage); err != nil {
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
	d.scratches = nil
	return d.page.ready()
}

// scratchCanvas is scratch canvas i for a w x h canvas at num/denom.
// Its buffer is kept by capacity: transparent between calls, a bigger
// one serves any size, so a resize allocates only when it grows.
func (d *Device) scratchCanvas(i, w, h, num, denom int) *scratchCanvas {
	for len(d.scratches) <= i {
		d.scratches = append(d.scratches, nil)
	}
	sc := d.scratches[i]
	need := render.Stride(w) * h
	switch {
	case sc == nil || cap(sc.buf) < need:
		sc = newScratchCanvas(w, h, num, denom)
		d.scratches[i] = sc
	case len(sc.buf) != need || sc.cv.Rect().W != w || sc.cv.Rect().H != h:
		sc.buf = sc.buf[:need]
		sc.cv.Reset(sc.buf, render.Stride(w), w, h, num, denom)
	}
	if n, dn := sc.cv.DeviceScale(); n != num || dn != denom {
		sc.cv.Reset(sc.buf, render.Stride(w), w, h, num, denom)
	}
	return sc
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
		ls.s.close()
	}
}

func (d *Device) closeScratch() {
	d.freeDeadLayers()
	d.closeImages()
	for _, ls := range d.freeLayers {
		ls.s.close()
	}
	for _, s := range []*surface{d.page, d.blank, d.masks.page} {
		if s != nil {
			s.close()
		}
	}
	for _, b := range []*buffer{d.prims, d.staging, d.readback} {
		if b != nil {
			b.close()
		}
	}
	d.scratch = scratch{}
}
