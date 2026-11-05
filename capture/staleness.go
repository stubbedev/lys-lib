package capture

import "github.com/stubbedev/lys-lib/third_party/wayland/wl"

// maxStaleBuffers bounds the buffers a session keeps the staleness of; a
// buffer it forgot is damaged whole.
const maxStaleBuffers = 8

// maxStaleRects bounds a buffer's stale rects: past it they merge into
// their bounds.
const maxStaleRects = 16

// staleBuffer is what changed on the source since buf was last filled,
// under the constraints of generation gen: rects, or all of it when
// whole.
type staleBuffer struct {
	buf   *wl.Buffer
	gen   uint64
	rects []Rect
	whole bool
}

// bufferDamage keeps, per buffer a session fills, the union of the
// damage of the frames published since that buffer was last filled: what
// its next frame asks the compositor to copy.
type bufferDamage struct {
	buffers []staleBuffer
	evict   int
}

func (b *bufferDamage) entry(buf *wl.Buffer) *staleBuffer {
	for i := range b.buffers {
		if b.buffers[i].buf == buf {
			return &b.buffers[i]
		}
	}
	return nil
}

// stale is what buf needs copied under the constraints of generation
// gen: rects, or the whole buffer when whole, as for a buffer never
// filled or filled under other constraints.
func (b *bufferDamage) stale(buf *wl.Buffer, gen uint64) (rects []Rect, whole bool) {
	e := b.entry(buf)
	if e == nil || e.whole || e.gen != gen {
		return nil, true
	}
	return e.rects, false
}

// filled records a frame into buf under generation gen: when ready, its
// damage (empty for the whole frame) goes stale in every other buffer
// and buf is current; otherwise buf holds nothing it can be trusted for.
func (b *bufferDamage) filled(buf *wl.Buffer, gen uint64, damage []Rect, ready bool) {
	if ready {
		for i := range b.buffers {
			if e := &b.buffers[i]; e.buf != buf {
				e.add(damage)
			}
		}
	}
	e := b.entry(buf)
	if e == nil {
		e = b.insert(buf)
	}
	e.gen, e.whole, e.rects = gen, !ready, e.rects[:0]
}

func (b *bufferDamage) insert(buf *wl.Buffer) *staleBuffer {
	if len(b.buffers) < maxStaleBuffers {
		b.buffers = append(b.buffers, staleBuffer{buf: buf, rects: make([]Rect, 0, maxStaleRects)})
		return &b.buffers[len(b.buffers)-1]
	}
	e := &b.buffers[b.evict]
	b.evict = (b.evict + 1) % maxStaleBuffers
	e.buf = buf
	return e
}

func (e *staleBuffer) add(damage []Rect) {
	if len(damage) == 0 {
		e.whole = true
	}
	if e.whole {
		return
	}
	for _, r := range damage {
		if len(e.rects) == maxStaleRects {
			e.rects = append(e.rects[:0], bounds(e.rects))
		}
		e.rects = append(e.rects, r)
	}
}

func bounds(rects []Rect) Rect {
	out := rects[0]
	for _, r := range rects[1:] {
		x0, y0 := min(out.X, r.X), min(out.Y, r.Y)
		x1, y1 := max(out.X+out.Width, r.X+r.Width), max(out.Y+out.Height, r.Y+r.Height)
		out = Rect{X: x0, Y: y0, Width: x1 - x0, Height: y1 - y0}
	}
	return out
}
