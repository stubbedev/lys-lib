package capture

import (
	"fmt"
	"image"

	"github.com/stubbedev/gelm/third_party/neurlang-wayland/wl"
	"github.com/stubbedev/gelm/wlr"
)

// screencopyYInvert is the zwlr_screencopy_frame_v1 flags bit that
// marks bottom-up rows (hyprland's toplevel-export shares the enum).
const screencopyYInvert = 1

// Options tunes one capture.
type Options struct {
	// Cursor composites the pointer into the frame.
	Cursor bool
	// Dst is reused for the frame's pixels when it is large enough,
	// sparing the continuous-capture paths an allocation per frame.
	Dst []byte
}

// shmParams is one advertised wl_shm buffer layout.
type shmParams struct {
	width, height, stride int
	format                Format
}

// copyState accumulates the events of one wlr-screencopy or hyprland
// toplevel-export frame; the two protocols share the event shapes.
type copyState struct {
	shm        *shmParams
	dmabuf     *DmabufFormat
	bufferDone bool
	flags      uint32
	ready      bool
	failed     bool
	damage     []Rect
}

func (s *copyState) buffer(format, width, height, stride uint32) {
	s.shm = &shmParams{width: int(width), height: int(height), stride: int(stride), format: Format(format)}
}

func (s *copyState) linuxDmabuf(fourcc, width, height uint32) {
	s.dmabuf = &DmabufFormat{Fourcc: fourcc, Width: int(width), Height: int(height)}
}

func (s *copyState) addDamage(x, y, w, h uint32) {
	s.damage = append(s.damage, Rect{X: int(x), Y: int(y), Width: int(w), Height: int(h)})
}

// finished reports a terminal state: the copy landed or failed.
func (s *copyState) finished() bool { return s.ready || s.failed }

// scEvents adapts copyState to the zwlr_screencopy_frame_v1 handlers.
type scEvents struct{ *copyState }

func (e scEvents) HandleZwlrScreencopyFrameV1Buffer(ev wlr.ZwlrScreencopyFrameV1BufferEvent) {
	e.buffer(ev.Format, ev.Width, ev.Height, ev.Stride)
}

func (e scEvents) HandleZwlrScreencopyFrameV1Flags(ev wlr.ZwlrScreencopyFrameV1FlagsEvent) {
	e.flags = ev.Flags
}

func (e scEvents) HandleZwlrScreencopyFrameV1Ready(wlr.ZwlrScreencopyFrameV1ReadyEvent) {
	e.ready = true
}

func (e scEvents) HandleZwlrScreencopyFrameV1Failed(wlr.ZwlrScreencopyFrameV1FailedEvent) {
	e.failed = true
}

func (e scEvents) HandleZwlrScreencopyFrameV1Damage(ev wlr.ZwlrScreencopyFrameV1DamageEvent) {
	e.addDamage(ev.X, ev.Y, ev.Width, ev.Height)
}

func (e scEvents) HandleZwlrScreencopyFrameV1LinuxDmabuf(ev wlr.ZwlrScreencopyFrameV1LinuxDmabufEvent) {
	e.linuxDmabuf(ev.Format, ev.Width, ev.Height)
}

func (e scEvents) HandleZwlrScreencopyFrameV1BufferDone(wlr.ZwlrScreencopyFrameV1BufferDoneEvent) {
	e.bufferDone = true
}

// listen wires every frame event into the state.
func (e scEvents) listen(f *wlr.ZwlrScreencopyFrameV1) {
	f.AddBufferHandler(e)
	f.AddFlagsHandler(e)
	f.AddReadyHandler(e)
	f.AddFailedHandler(e)
	f.AddDamageHandler(e)
	f.AddLinuxDmabufHandler(e)
	f.AddBufferDoneHandler(e)
}

// CaptureOutput copies one frame of a whole output through
// wlr-screencopy into shared memory.
func (c *Client) CaptureOutput(o Output, opts Options) (*Frame, error) {
	return c.screencopy1(o, nil, opts)
}

// CaptureOutputRegion copies a region of an output, given in the
// output's logical coordinates, through wlr-screencopy.
func (c *Client) CaptureOutputRegion(o Output, region image.Rectangle, opts Options) (*Frame, error) {
	if region.Empty() {
		return nil, fmt.Errorf("capture: empty region %v", region)
	}
	return c.screencopy1(o, &region, opts)
}

