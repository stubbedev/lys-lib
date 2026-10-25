package render

import (
	"slices"
	"sync"
)

// layerShape is the geometry a layer's buffer fits: a canvas's.
type layerShape struct {
	stride, w, h, num, denom int
}

// maxPooledShapes bounds the canvas geometries scratch layers are kept
// for: a window and its popups, each resized now and then.
const maxPooledShapes = 4

// scratchLayers holds the released scratch layers by canvas geometry,
// most recently used geometry last. Canvases paint on several
// goroutines (the loop and a grabbed menu's nested loop), so it is
// lock-guarded.
var scratchLayers struct {
	mu     sync.Mutex
	free   map[layerShape][]*Layer
	shapes []layerShape
}

// ScratchLayer is Layer for an offscreen buffer used within one paint:
// it comes from a pool shared by every widget and goes back with
// Release, so a subtree effect costs one buffer per nesting depth and
// canvas size rather than one per widget held for good.
func (c *Canvas) ScratchLayer(region Rect) *Layer {
	k := layerShape{c.stride, c.w, c.h, c.num, c.denom}
	scratchLayers.mu.Lock()
	var l *Layer
	if free := scratchLayers.free[k]; len(free) > 0 {
		l = free[len(free)-1]
		scratchLayers.free[k] = free[:len(free)-1]
	}
	scratchLayers.mu.Unlock()
	l = c.Layer(l, region)
	l.scratch = true
	return l
}

// ScratchSnapshot is Snapshot into a scratch layer.
func (c *Canvas) ScratchSnapshot(region Rect) *Layer {
	l := c.ScratchLayer(region)
	l.copyFrom(c, region)
	return l
}

// Release returns a scratch layer to the pool; l must not be used
// after. Releasing a layer that did not come from ScratchLayer is a
// caller bug.
func (l *Layer) Release() {
	if !l.scratch {
		panic("render: Release of a layer that is not a scratch layer")
	}
	k := layerShape{l.cv.stride, l.cv.w, l.cv.h, l.cv.num, l.cv.denom}
	scratchLayers.mu.Lock()
	defer scratchLayers.mu.Unlock()
	if scratchLayers.free == nil {
		scratchLayers.free = map[layerShape][]*Layer{}
	}
	if i := slices.Index(scratchLayers.shapes, k); i >= 0 {
		scratchLayers.shapes = slices.Delete(scratchLayers.shapes, i, i+1)
	} else if len(scratchLayers.shapes) == maxPooledShapes {
		delete(scratchLayers.free, scratchLayers.shapes[0])
		scratchLayers.shapes = scratchLayers.shapes[1:]
	}
	scratchLayers.shapes = append(scratchLayers.shapes, k)
	scratchLayers.free[k] = append(scratchLayers.free[k], l)
}
