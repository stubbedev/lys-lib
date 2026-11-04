package render

import (
	"sync"
	"sync/atomic"
)

// fallbackFaces resolves the runes a chain's own faces lack off the paint
// goroutine, as a font store reads and parses files to answer.
type fallbackFaces struct {
	resolve    func(rune) *Typeface
	onResolved func()
	generation atomic.Uint64

	mu      sync.Mutex
	done    map[rune]*Typeface
	queue   []rune
	queued  map[rune]struct{}
	running bool
}

func newFallbackFaces(resolve func(rune) *Typeface, onResolved func()) *fallbackFaces {
	return &fallbackFaces{resolve: resolve, onResolved: onResolved, done: map[rune]*Typeface{}, queued: map[rune]struct{}{}}
}

// lookup is r's resolved face, nil for none; false while it is on its way,
// starting it.
func (w *fallbackFaces) lookup(r rune) (*Typeface, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if f, ok := w.done[r]; ok {
		return f, true
	}
	if _, ok := w.queued[r]; ok {
		return nil, false
	}
	w.queued[r] = struct{}{}
	w.queue = append(w.queue, r)
	if !w.running {
		w.running = true
		go w.run()
	}
	return nil, false
}

func (w *fallbackFaces) run() {
	for {
		w.mu.Lock()
		if len(w.queue) == 0 {
			w.running = false
			w.mu.Unlock()
			return
		}
		r := w.queue[0]
		w.queue = w.queue[1:]
		w.mu.Unlock()
		w.settle(r)
	}
}

// settle resolves r unless it is resolved, here on the caller's goroutine.
func (w *fallbackFaces) settle(r rune) {
	w.mu.Lock()
	_, known := w.done[r]
	w.mu.Unlock()
	if known {
		return
	}
	f := w.resolve(r)
	w.mu.Lock()
	w.done[r] = f
	delete(w.queued, r)
	w.mu.Unlock()
	if f != nil {
		w.generation.Add(1)
		if w.onResolved != nil {
			w.onResolved()
		}
	}
}

// generation changes when a resolved face changes how font shapes, which the
// shaping caches key on: lines shaped before it reshape.
func generationOf(font Font) uint64 {
	if c, ok := font.(*Chain); ok && c.fallback != nil {
		return c.fallback.generation.Load()
	}
	return 0
}
