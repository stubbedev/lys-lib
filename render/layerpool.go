package render

import (
	"slices"
	"sync"
)

// maxPooledLayers bounds the scratch layers kept for reuse: a few
// nesting depths of effects, on a window and its popups.
const maxPooledLayers = 6

// scratchLayers holds the released scratch layers. Any big enough
// buffer serves any canvas, so a resize, which changes the canvas's
// geometry every frame, reuses them instead of allocating. Canvases
// paint on several goroutines (the loop and a grabbed menu's nested
// loop), so it is lock-guarded.
var scratchLayers struct {
	mu   sync.Mutex
	free []*Layer
}

// ScratchLayer is Layer for an offscreen buffer used within one paint:
// it comes from a pool shared by every widget and goes back with
// Release, so a subtree effect costs one buffer per nesting depth
// rather than one per widget held for good, and none at a new size
// while a pooled buffer is big enough.
func (c *Canvas) ScratchLayer(region Rect) *Layer {
	need := c.stride * c.h
	scratchLayers.mu.Lock()
	var l *Layer
	best := -1
	for i, f := range scratchLayers.free {
		if cap(f.buf) >= need && (best < 0 || cap(f.buf) < cap(scratchLayers.free[best].buf)) {
			best = i
		}
	}
	if best >= 0 {
		l = scratchLayers.free[best]
		scratchLayers.free = slices.Delete(scratchLayers.free, best, best+1)
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
// after. A full pool keeps its largest buffers. Releasing a layer that
// did not come from ScratchLayer is a caller bug.
func (l *Layer) Release() {
	if !l.scratch {
		panic("render: Release of a layer that is not a scratch layer")
	}
	scratchLayers.mu.Lock()
	defer scratchLayers.mu.Unlock()
	scratchLayers.free = append(scratchLayers.free, l)
	if len(scratchLayers.free) > maxPooledLayers {
		smallest := 0
		for i, f := range scratchLayers.free {
			if cap(f.buf) < cap(scratchLayers.free[smallest].buf) {
				smallest = i
			}
		}
		scratchLayers.free = slices.Delete(scratchLayers.free, smallest, smallest+1)
	}
}
