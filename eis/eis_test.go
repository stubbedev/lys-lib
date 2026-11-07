package eis

import (
	"slices"
	"testing"
)

// An empty seat name is refused by Pair and Serve alike, before any
// socket is made or any byte is sent; it never falls back to a default.
func TestAnEmptySeatNameIsRefused(t *testing.T) {
	const want = "eis: the seat name is empty: the client lists the seat and its devices by it"
	if f, err := Pair(&recorder{}, "", nil); f != nil || err == nil || err.Error() != want {
		t.Errorf("Pair with no seat = %v, %v; want %q", f, err, want)
	}
	if err := Serve(nil, &recorder{}, "", nil); err == nil || err.Error() != want {
		t.Errorf("Serve with no seat = %v; want %q", err, want)
	}
}

// A region without an area or a positive scale is refused before
// anything is served: no absolute event could ever land in it.
func TestARegionWithoutAnAreaIsRefused(t *testing.T) {
	const want = "eis: region 1 is 0x1080 at scale 1: a region needs an area and a positive scale"
	regions := []Region{{Width: 1920, Height: 1080, Scale: 1}, {X: 1920, Height: 1080, Scale: 1}}
	if f, err := Pair(&recorder{}, testSeat, regions); f != nil || err == nil || err.Error() != want {
		t.Errorf("Pair = %v, %v; want %q", f, err, want)
	}
	if err := Serve(nil, &recorder{}, testSeat, []Region{{Width: 1, Height: 1}}); err == nil {
		t.Error("Serve took a region at scale 0")
	}
}

// Absolute motion and touches outside every region never reach the
// Handler, and neither does anything after a touch that went down
// outside them; touches inside go through, an up ending each.
func TestAbsoluteEventsOutsideTheRegionsAreDropped(t *testing.T) {
	rec := &recorder{}
	s := &server{
		h: rec, regions: []Region{{X: 100, Y: 0, Width: 200, Height: 100, Scale: 1}},
		objects: map[uint64]string{1: ifAbsolute, 2: ifTouch}, touches: map[uint32]struct{}{},
	}
	send := func(object uint64, opcode uint32, w *writer) {
		t.Helper()
		if _, err := s.dispatch(message{object: object, opcode: opcode, args: w.bytes()[headerSize:]}); err != nil {
			t.Fatal(err)
		}
	}
	at := func(x, y float32) *writer { return newMessage(0, 0).f32(x).f32(y) }
	touch := func(id uint32) *writer { return newMessage(0, 0).u32(id) }
	send(1, 1, at(150, 50))
	send(1, 1, at(50, 50))
	send(1, 1, at(300, 50))
	send(2, 1, touch(7).f32(120).f32(10))
	send(2, 2, touch(7).f32(130).f32(20))
	send(2, 2, touch(7).f32(10).f32(20))
	send(2, 3, touch(7))
	send(2, 1, touch(8).f32(10).f32(10))
	send(2, 2, touch(8).f32(150).f32(50))
	send(2, 3, touch(8))
	send(2, 1, touch(9).f32(299).f32(99))
	send(2, 4, touch(9))
	send(2, 3, touch(9))
	want := []string{
		"absolute 150 50",
		"down 7 120 10", "touch 7 130 20", "up 7",
		"down 9 299 99", "cancel 9",
	}
	if got := rec.list(); !slices.Equal(got, want) {
		t.Errorf("handled %q, want %q", got, want)
	}
}
