package pipewire

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/pipewire/pwtest"
)

// testCursor is a Cursor whose shape n is an n x n image of byte n.
type testCursor struct {
	mu    sync.Mutex
	state CursorState
	wake  func()
	// bitmaps counts the images written.
	bitmaps atomic.Int64
}

func (c *testCursor) CursorState() CursorState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *testCursor) CursorBitmap(shape uint64, dst []byte, side int) (CursorBitmap, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if shape != c.state.Shape {
		return CursorBitmap{}, false
	}
	n := min(int(shape), side)
	for i := range n * n * 4 {
		dst[i] = byte(shape)
	}
	c.bitmaps.Add(1)
	return CursorBitmap{Format: VideoBGRA, Width: uint32(n), Height: uint32(n), Stride: uint32(n) * 4}, true
}

func (c *testCursor) NotifyCursor(wake func()) {
	c.mu.Lock()
	c.wake = wake
	c.mu.Unlock()
}

func (c *testCursor) set(change func(*CursorState)) {
	c.mu.Lock()
	change(&c.state)
	wake := c.wake
	c.mu.Unlock()
	wake()
}

// still is a source whose one frame never changes: it fills once, and
// again only for new buffers. fills counts the frames it filled.
type still struct {
	sent  atomic.Bool
	fills atomic.Int64
}

func (s *still) Fill(dst []byte) ([]Rect, bool) {
	if s.sent.Swap(true) {
		return nil, false
	}
	s.fills.Add(1)
	for i := range dst {
		dst[i] = 0x33
	}
	return nil, true
}

func (s *still) Restart() { s.sent.Store(false) }

// cursorStream streams src with cur (none when nil) into a linked
// reference consumer.
func cursorStream(t *testing.T, src Source, cur Cursor) *consumer {
	t.Helper()
	pwtest.Start(t)
	p, err := Start(Config{Application: testApplication, Name: testNode, Width: 64, Height: 32, Stride: 256, Format: VideoBGRx, FPS: 240, Source: src, Cursor: cur})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	loop, err := startLoop(l)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.stop)
	c := newConsumer(t, loop, 64, 32)
	t.Cleanup(func() {
		loop.do(func() {
			l.streamDisconnect(c.stream)
			l.streamDestroy(c.stream)
		})
	})
	link(t)
	return c
}

// await returns the first buffer the consumer receives that ok accepts,
// passing every buffer before it to seen.
func await(t *testing.T, c *consumer, what string, ok func(consumed) bool, seen func(consumed)) consumed {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f := <-c.frames:
			if ok(f) {
				return f
			}
			seen(f)
		case <-deadline:
			t.Fatalf("the consumer received no buffer with %s", what)
		}
	}
}

func at(x, y int32) func(consumed) bool {
	return func(f consumed) bool {
		return f.cursor != nil && f.cursor.head.id != 0 && f.cursor.head.x == x && f.cursor.head.y == y
	}
}

func bitmapOf(shape uint64) func(consumed) bool {
	return func(f consumed) bool {
		return f.cursor != nil && f.cursor.bitmap != nil && f.cursor.bitmap.width == uint32(shape)
	}
}

// A moving pointer over a still frame sends a buffer per move holding
// only the cursor meta (an empty, corrupted chunk) and fills no frame;
// the bitmap goes out once per shape, in the buffer that first shows it.
func TestAMovingPointerSendsCursorMetasAndNoFrames(t *testing.T) {
	src := &still{}
	cur := &testCursor{state: CursorState{Visible: true, X: 5, Y: 6, HotspotX: 1, HotspotY: 2, Shape: 3}}
	c := cursorStream(t, src, cur)
	first := await(t, c, "the first frame", func(f consumed) bool { return f.size > 0 }, func(consumed) {})
	if first.cursor == nil || first.cursor.head.id == 0 {
		t.Fatalf("the first frame carries no cursor: %+v", first)
	}
	frameBitmaps := 0
	if first.cursor.bitmap != nil {
		frameBitmaps = 1
	}
	var stray []string
	count := func(f consumed) {
		if f.size > 0 {
			stray = append(stray, fmt.Sprintf("a frame %+v", f))
		}
		if f.cursor != nil && f.cursor.bitmap != nil {
			stray = append(stray, fmt.Sprintf("a bitmap %+v", *f.cursor.bitmap))
		}
	}
	if frameBitmaps == 0 {
		await(t, c, "shape 3's bitmap", bitmapOf(3), count)
	}
	const moves = 30
	for i := range int32(moves) {
		cur.set(func(s *CursorState) { s.X, s.Y = 10+i, 20+i })
		f := await(t, c, fmt.Sprintf("the pointer at %d,%d", 10+i, 20+i), at(10+i, 20+i), count)
		if f.size != 0 || f.flags&chunkFlagCorrupted == 0 {
			t.Fatalf("move %d came in a buffer of %d bytes, flags %#x", i, f.size, f.flags)
		}
		if f.cursor.bitmap != nil {
			t.Fatalf("move %d resent the bitmap", i)
		}
		if f.cursor.head.hotX != 1 || f.cursor.head.hotY != 2 {
			t.Fatalf("move %d hotspot %d,%d", i, f.cursor.head.hotX, f.cursor.head.hotY)
		}
	}
	if len(stray) > 0 {
		t.Fatalf("the moves brought %v", stray)
	}
	if n := src.fills.Load(); n != 1 {
		t.Fatalf("%d moves over a still frame filled %d frames, want the 1 first", moves, n)
	}
	for _, shape := range []uint64{4, 7} {
		cur.set(func(s *CursorState) { s.Shape = shape })
		f := await(t, c, fmt.Sprintf("shape %d's bitmap", shape), bitmapOf(shape), func(consumed) {})
		bm := f.cursor.bitmap
		if bm.height != uint32(shape) || bm.stride != int32(shape)*4 || bm.format != uint32(VideoBGRA) || !bytes.Equal(f.cursor.pixels, bytes.Repeat([]byte{byte(shape)}, int(shape*shape*4))) {
			t.Fatalf("shape %d's bitmap %+v pixels %v", shape, *bm, f.cursor.pixels)
		}
	}
	if got := cur.bitmaps.Load(); got != 3 {
		t.Fatalf("3 shapes wrote %d bitmaps", got)
	}
	cur.set(func(s *CursorState) { s.Visible = false })
	f := await(t, c, "the pointer leaving", func(f consumed) bool { return f.cursor != nil && f.cursor.bitmap != nil }, func(consumed) {})
	if f.cursor.head.id == 0 || f.cursor.bitmap.width != 0 || f.cursor.bitmap.height != 0 {
		t.Fatalf("a hidden pointer sent %+v", *f.cursor)
	}
	cur.set(func(s *CursorState) { s.Visible, s.X = true, 99 })
	f = await(t, c, "the pointer back", at(99, 49), func(consumed) {})
	if f.cursor.bitmap == nil || f.cursor.bitmap.width != 7 {
		t.Fatalf("the pointer came back without its bitmap: %+v", *f.cursor)
	}
}

