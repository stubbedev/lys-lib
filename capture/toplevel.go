package capture

import (
	"errors"
	"fmt"
	"slices"

	"github.com/stubbedev/gelm/wlr"
)

// Toplevel is one window as ext-foreign-toplevel-list reports it. The
// list carries no focus or activation state, so the caller decides
// which toplevel to capture (typically by matching the compositor's
// active window on AppID and Title).
type Toplevel struct {
	// Title and AppID are the window's latest title and app id.
	Title, AppID string
	// Identifier is the compositor's stable identifier for the
	// window, unique for its lifetime.
	Identifier string

	handle *wlr.ForeignToplevelHandleV1
}

// toplevelState tracks one handle as its metadata arrives.
type toplevelState struct {
	tl     Toplevel
	client *Client
}

// HandleForeignToplevelListV1Toplevel implements the list's toplevel
// handler: start tracking a new handle.
func (c *Client) HandleForeignToplevelListV1Toplevel(ev wlr.ForeignToplevelListV1ToplevelEvent) {
	if ev.Toplevel == nil {
		return
	}
	st := &toplevelState{tl: Toplevel{handle: ev.Toplevel}, client: c}
	ev.Toplevel.AddTitleHandler(st)
	ev.Toplevel.AddAppIdHandler(st)
	ev.Toplevel.AddIdentifierHandler(st)
	ev.Toplevel.AddClosedHandler(st)
	c.toplevels = append(c.toplevels, st)
}

// HandleForeignToplevelHandleV1Title implements the title handler.
func (s *toplevelState) HandleForeignToplevelHandleV1Title(ev wlr.ForeignToplevelHandleV1TitleEvent) {
	s.tl.Title = ev.Title
}

// HandleForeignToplevelHandleV1AppId implements the app_id handler.
func (s *toplevelState) HandleForeignToplevelHandleV1AppId(ev wlr.ForeignToplevelHandleV1AppIdEvent) {
	s.tl.AppID = ev.AppId
}

// HandleForeignToplevelHandleV1Identifier implements the identifier
// handler.
func (s *toplevelState) HandleForeignToplevelHandleV1Identifier(ev wlr.ForeignToplevelHandleV1IdentifierEvent) {
	s.tl.Identifier = ev.Identifier
}

// HandleForeignToplevelHandleV1Closed implements the closed handler:
// the window is gone, so it leaves the list and its handle is freed.
func (s *toplevelState) HandleForeignToplevelHandleV1Closed(wlr.ForeignToplevelHandleV1ClosedEvent) {
	c := s.client
	if i := slices.Index(c.toplevels, s); i >= 0 {
		c.toplevels = slices.Delete(c.toplevels, i, i+1)
	}
	_ = s.tl.handle.Destroy()
}

// Toplevels lists the windows the compositor currently advertises, in
// arrival order, after a roundtrip that brings the list up to date.
func (c *Client) Toplevels() ([]Toplevel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.usable(); err != nil {
		return nil, err
	}
	if c.toplevelList == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, ifaceToplevelList)
	}
	if err := c.roundtrip(); err != nil {
		return nil, err
	}
	out := make([]Toplevel, len(c.toplevels))
	for i, t := range c.toplevels {
		out[i] = t.tl
	}
	return out, nil
}

// CaptureToplevel copies one frame of a window through
// ext-image-copy-capture: a capture source from the toplevel handle, a
// session whose constraints size the shm buffer, and one frame.
func (c *Client) CaptureToplevel(t Toplevel, opts Options) (*Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.usable(); err != nil {
		return nil, err
	}
	if !c.HasToplevelCapture() {
		return nil, fmt.Errorf("%w: %s + %s", ErrUnsupported, ifaceCopyCapture, ifaceToplevelSrc)
	}
	if t.handle == nil {
		return nil, errors.New("capture: toplevel is not from this client")
	}
	// A closed window's handle is destroyed; sending a request against
	// it would be a protocol error that kills the connection.
	if !slices.ContainsFunc(c.toplevels, func(s *toplevelState) bool { return s.tl.handle == t.handle }) {
		return nil, fmt.Errorf("%w: the window %q closed", ErrFailed, t.Title)
	}
	source, err := c.toplevelSrc.CreateSource(t.handle)
	if err != nil {
		return nil, fmt.Errorf("capture: toplevel source: %w", err)
	}
	defer func() { _ = source.Destroy() }()
	return c.captureSourceOnce(source, opts)
}

// captureSourceOnce opens a session on source, negotiates, and copies
// one frame into a fresh shm buffer.
func (c *Client) captureSourceOnce(source *wlr.ImageCaptureSourceV1, opts Options) (*Frame, error) {
	session, err := c.copyCapture.CreateSession(source, sessionOptions(opts.Cursor))
	if err != nil {
		return nil, fmt.Errorf("capture: session: %w", err)
	}
	defer func() { _ = session.Destroy() }()
	ss := &sessionState{}
	ss.listen(session)
	if err := c.dispatchUntil(func() bool { return ss.stopped || ss.negotiated() }); err != nil {
		return nil, err
	}
	if ss.stopped {
		return nil, ErrFailed
	}
	format, stride, err := ss.layout()
	if err != nil {
		return nil, err
	}
	buf, err := newShmBuffer(c.shm, ss.width, ss.height, stride, format)
	if err != nil {
		return nil, err
	}
	defer buf.release()
	fs, err := captureSessionFrame(c.dispatchUntil, session, ss, buf)
	if err != nil {
		return nil, err
	}
	if !fs.ready {
		return nil, ErrFailed
	}
	out := buf.frame(opts.Dst)
	out.Transform = fs.transform
	out.Damage = fs.damage
	return out, nil
}

// CaptureOutputOnce copies one frame of a whole output through
// ext-image-copy-capture - the compositor-neutral alternative to
// CaptureOutput for compositors without wlr-screencopy.
func (c *Client) CaptureOutputOnce(o Output, opts Options) (*Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.usable(); err != nil {
		return nil, err
	}
	if !c.HasOutputCapture() {
		return nil, fmt.Errorf("%w: %s + %s", ErrUnsupported, ifaceCopyCapture, ifaceOutputSource)
	}
	if o.wl == nil {
		return nil, fmt.Errorf("capture: output %q is not from this client", o.Name)
	}
	source, err := c.outputSource.CreateSource(o.wl)
	if err != nil {
		return nil, fmt.Errorf("capture: output source: %w", err)
	}
	defer func() { _ = source.Destroy() }()
	return c.captureSourceOnce(source, opts)
}
