package capture

import (
	"errors"
	"fmt"

	"github.com/neurlang/wayland/wl"

	"github.com/stubbedev/gelm/wlr"
)

// DmabufFormat is the dmabuf layout the compositor offers for an
// output's screencopy frames: a DRM fourcc and the buffer size.
type DmabufFormat struct {
	Fourcc        uint32
	Width, Height int
}

// DmabufPlane is one plane of a dmabuf: its file descriptor and the
// plane's byte offset and stride within it.
type DmabufPlane struct {
	Fd     uintptr
	Offset uint32
	Stride uint32
}

// Dmabuf describes a GPU buffer to import as a screencopy target. The
// caller allocates it (gbm, a DRM dumb buffer, udmabuf) and keeps the
// plane fds open for as long as the DmabufBuffer lives; the import
// dups nothing.
type Dmabuf struct {
	Width, Height int
	Fourcc        uint32
	// Modifier is the DRM format modifier (DRM_FORMAT_MOD_LINEAR is 0,
	// DRM_FORMAT_MOD_INVALID means implicit).
	Modifier uint64
	Planes   []DmabufPlane
}

// DmabufBuffer is an imported dmabuf, ready to receive screencopy
// frames.
type DmabufBuffer struct {
	wl     *wl.Buffer
	client *Client
	// Attrs is the imported description.
	Attrs Dmabuf
}

// ErrDmabufRejected reports that the compositor refused a dmabuf
// import (the params failed event): the format, modifier, or device
// does not suit it.
var ErrDmabufRejected = errors.New("capture: the compositor rejected the dmabuf import")

// dmabufProbe caches one output's probe result: the offered format, or
// the fact that none is offered.
type dmabufProbe struct {
	format  DmabufFormat
	offered bool
}

// DmabufFormat reports the dmabuf layout the compositor offers for the
// output's screencopy frames, probing once per output with a frame
// that is never copied and caching the answer (the format is stable
// per output). ErrUnsupported means the compositor offers only shm
// here, or lacks linux-dmabuf or screencopy v3 entirely - stay on the
// shm path.
func (c *Client) DmabufFormat(o Output, cursor bool) (DmabufFormat, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dmabuf == nil {
		return DmabufFormat{}, fmt.Errorf("%w: %s", ErrUnsupported, ifaceDmabuf)
	}
	if c.screencopy != nil && c.scVersion < 3 {
		return DmabufFormat{}, fmt.Errorf("%w: %s v3 (linux_dmabuf event)", ErrUnsupported, ifaceScreencopy)
	}
	if p, ok := c.dmabufProbe[o.wl]; ok {
		if !p.offered {
			return DmabufFormat{}, fmt.Errorf("%w: no dmabuf format for output %q", ErrUnsupported, o.Name)
		}
		return p.format, nil
	}
	st, frame, err := c.startScreencopy(o, nil, cursor)
	if err != nil {
		return DmabufFormat{}, err
	}
	err = c.awaitAdvertised(st)
	_ = frame.Destroy()
	if err != nil {
		// A failed probe is transient: do not cache it.
		return DmabufFormat{}, err
	}
	p := dmabufProbe{offered: st.dmabuf != nil}
	if p.offered {
		p.format = *st.dmabuf
	}
	c.dmabufProbe[o.wl] = p
	if !p.offered {
		return DmabufFormat{}, fmt.Errorf("%w: no dmabuf format for output %q", ErrUnsupported, o.Name)
	}
	return p.format, nil
}

// paramsState records a zwp_linux_buffer_params_v1 import's verdict;
// with create_immed success is silent, so only failed matters.
type paramsState struct{ failed bool }

// HandleZwpBufferParamsV1Created implements the created handler; it
// only fires for the non-immediate create, which the client never
// sends.
func (p *paramsState) HandleZwpBufferParamsV1Created(wlr.ZwpBufferParamsV1CreatedEvent) {}

