package capture

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/stubbedev/lys-lib/wlr"
)

// CursorState is the pointer over a Cursor's source.
type CursorState struct {
	// Inside is whether the pointer is over the source.
	Inside bool
	// X and Y are where the pointer's hotspot is in the source, in its
	// buffer pixels, as of when it was last inside.
	X, Y int
	// HotspotX and HotspotY are the hotspot's place in the image.
	HotspotX, HotspotY int
	// Shape numbers the image: it grows with each new one the compositor
	// hands out, and is 0 until the first.
	Shape uint64
	// Width, Height, Stride and Format are the image's layout.
	Width, Height, Stride int
	Format                Format
}

// Cursor follows the pointer over one output or window through an
// ext-image-copy-capture cursor session, on its own connection and
// goroutine: the pointer's position and hotspot as the compositor
// reports them, and its image, copied anew only when the compositor
// says it changed. Err says why the loop ended on its own:
// ErrSessionStopped, ErrFailed after repeated refusals, or a dispatch
// failure.
type Cursor struct {
	captureLoop
	frame frameState

	// mu guards state, the image buffers and onChange against the
	// capture goroutine.
	mu       sync.Mutex
	state    CursorState
	images   [2]*shmBuffer
	front    int
	unmapped bool
	onChange func()
}

// OpenOutputCursor follows the pointer over the output with the given
// connector name. ErrUnsupported means the compositor offers no cursor
// sessions or the seat has no pointer.
func OpenOutputCursor(outputName string) (*Cursor, error) {
	return openCursor(func(c *Client) (Source, error) {
		out, ok := c.OutputByName(outputName)
		if !ok {
			return nil, fmt.Errorf("capture: output %q not found", outputName)
		}
		return out, nil
	})
}

// OpenToplevelCursor follows the pointer over the window with the given
// ext-foreign-toplevel identifier. ErrUnsupported means the compositor
// offers no cursor sessions on windows or the seat has no pointer.
func OpenToplevelCursor(identifier string) (*Cursor, error) {
	return openCursor(func(c *Client) (Source, error) {
		list, err := c.Toplevels()
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(list, func(t Toplevel) bool { return t.Identifier == identifier })
		if i < 0 {
			return nil, fmt.Errorf("capture: window %q not found", identifier)
		}
		return list[i], nil
	})
}

func openCursor(find func(*Client) (Source, error)) (*Cursor, error) {
	client, err := Connect()
	if err != nil {
		return nil, err
	}
	src, err := find(client)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	k := &Cursor{captureLoop: newCaptureLoop(client), front: -1}
	cs, err := k.open(src)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	go k.run(cs)
	return k, nil
}

// open creates the cursor session on src and its capture session, and
// roundtrips so the state holds where the pointer is. The image's first
// constraints are left to the capture goroutine: a compositor names
// them only once the pointer has an image.
func (k *Cursor) open(src Source) (*copySession, error) {
	c := k.client
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.usable(); err != nil {
		return nil, err
	}
	if !c.HasCursorCapture() {
		return nil, fmt.Errorf("%w: %s with a seat pointer", ErrUnsupported, ifaceCopyCapture)
	}
	if c.pointer == nil {
		pointer, err := c.seat.GetPointer()
		if err != nil {
			return nil, fmt.Errorf("capture: get the seat's pointer: %w", err)
		}
		c.pointer = pointer
	}
	source, release, err := src.imageSource(c)
	if err != nil {
		return nil, err
	}
	cursor, err := c.copyCapture.CreatePointerCursorSession(source, c.pointer)
	if err != nil {
		release()
		return nil, errors.Join(fmt.Errorf("capture: create a cursor session: %w", err), source.Destroy())
	}
	events := cursorEvents{k}
	cursor.AddEnterHandler(events)
	cursor.AddLeaveHandler(events)
	cursor.AddPositionHandler(events)
	cursor.AddHotspotHandler(events)
	session, err := cursor.GetCaptureSession()
	if err != nil {
		release()
		return nil, errors.Join(fmt.Errorf("capture: get the cursor's capture session: %w", err), cursor.Destroy(), source.Destroy())
	}
	cs := newCopySession(source, release, session, cursor)
	if err := c.roundtrip(); err != nil {
		return nil, errors.Join(err, cs.destroy())
	}
	if err := cs.usable(); err != nil {
		return nil, errors.Join(err, cs.destroy())
	}
	return cs, nil
}

// OnChange has wake called, from the capture goroutine and without its
// state locked, whenever the state changes. wake must not block.
func (k *Cursor) OnChange(wake func()) {
	k.mu.Lock()
	k.onChange = wake
	k.mu.Unlock()
}

// State is the pointer now.
func (k *Cursor) State() CursorState {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.state
}

