// Package vp9test makes the VP9 frames libvpx codes none of, out of
// the ones it does, for tests of what reads and decodes them: frames
// that show a frame again, hidden intra-only frames, and key frames
// that code a frame context of their own.
package vp9test

import (
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/internal/vp9"
)

// Headers reads the headers of the frames in each of packets, a stream
// from its first key frame.
func Headers(t *testing.T, packets [][]byte) [][]vp9.FrameHeader {
	t.Helper()
	var s vp9.Stream
	var scratch [vp9.MaxFrames][]byte
	out := make([][]vp9.FrameHeader, len(packets))
	for i, p := range packets {
		frames, err := vp9.Frames(p, &scratch)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = make([]vp9.FrameHeader, len(frames))
		for j, f := range frames {
			if err := s.ParseFrame(&out[i][j], f); err != nil {
				t.Fatalf("packet %d frame %d: %v", i, j, err)
			}
			s.Update(&out[i][j])
		}
	}
	return out
}

// ShowExisting is a profile 0 frame that shows the frame in reference
// frame slot: a frame marker, the profile and show_existing_frame,
// then the slot.
func ShowExisting(slot uint8) []byte { return []byte{0x88 | slot} }

// bitString is bits written first bit first.
type bitString struct {
	b []byte
	n int
}

func (w *bitString) put(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		w.b[w.n/8] |= byte(v>>i&1) << (7 - w.n%8)
		w.n++
	}
}

// from appends bits from to to of b.
func (w *bitString) from(b []byte, from, to int) {
	for i := from; i < to; i++ {
		w.put(uint32(b[i/8]>>(7-i%8)&1), 1)
	}
}

// Where a shown profile 0 key frame's uncompressed header has its sync
// code, its frame size, and what follows its render size when that is
// the frame's; contextBits is how many bits frame_context_idx takes.
const (
	keySync     = 8
	keySize     = 36
	keySized    = 69
	contextBits = 2
)

// keyHeader reads key, a shown profile 0 key frame, and says where its
// header's refresh_frame_context is, or, error resilient, where that
// would be: frame_context_idx.
func keyHeader(t *testing.T, key []byte) (h vp9.FrameHeader, tail int) {
	t.Helper()
	if key[0]&^1 != 0x82 {
		t.Fatalf("a frame starting %#x is no shown profile 0 key frame", key[0])
	}
	var s vp9.Stream
	if err := s.ParseFrame(&h, key); err != nil {
		t.Fatal(err)
	}
	tail = keySized
	if h.RenderAndFrameSizeDifferent {
		tail += 32
	}
	return h, tail
}

// withHeader is key with header for its uncompressed header, which
// ends within header's last byte or before it: read back, it says
// where.
func withHeader(t *testing.T, key []byte, was *vp9.FrameHeader, header []byte) ([]byte, vp9.FrameHeader) {
	t.Helper()
	var is vp9.FrameHeader
	parse := func(header []byte) []byte {
		frame := slices.Concat(header, key[was.HeaderBytes:])
		var s vp9.Stream
		if err := s.ParseFrame(&is, frame); err != nil {
			t.Fatal(err)
		}
		return frame
	}
	parse(header)
	return parse(header[:is.HeaderBytes]), is
}

// IntraOnly is the header of a hidden intra-only frame made of a key
// frame: whether it is error resilient, which resets every frame
// context; else its reset_frame_context; its refresh_frame_flags; and
// the frame_context_idx it codes, which is the context a
// reset_frame_context of 2 resets and no more: the frame decodes from
// context 0 and saves to it, as every intra frame does.
type IntraOnly struct {
	Resilient bool
	Reset     uint8
	Refresh   uint8
	Context   uint8
}

// Of turns key, a shown profile 0 key frame, into the hidden
// intra-only frame to says, which decodes to the key frame's picture
// where frame context 0 is the default one when it decodes: an
// intra-only frame's compressed header and tiles are coded as a key
// frame's, and of the uncompressed header only what comes before the
// frame size differs. A key frame that is error resilient codes neither
// refresh_frame_context nor frame_parallel_decoding_mode, which an
// intra-only frame that is not takes as that mode implies them.
func (to IntraOnly) Of(t *testing.T, key []byte) []byte {
	t.Helper()
	was, tail := keyHeader(t, key)
	var w bitString
	w.put(0b1000010, 7)
	w.put(bit(to.Resilient), 1)
	w.put(1, 1)
	if !to.Resilient {
		w.put(uint32(to.Reset), 2)
	}
	w.from(key, keySync, keySync+24)
	w.put(uint32(to.Refresh), 8)
	w.from(key, keySize, tail)
	switch {
	case was.ErrorResilientMode && !to.Resilient:
		w.put(0b01, 2)
	case was.ErrorResilientMode != to.Resilient:
		t.Fatal("an error resilient frame made of one that is not would lose the contexts the frames after it decode from")
	case !was.ErrorResilientMode:
		w.from(key, tail, tail+2)
		tail += 2
	}
	w.put(uint32(to.Context), contextBits)
	w.from(key, tail+contextBits, 8*was.HeaderBytes)
	frame, is := withHeader(t, key, &was, w.b)
	if !is.IntraOnly || is.ShowFrame || is.RefreshFrameFlags != to.Refresh || is.RefreshFrameContext != was.RefreshFrameContext || !is.FrameParallelDecodingMode ||
		is.CompressedBytes != was.CompressedBytes || is.Quant != was.Quant || is.LoopFilter != was.LoopFilter || is.Seg != was.Seg || is.FrameWidth != was.FrameWidth || is.FrameHeight != was.FrameHeight {
		t.Fatalf("the intra-only frame's header reads %+v, the key frame's %+v", is, was)
	}
	return frame
}

// KeyContext is key, a shown profile 0 key frame, coding context for
// its frame_context_idx: a field libvpx writes 0 in and reads nothing
// from, a key frame decoding from context 0 and saving to it whatever
// the field says.
func KeyContext(t *testing.T, key []byte, context uint8) []byte {
	t.Helper()
	was, tail := keyHeader(t, key)
	if !was.ErrorResilientMode {
		tail += 2
	}
	var w bitString
	w.from(key, 0, tail)
	w.put(uint32(context), contextBits)
	w.from(key, tail+contextBits, 8*was.HeaderBytes)
	frame, is := withHeader(t, key, &was, w.b)
	if is.FrameType != vp9.KeyFrame || is.CompressedBytes != was.CompressedBytes || is.Quant != was.Quant || is.LoopFilter != was.LoopFilter || len(frame) != len(key) {
		t.Fatalf("the key frame's header reads %+v, and read %+v", is, was)
	}
	return frame
}

func bit(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}