// HandleZwpBufferParamsV1Failed implements the failed handler.
func (p *paramsState) HandleZwpBufferParamsV1Failed(wlr.ZwpBufferParamsV1FailedEvent) {
	p.failed = true
}

// ImportDmabuf imports a caller-allocated dmabuf as a wl_buffer
// (create_immed) and waits one roundtrip for the compositor's verdict,
// so a rejected import surfaces here as ErrDmabufRejected rather than
// as a failed copy later.
func (c *Client) ImportDmabuf(d Dmabuf) (*DmabufBuffer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.usable(); err != nil {
		return nil, err
	}
	if c.dmabuf == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, ifaceDmabuf)
	}
	if d.Width <= 0 || d.Height <= 0 || len(d.Planes) == 0 {
		return nil, fmt.Errorf("capture: bad dmabuf %dx%d with %d planes", d.Width, d.Height, len(d.Planes))
	}
	params, err := c.dmabuf.CreateParams()
	if err != nil {
		return nil, fmt.Errorf("capture: dmabuf params: %w", err)
	}
	st := &paramsState{}
	params.AddCreatedHandler(st)
	params.AddFailedHandler(st)
	hi, lo := uint32(d.Modifier>>32), uint32(d.Modifier)
	for i, p := range d.Planes {
		if err := params.Add(p.Fd, uint32(i), p.Offset, p.Stride, hi, lo); err != nil {
			_ = params.Destroy()
			return nil, fmt.Errorf("capture: dmabuf plane %d: %w", i, err)
		}
	}
	buf, err := params.CreateImmed(int32(d.Width), int32(d.Height), d.Fourcc, 0)
	if err != nil {
		_ = params.Destroy()
		return nil, fmt.Errorf("capture: dmabuf create_immed: %w", err)
	}
	err = c.roundtrip()
	_ = params.Destroy()
	if err != nil {
		return nil, err
	}
	if st.failed {
		_ = buf.Destroy()
		return nil, ErrDmabufRejected
	}
	return &DmabufBuffer{wl: buf, client: c, Attrs: d}, nil
}

// Destroy releases the compositor-side buffer. The caller's plane fds
// are untouched.
func (b *DmabufBuffer) Destroy() {
	b.client.mu.Lock()
	defer b.client.mu.Unlock()
	if b.wl != nil && !b.client.closed {
		_ = b.wl.Destroy()
	}
	b.wl = nil
}

// CopyOutputDmabuf copies the output's next frame into an imported
// dmabuf without waiting for it to change (a plain screencopy copy):
// what a stream filling at its own rate, or a probe of an idle output,
// needs. CaptureOutputDmabuf waits for damage instead, and reports it.
func (c *Client) CopyOutputDmabuf(o Output, cursor bool, buf *DmabufBuffer) error {
	_, err := c.captureDmabuf(o, cursor, buf, false)
	return err
}

// CaptureOutputDmabuf copies one screencopy frame of the output
// straight into an imported dmabuf (zero copy: no shm readback). It
// returns the frame's damage. Fencing is the caller's: reuse a buffer
// only after its consumer finished reading it.
func (c *Client) CaptureOutputDmabuf(o Output, cursor bool, buf *DmabufBuffer) ([]Rect, error) {
	return c.captureDmabuf(o, cursor, buf, c.scVersion >= 2)
}

func (c *Client) captureDmabuf(o Output, cursor bool, buf *DmabufBuffer, withDamage bool) ([]Rect, error) {
	if buf == nil || buf.client != c {
		return nil, errors.New("capture: dmabuf buffer is not from this client")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if buf.wl == nil {
		return nil, errors.New("capture: dmabuf buffer was destroyed")
	}
	st, frame, err := c.startScreencopy(o, nil, cursor)
	if err != nil {
		return nil, err
	}
	defer func() { _ = frame.Destroy() }()
	if err := c.awaitAdvertised(st); err != nil {
		return nil, err
	}
	if err := c.copyIntoAs(frame, st, buf.wl, withDamage); err != nil {
		return nil, err
	}
	return st.damage, nil
}
