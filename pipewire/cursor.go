package pipewire

import (
	"fmt"
	"math"
	"unsafe"

	"github.com/stubbedev/lys-lib/intconv"
)

// Cursor is the pointer a stream carries beside its frames as an
// SPA_META_Cursor, the ScreenCast portal's metadata cursor mode: every
// buffer says where the pointer is, a buffer carries the pointer's bitmap
// only when its shape changed, and a change of the pointer alone sends a
// buffer that holds no frame (a zero-size, corrupted chunk).
type Cursor interface {
	// CursorState is the pointer now.
	CursorState() CursorState
	// CursorBitmap writes the image of shape into dst as rows of pixels
	// without padding, cropped from the top left to at most side x side
	// pixels and to what dst holds, and says how it laid it out. It
	// reports false when shape is no longer the pointer's.
	CursorBitmap(shape uint64, dst []byte, side int) (CursorBitmap, bool)
	// NotifyCursor is called once, before any read. The cursor calls
	// wake, from any goroutine and without blocking, whenever its state
	// changed.
	NotifyCursor(wake func())
}

// CursorState is where the pointer is over the stream's frames.
type CursorState struct {
	// Visible is whether the pointer is over the frames; the rest means
	// nothing while it is not.
	Visible bool
	// X and Y are where the pointer's hotspot is, in frame pixels.
	X, Y int32
	// HotspotX and HotspotY are the hotspot's place in the bitmap.
	HotspotX, HotspotY int32
	// Shape numbers the pointer's image, which changes only with it; 0 is
	// no image yet.
	Shape uint64
}

// CursorBitmap is how a Cursor laid out the image it wrote.
type CursorBitmap struct {
	Format        VideoFormat
	Width, Height uint32
	Stride        uint32
}

// CursorBitmapSide is the largest cursor image a stream's buffers have
// room for, in pixels each way; a larger one is cropped.
const CursorBitmapSide = 256

const (
	metaCursor = 5

	cursorHeaderSize = uint32(unsafe.Sizeof(cMetaCursor{}))
	bitmapHeaderSize = uint32(unsafe.Sizeof(cMetaBitmap{}))
	cursorPixelsAt   = cursorHeaderSize + bitmapHeaderSize
	cursorPixelBytes = 4
)

// cursorMetaSize is the size of a cursor meta with room for a side x
// side bitmap.
func cursorMetaSize(side uint32) uint32 {
	return cursorPixelsAt + side*side*cursorPixelBytes
}

// cursorMetaPod is the Cursor meta, sized for a CursorBitmapSide bitmap.
func cursorMetaPod() []byte {
	size := intconv.Must[int32](cursorMetaSize(CursorBitmapSide))
	return object(objectParamMeta, paramMeta,
		prop{key: metaKeyType, value: podID(metaCursor)},
		prop{key: metaKeySize, value: podIntRange(size, intconv.Must[int32](cursorMetaSize(1)), size)},
	)
}

// cursorMeta is one buffer's cursor meta: its header, and the room
// behind it for the bitmap header and pixels.
type cursorMeta struct {
	head   *cMetaCursor
	bitmap *cMetaBitmap
	pixels []byte
}

// layCursorMeta points a cursor meta of size bytes at at in mem.
func layCursorMeta(mem []byte, at, size uint32) (cursorMeta, error) {
	if size < cursorPixelsAt || uint64(at)+uint64(size) > uint64(len(mem)) {
		return cursorMeta{}, fmt.Errorf("a %d-byte cursor meta at %d of a %d-byte area", size, at, len(mem))
	}
	head, err := view[cMetaCursor](mem, at)
	if err != nil {
		return cursorMeta{}, err
	}
	bitmap, err := view[cMetaBitmap](mem, at+cursorHeaderSize)
	if err != nil {
		return cursorMeta{}, err
	}
	return cursorMeta{head: head, bitmap: bitmap, pixels: mem[at+cursorPixelsAt : at+size]}, nil
}

// side is the largest square bitmap the meta holds.
func (m cursorMeta) side() uint32 {
	return min(uint32(math.Sqrt(float64(len(m.pixels)/cursorPixelBytes))), CursorBitmapSide)
}

// cursorSender is what a producer has told its consumer of the pointer.
type cursorSender struct {
	source Cursor
	// synced is whether sent is what the consumer knows: false before
	// the first buffer and after the buffers were dropped.
	synced bool
	sent   CursorState
	// shown is the shape whose bitmap the consumer has; 0 is none, or the
	// empty bitmap of a hidden pointer.
	shown uint64
}

// state is the pointer now, with nothing but its visibility while it is
// hidden.
func (c *cursorSender) state() CursorState {
	s := c.source.CursorState()
	if !s.Visible {
		return CursorState{}
	}
	return s
}

// pending reports whether the consumer is behind the pointer: a buffer
// without a frame has news to carry.
func (c *cursorSender) pending() bool {
	s := c.state()
	return !c.synced || s != c.sent || c.wantsBitmap(s)
}

func (c *cursorSender) wantsBitmap(s CursorState) bool {
	return s.Visible && s.Shape != 0 && s.Shape != c.shown
}

// reset has the next buffer tell the consumer everything again.
func (c *cursorSender) reset() { c.synced, c.shown = false, 0 }

// stamp writes the pointer into m: nothing new, its position, its
// position and bitmap, or the empty bitmap of a pointer that left. A
// bitmap the source laid out past what it was given is an error.
func (c *cursorSender) stamp(m cursorMeta) error {
	s := c.state()
	wasHidden := c.synced && !c.sent.Visible
	c.synced, c.sent = true, s
	h := m.head
	if !s.Visible {
		if wasHidden {
			*h = cMetaCursor{}
			return nil
		}
		*h = cMetaCursor{id: 1, bitmapOffset: cursorHeaderSize}
		*m.bitmap = cMetaBitmap{format: uint32(VideoRGBA), offset: bitmapHeaderSize}
		c.shown = 0
		return nil
	}
	*h = cMetaCursor{id: 1, x: s.X, y: s.Y, hotX: s.HotspotX, hotY: s.HotspotY}
	if !c.wantsBitmap(s) {
		return nil
	}
	side := m.side()
	bm, ok := c.source.CursorBitmap(s.Shape, m.pixels, int(side))
	if !ok {
		return nil
	}
	if bm.Width > side || bm.Height > side || bm.Stride > side*cursorPixelBytes || uint64(bm.Stride)*uint64(bm.Height) > uint64(len(m.pixels)) {
		return fmt.Errorf("pipewire: a %dx%d cursor bitmap of stride %d, in room for %dx%d", bm.Width, bm.Height, bm.Stride, side, side)
	}
	h.bitmapOffset = cursorHeaderSize
	*m.bitmap = cMetaBitmap{format: uint32(bm.Format), width: bm.Width, height: bm.Height, stride: int32(bm.Stride), offset: bitmapHeaderSize} //nolint:gosec // checked against the room above
	c.shown = s.Shape
	return nil
}