// Without a Cursor the stream negotiates no cursor meta, even for a
// consumer that asks for one.
func TestAStreamWithoutACursorCarriesNoCursorMeta(t *testing.T) {
	c := cursorStream(t, damaged{}, nil)
	for range 3 {
		f := await(t, c, "a frame", func(f consumed) bool { return f.size > 0 }, func(consumed) {})
		if f.cursor != nil {
			t.Fatalf("a frame carries a cursor meta %+v", *f.cursor)
		}
	}
}

// The sender says nothing new of a pointer that stays hidden, tells a
// change only once, and retries a bitmap the cursor moved on from.
func TestCursorSenderStamps(t *testing.T) {
	mem := make([]byte, cursorMetaSize(8))
	m, err := layCursorMeta(mem, 0, uint32(len(mem)))
	if err != nil {
		t.Fatal(err)
	}
	if m.side() != 8 {
		t.Fatalf("a meta for 8x8 holds %dx%d", m.side(), m.side())
	}
	cur := &testCursor{}
	s := &cursorSender{source: cur}
	if !s.pending() {
		t.Fatal("a fresh sender has nothing to tell")
	}
	stamp := func() {
		t.Helper()
		if err := s.stamp(m); err != nil {
			t.Fatal(err)
		}
	}
	stamp()
	if m.head.id != 1 || m.head.bitmapOffset == 0 || m.bitmap.width != 0 {
		t.Fatalf("the first stamp of a hidden pointer %+v %+v", *m.head, *m.bitmap)
	}
	stamp()
	if m.head.id != 0 || s.pending() {
		t.Fatalf("a still hidden pointer stamped %+v, pending %v", *m.head, s.pending())
	}
	cur.state = CursorState{Visible: true, X: 3, Y: 4, Shape: 9}
	if !s.pending() {
		t.Fatal("a pointer that appeared is not news")
	}
	stamp()
	if m.head.id != 1 || m.head.bitmapOffset == 0 || m.bitmap.width != 8 || m.bitmap.stride != 32 {
		t.Fatalf("a 9x9 shape in room for 8x8 stamped %+v %+v", *m.head, *m.bitmap)
	}
	stamp()
	if m.head.id != 1 || m.head.bitmapOffset != 0 || s.pending() {
		t.Fatalf("an unchanged pointer stamped %+v, pending %v", *m.head, s.pending())
	}
	raced := &racingCursor{testCursor: cur}
	s.source = raced
	cur.state.Shape = 10
	stamp()
	if m.head.bitmapOffset != 0 || !s.pending() {
		t.Fatalf("a shape gone before its bitmap stamped %+v, pending %v", *m.head, s.pending())
	}
	s.source = cur
	stamp()
	if m.head.bitmapOffset == 0 || s.pending() {
		t.Fatalf("the retried bitmap stamped %+v, pending %v", *m.head, s.pending())
	}
	s.source = oversized{cur}
	cur.state.Shape = 11
	if err := s.stamp(m); err == nil {
		t.Fatal("a bitmap past the meta's room was stamped")
	}
}

// racingCursor's shape always changed before its bitmap is read.
type racingCursor struct{ *testCursor }

func (racingCursor) CursorBitmap(uint64, []byte, int) (CursorBitmap, bool) {
	return CursorBitmap{}, false
}

// oversized claims a bitmap larger than it was given room for.
type oversized struct{ *testCursor }

func (oversized) CursorBitmap(_ uint64, _ []byte, side int) (CursorBitmap, bool) {
	return CursorBitmap{Format: VideoBGRA, Width: uint32(side + 1), Height: 1, Stride: uint32(side+1) * 4}, true
}
