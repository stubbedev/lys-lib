package capture

import (
	"errors"
	"fmt"
	"slices"

	"github.com/neurlang/wayland/wl"

	"github.com/stubbedev/lys-ui/dmabuf"
	"github.com/stubbedev/lys-ui/internal/dmabufwl"
	"github.com/stubbedev/lys-ui/wlr"
)

// DmabufFormat is the dmabuf layout the compositor offers for an
// output's screencopy frames: a DRM fourcc and the buffer size.
type DmabufFormat struct {
	Fourcc        uint32
	Width, Height int
	// Modifiers are the explicit modifiers the compositor imports
	// Fourcc in, in its preference order: allocate in one of them.
	Modifiers []uint64
	// Device is the dev_t of the DRM device the compositor wants the
	// buffer allocated on (gpu.OpenNode).
	Device uint64
}

// DmabufBuffer is an imported dmabuf, ready to receive screencopy
// frames.
type DmabufBuffer struct {
	wl     *wl.Buffer
	client *Client
	// Attrs is the imported description.
	Attrs dmabuf.Buffer
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
// output's screencopy frames (wlr-screencopy v3's linux_dmabuf event),
// with the modifiers and device its linux-dmabuf v4 default feedback
// names, probing once per output with a frame that is never copied and
// caching the answer (the format is stable per output). ErrUnsupported
// means the compositor offers only shm here, lacks linux-dmabuf v4
// feedback or screencopy v3, or imports the format in no explicit
// modifier - stay on the shm path.
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
		fb, err := c.dmabufFeedback()
		if err != nil {
			return DmabufFormat{}, err
		}
		p.format.Device = fb.Device
		p.format.Modifiers = slices.Clone(fb.Modifiers[p.format.Fourcc])
		if len(p.format.Modifiers) == 0 {
			return DmabufFormat{}, fmt.Errorf("%w: the compositor imports DRM format %#08x in no explicit modifier", ErrUnsupported, p.format.Fourcc)
		}
	}
	c.dmabufProbe[o.wl] = p
	if !p.offered {
		return DmabufFormat{}, fmt.Errorf("%w: no dmabuf format for output %q", ErrUnsupported, o.Name)
	}
	return p.format, nil
}

// dmabufFeedback is the compositor's linux-dmabuf default feedback,
// read once: a compositor whose linux-dmabuf predates feedback (v4)
// names no modifiers or device, which is ErrUnsupported, not a guess.
func (c *Client) dmabufFeedback() (dmabufwl.Feedback, error) {
	if c.feedback != nil {
		return *c.feedback, nil
	}
	fb, err := dmabufwl.DefaultFeedback(c.dmabuf, c.dmabufVersion, c.roundtrip)
	if err != nil {
		return dmabufwl.Feedback{}, fmt.Errorf("%w: %w", ErrUnsupported, err)
	}
	c.feedback = &fb
	return fb, nil
}

// paramsState records a zwp_linux_buffer_params_v1 import's verdict:
// the created buffer or the failure.
type paramsState struct {
	buf    *wlr.Buffer
	failed bool
}

// HandleZwpBufferParamsV1Created takes the imported buffer.
func (p *paramsState) HandleZwpBufferParamsV1Created(ev wlr.ZwpBufferParamsV1CreatedEvent) {
	p.buf = ev.Buffer
}

// HandleZwpBufferParamsV1Failed implements the failed handler.
func (p *paramsState) HandleZwpBufferParamsV1Failed(wlr.ZwpBufferParamsV1FailedEvent) {
	p.failed = true
}

// ImportDmabuf imports a caller-allocated dmabuf as a wl_buffer and
// waits one roundtrip for the compositor's verdict. The import uses
// the asynchronous create, whose failure is an event: a rejected
// import surfaces here as ErrDmabufRejected with the connection intact,
// where create_immed's failure is a fatal protocol error.
func (c *Client) ImportDmabuf(d dmabuf.Buffer) (*DmabufBuffer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.usable(); err != nil {
		return nil, err
	}
	if c.dmabuf == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, ifaceDmabuf)
	}
	params, err := dmabufwl.Params(c.dmabuf, d)
	if err != nil {
		return nil, fmt.Errorf("capture: %w", err)
	}
	defer func() { _ = params.Destroy() }()
	st := &paramsState{}
	params.AddCreatedHandler(st)
	params.AddFailedHandler(st)
	if err := params.Create(int32(d.Width), int32(d.Height), d.Format, 0); err != nil {
		return nil, fmt.Errorf("capture: dmabuf create: %w", err)
	}
	if err := c.roundtrip(); err != nil {
		return nil, err
	}
	switch {
	case st.failed:
		return nil, ErrDmabufRejected
	case st.buf == nil:
		return nil, fmt.Errorf("capture: the compositor answered a %dx%d dmabuf import with neither created nor failed", d.Width, d.Height)
	}
	return &DmabufBuffer{wl: st.buf, client: c, Attrs: d}, nil
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