// screencopy1 runs one SHM screencopy: request the frame, wait until
// the compositor finished advertising buffer layouts, copy into the
// (reused) shm slot, and wait for ready.
func (c *Client) screencopy1(o Output, region *image.Rectangle, opts Options) (*Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, frame, err := c.startScreencopy(o, region, opts.Cursor)
	if err != nil {
		return nil, err
	}
	defer func() { _ = frame.Destroy() }()
	if err := c.awaitAdvertised(st); err != nil {
		return nil, err
	}
	if st.shm == nil {
		return nil, errNoBuffer
	}
	buf, err := c.reuseSlot(*st.shm)
	if err != nil {
		return nil, err
	}
	if err := c.copyInto(frame, st, buf.wl); err != nil {
		return nil, err
	}
	out := buf.frame(opts.Dst)
	out.YInvert = st.flags&screencopyYInvert != 0
	out.Damage = st.damage
	return out, nil
}

// startScreencopy requests one screencopy frame and wires its events.
func (c *Client) startScreencopy(o Output, region *image.Rectangle, cursor bool) (*copyState, *wlr.ZwlrScreencopyFrameV1, error) {
	if err := c.usable(); err != nil {
		return nil, nil, err
	}
	if !c.HasScreencopy() {
		return nil, nil, fmt.Errorf("%w: %s", ErrUnsupported, ifaceScreencopy)
	}
	if o.wl == nil {
		return nil, nil, fmt.Errorf("capture: output %q is not from this client", o.Name)
	}
	overlay := int32(0)
	if cursor {
		overlay = 1
	}
	var frame *wlr.ZwlrScreencopyFrameV1
	var err error
	if region != nil {
		frame, err = c.screencopy.CaptureOutputRegion(overlay, o.wl,
			int32(region.Min.X), int32(region.Min.Y), int32(region.Dx()), int32(region.Dy()))
	} else {
		frame, err = c.screencopy.CaptureOutput(overlay, o.wl)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("capture: screencopy request: %w", err)
	}
	st := &copyState{}
	scEvents{st}.listen(frame)
	return st, frame, nil
}

// awaitAdvertised dispatches until the compositor sent every buffer
// layout it offers (buffer_done on screencopy v3+, the shm buffer event
// before that). Deciding earlier races the buffer / linux_dmabuf /
// buffer_done sequence, whose first two arrive in unspecified order.
func (c *Client) awaitAdvertised(st *copyState) error {
	v3 := c.scVersion >= 3
	err := c.dispatchUntil(func() bool {
		if st.failed {
			return true
		}
		if v3 {
			return st.bufferDone
		}
		return st.shm != nil
	})
	if err != nil {
		return err
	}
	if st.failed {
		return ErrFailed
	}
	return nil
}

// copyInto sends the copy into target and waits for the verdict.
// copy_with_damage (v2+) is what makes the compositor report damage.
func (c *Client) copyInto(frame *wlr.ZwlrScreencopyFrameV1, st *copyState, target *wl.Buffer) error {
	return c.copyIntoAs(frame, st, target, c.scVersion >= 2)
}

// copyIntoAs is copyInto choosing the request: copy_with_damage waits
// for the output to change, a plain copy takes the next frame.
func (c *Client) copyIntoAs(frame *wlr.ZwlrScreencopyFrameV1, st *copyState, target *wl.Buffer, withDamage bool) error {
	var err error
	if withDamage {
		err = frame.CopyWithDamage(target)
	} else {
		err = frame.Copy(target)
	}
	if err != nil {
		return fmt.Errorf("capture: screencopy copy: %w", err)
	}
	if err := c.dispatchUntil(st.finished); err != nil {
		return err
	}
	if st.failed {
		return ErrFailed
	}
	return nil
}

// reuseSlot returns the client's shm slot, reallocated only when the
// advertised layout changed: steady-state captures of one output then
// reuse one memfd, pool, and wl_buffer. Reuse is sound because every
// capture is synchronous - the previous copy finished before the next
// one targets the slot.
func (c *Client) reuseSlot(p shmParams) (*shmBuffer, error) {
	if c.slot != nil && c.slot.matches(p.width, p.height, p.stride, p.format) {
		return c.slot, nil
	}
	if c.slot != nil {
		c.slot.release()
		c.slot = nil
	}
	buf, err := newShmBuffer(c.shm, p.width, p.height, p.stride, p.format)
	if err != nil {
		return nil, err
	}
	c.slot = buf
	return buf, nil
}