// ReadImage copies the image numbered shape into dst as rows of its
// format's pixels without padding, cropped from the top left to at most
// maxWidth x maxHeight and to what dst holds, and reports the size
// copied. It reports false when shape is no longer the current image or
// the cursor is closed.
func (k *Cursor) ReadImage(shape uint64, dst []byte, maxWidth, maxHeight int) (width, height int, ok bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.unmapped || k.front < 0 || shape != k.state.Shape {
		return 0, 0, false
	}
	img := k.images[k.front]
	row := min(img.width, maxWidth) * img.format.BytesPerPixel()
	if row <= 0 {
		return 0, 0, false
	}
	height = min(img.height, maxHeight, len(dst)/row)
	for y := range height {
		copy(dst[y*row:(y+1)*row], img.data[y*img.stride:])
	}
	return row / img.format.BytesPerPixel(), height, true
}

// Close stops the capture goroutine, waits for it, and drops the
// connection and the images.
func (k *Cursor) Close() error {
	if !k.stop() {
		return nil
	}
	k.mu.Lock()
	for _, b := range k.images {
		if b != nil {
			b.unmap()
		}
	}
	k.unmapped = true
	k.mu.Unlock()
	return k.client.Close()
}

// run is the capture loop: wait for the image's constraints, capture
// the image into the buffer not published, publish it when ready, until
// the session stops or Close.
func (k *Cursor) run(cs *copySession) {
	defer close(k.done)
	defer func() { _ = cs.destroy() }()
	wait := k.wait()
	failures := 0
	for !k.stopping.Load() {
		if err := wait.until(func() bool { return cs.state.stopped || cs.state.generation > 0 }); err != nil {
			k.fail(err)
			return
		}
		if k.stopping.Load() {
			return
		}
		if err := cs.usable(); err != nil {
			k.fail(err)
			return
		}
		buf, err := k.back(cs.state.current)
		if err != nil {
			k.fail(err)
			return
		}
		if err := cs.captureFrame(wait, buf.wl, buf.wlWidth, buf.wlHeight, &k.frame); err != nil {
			k.fail(err)
			return
		}
		if !k.frame.finished() && !cs.state.stopped {
			continue
		}
		err = cs.verdict(&k.frame)
		switch {
		case err == nil:
			failures = 0
			k.publish()
		case errors.Is(err, ErrSessionStopped):
			k.fail(err)
			return
		case !fits(buf, cs.state.current):
		default:
			failures++
			if failures >= maxStreamFailures {
				k.fail(err)
				return
			}
		}
	}
}

// back is the buffer the next image is captured into, allocated anew
// when the one there does not fit the constraints.
func (k *Cursor) back(cons constraints) (*shmBuffer, error) {
	i := (k.front + 1) % len(k.images)
	if buf := k.images[i]; buf != nil && fits(buf, cons) {
		return buf, nil
	}
	format, stride, err := cons.cursorLayout()
	if err != nil {
		return nil, err
	}
	buf, err := newShmBuffer(k.client.shm, cons.width, cons.height, stride, format)
	if err != nil {
		return nil, err
	}
	if old := k.images[i]; old != nil {
		old.release()
	}
	k.images[i] = buf
	return buf, nil
}

// fits reports whether buf has the layout an image under cons takes.
func fits(buf *shmBuffer, cons constraints) bool {
	format, stride, err := cons.cursorLayout()
	return err == nil && buf.matches(cons.width, cons.height, stride, format)
}

// publish makes the image just captured the current one.
func (k *Cursor) publish() {
	k.update(func(s *CursorState) {
		k.front = (k.front + 1) % len(k.images)
		img := k.images[k.front]
		s.Shape++
		s.Width, s.Height, s.Stride, s.Format = img.width, img.height, img.stride, img.format
	})
}

// update changes the state and tells the OnChange callback.
func (k *Cursor) update(change func(*CursorState)) {
	k.mu.Lock()
	change(&k.state)
	wake := k.onChange
	k.mu.Unlock()
	if wake != nil {
		wake()
	}
}

// cursorEvents takes a cursor session's events into its Cursor's state.
type cursorEvents struct{ k *Cursor }

// HandleImageCopyCaptureCursorSessionV1Enter implements the enter
// handler.
func (e cursorEvents) HandleImageCopyCaptureCursorSessionV1Enter(wlr.ImageCopyCaptureCursorSessionV1EnterEvent) {
	e.k.update(func(s *CursorState) { s.Inside = true })
}

// HandleImageCopyCaptureCursorSessionV1Leave implements the leave
// handler.
func (e cursorEvents) HandleImageCopyCaptureCursorSessionV1Leave(wlr.ImageCopyCaptureCursorSessionV1LeaveEvent) {
	e.k.update(func(s *CursorState) { s.Inside = false })
}

// HandleImageCopyCaptureCursorSessionV1Position implements the position
// handler.
func (e cursorEvents) HandleImageCopyCaptureCursorSessionV1Position(ev wlr.ImageCopyCaptureCursorSessionV1PositionEvent) {
	e.k.update(func(s *CursorState) { s.X, s.Y = int(ev.X), int(ev.Y) })
}

// HandleImageCopyCaptureCursorSessionV1Hotspot implements the hotspot
// handler.
func (e cursorEvents) HandleImageCopyCaptureCursorSessionV1Hotspot(ev wlr.ImageCopyCaptureCursorSessionV1HotspotEvent) {
	e.k.update(func(s *CursorState) { s.HotspotX, s.HotspotY = int(ev.X), int(ev.Y) })
}
