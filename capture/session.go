package capture

import (
	"errors"
	"fmt"
	"slices"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/dmabufwl"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/wayland/wl"
	"github.com/stubbedev/lys-lib/wlr"
)

// Source is what an ext-image-copy-capture session captures: an Output
// or a Toplevel, as the Client that opens the session lists them.
type Source interface {
	// imageSource creates a capture source on c, and the release of
	// the claim the session opened on it holds until it is destroyed.
	imageSource(c *Client) (*wlr.ImageCaptureSourceV1, func(), error)
}

// ErrSessionStopped reports that the compositor ended a capture
// session: its source went away (the output was unplugged, the window
// closed). It is an ErrFailed.
var ErrSessionStopped = fmt.Errorf("%w: the capture session stopped", ErrFailed)

// ErrReconfigured reports that a capture session's buffer constraints
// changed (the output switched mode, the window resized): the buffers
// allocated against the old ones no longer fit.
var ErrReconfigured = errors.New("capture: the source's buffer constraints changed")

// dmabufOffer is one dmabuf_format event: a DRM format and the
// explicit modifiers the session takes it in.
type dmabufOffer struct {
	format    fourcc.Format
	modifiers []fourcc.Modifier
}

// constraints is one batch of a session's buffer constraints, the
// events up to a done.
type constraints struct {
	width, height int
	// shm are the advertised shm formats in arrival order.
	shm []Format
	// dmabuf are the advertised dmabuf formats in arrival order, on
	// device when hasDevice.
	dmabuf    []dmabufOffer
	device    uint64
	hasDevice bool
}

// sessionState tracks an ext_image_copy_capture_session_v1's buffer
// constraints: each batch gathers in pending and replaces current at
// its done.
type sessionState struct {
	pending, current constraints
	// generation counts the batches done; 0 before the first.
	generation uint64
	// err is the first malformed constraint event.
	err     error
	stopped bool
}

// HandleImageCopyCaptureSessionV1BufferSize implements the buffer_size
// handler.
func (s *sessionState) HandleImageCopyCaptureSessionV1BufferSize(ev wlr.ImageCopyCaptureSessionV1BufferSizeEvent) {
	s.pending.width, s.pending.height = int(ev.Width), int(ev.Height)
}

// HandleImageCopyCaptureSessionV1ShmFormat implements the shm_format
// handler.
func (s *sessionState) HandleImageCopyCaptureSessionV1ShmFormat(ev wlr.ImageCopyCaptureSessionV1ShmFormatEvent) {
	s.pending.shm = append(s.pending.shm, Format(ev.Format))
}

// HandleImageCopyCaptureSessionV1DmabufDevice implements the
// dmabuf_device handler.
func (s *sessionState) HandleImageCopyCaptureSessionV1DmabufDevice(ev wlr.ImageCopyCaptureSessionV1DmabufDeviceEvent) {
	dev, err := dmabufwl.Device(ev.Device)
	if err != nil {
		s.fail(fmt.Errorf("capture: the session's dmabuf_device: %w", err))
		return
	}
	s.pending.device, s.pending.hasDevice = dev, true
}

// HandleImageCopyCaptureSessionV1DmabufFormat implements the
// dmabuf_format handler.
func (s *sessionState) HandleImageCopyCaptureSessionV1DmabufFormat(ev wlr.ImageCopyCaptureSessionV1DmabufFormatEvent) {
	format := fourcc.Format(ev.Format)
	mods, err := dmabufwl.Modifiers(ev.Modifiers)
	if err != nil {
		s.fail(fmt.Errorf("capture: the session's dmabuf_format %s: %w", format, err))
		return
	}
	s.pending.dmabuf = append(s.pending.dmabuf, dmabufOffer{format: format, modifiers: mods})
}

// HandleImageCopyCaptureSessionV1Done implements the done handler: the
// pending batch becomes the session's constraints.
func (s *sessionState) HandleImageCopyCaptureSessionV1Done(wlr.ImageCopyCaptureSessionV1DoneEvent) {
	s.current, s.pending = s.pending, constraints{}
	s.generation++
}

// HandleImageCopyCaptureSessionV1Stopped implements the stopped
// handler: the source went away (the window closed, the output was
// unplugged).
func (s *sessionState) HandleImageCopyCaptureSessionV1Stopped(wlr.ImageCopyCaptureSessionV1StoppedEvent) {
	s.stopped = true
}

func (s *sessionState) fail(err error) {
	if s.err == nil {
		s.err = err
	}
}

func (s *sessionState) listen(session *wlr.ImageCopyCaptureSessionV1) {
	session.AddBufferSizeHandler(s)
	session.AddShmFormatHandler(s)
	session.AddDmabufDeviceHandler(s)
	session.AddDmabufFormatHandler(s)
	session.AddDoneHandler(s)
	session.AddStoppedHandler(s)
}

// layout picks the shm format to allocate - the first advertised one
// Frame.Image can convert - and the tightly packed stride for it.
// Sessions leave the stride to the client, so a format of unknown
// pixel size cannot be allocated at all.
func (k constraints) layout() (Format, int, error) {
	for _, f := range k.shm {
		if f.Supported() {
			return f, k.width * f.BytesPerPixel(), nil
		}
	}
	return 0, 0, fmt.Errorf("%w: the session offers only %v", ErrFormat, k.shm)
}

// dmabufFormats are the dmabuf layouts the constraints accept, in the
// compositor's order, each format with at least one explicit modifier.
// ErrUnsupported means the session takes no dmabuf: no format, no
// device to allocate on, or only implicit modifiers.
func (k constraints) dmabufFormats() ([]DmabufFormat, error) {
	if len(k.dmabuf) == 0 {
		return nil, fmt.Errorf("%w: the capture session offers no dmabuf format", ErrUnsupported)
	}
	if !k.hasDevice {
		return nil, fmt.Errorf("%w: the capture session names no dmabuf device", ErrUnsupported)
	}
	out := make([]DmabufFormat, 0, len(k.dmabuf))
	for _, o := range k.dmabuf {
		if len(o.modifiers) == 0 {
			continue
		}
		out = append(out, DmabufFormat{
			Fourcc: o.format, Width: k.width, Height: k.height,
			Modifiers: slices.Clone(o.modifiers), Device: k.device,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: the capture session takes its dmabuf formats in no explicit modifier", ErrUnsupported)
	}
	return out, nil
}

// fits reports whether the constraints take the dmabuf: its size, and
// its format in its modifier.
func (k constraints) fits(d dmabuf.Buffer) bool {
	if d.Width != k.width || d.Height != k.height {
		return false
	}
	return slices.ContainsFunc(k.dmabuf, func(o dmabufOffer) bool {
		return o.format == d.Format && slices.Contains(o.modifiers, d.Modifier)
	})
}

// failureReason is why the compositor failed a frame.
type failureReason uint32

const (
	failureBufferConstraints failureReason = wlr.ImageCopyCaptureFrameV1FailureReasonBufferConstraints
	failureStopped           failureReason = wlr.ImageCopyCaptureFrameV1FailureReasonStopped
)

// FrameInfo is what the compositor reports about one ext-image-copy
// frame it copied.
type FrameInfo struct {
	// Damage is what changed since the session's previous frame;
	// empty means the whole frame.
	Damage []Rect
	// Transform is the buffer transform of this frame.
	Transform Transform
	// PTSNanos is the compositor's presentation time in nanoseconds
	// (CLOCK_MONOTONIC), 0 when it reported none.
	PTSNanos uint64
}

// frameState accumulates one ext_image_copy_capture_frame_v1's
// events.
type frameState struct {
	info          FrameInfo
	ready, failed bool
	reason        failureReason
}

// HandleImageCopyCaptureFrameV1Transform implements the transform
// handler.
func (f *frameState) HandleImageCopyCaptureFrameV1Transform(ev wlr.ImageCopyCaptureFrameV1TransformEvent) {
	f.info.Transform = Transform(ev.Transform)
}

// HandleImageCopyCaptureFrameV1Damage implements the damage handler;
// empty rects are dropped.
func (f *frameState) HandleImageCopyCaptureFrameV1Damage(ev wlr.ImageCopyCaptureFrameV1DamageEvent) {
	x, y := max(ev.X, 0), max(ev.Y, 0)
	w, h := max(ev.Width, 0), max(ev.Height, 0)
	if w > 0 && h > 0 {
		f.info.Damage = append(f.info.Damage, Rect{X: int(x), Y: int(y), Width: int(w), Height: int(h)})
	}
}

// HandleImageCopyCaptureFrameV1PresentationTime implements the
// presentation_time handler.
func (f *frameState) HandleImageCopyCaptureFrameV1PresentationTime(ev wlr.ImageCopyCaptureFrameV1PresentationTimeEvent) {
	secs := uint64(ev.TvSecHi)<<32 | uint64(ev.TvSecLo)
	f.info.PTSNanos = secs*1_000_000_000 + uint64(ev.TvNsec)
}

// HandleImageCopyCaptureFrameV1Ready implements the ready handler.
func (f *frameState) HandleImageCopyCaptureFrameV1Ready(wlr.ImageCopyCaptureFrameV1ReadyEvent) {
	f.ready = true
}

// HandleImageCopyCaptureFrameV1Failed implements the failed handler.
func (f *frameState) HandleImageCopyCaptureFrameV1Failed(ev wlr.ImageCopyCaptureFrameV1FailedEvent) {
	f.failed, f.reason = true, failureReason(ev.Reason)
}

func (f *frameState) finished() bool { return f.ready || f.failed }

func (f *frameState) listen(frame *wlr.ImageCopyCaptureFrameV1) {
	frame.AddTransformHandler(f)
	frame.AddDamageHandler(f)
	frame.AddPresentationTimeHandler(f)
	frame.AddReadyHandler(f)
	frame.AddFailedHandler(f)
}

// sessionOptions maps the cursor choice to the create_session options.
func sessionOptions(cursor bool) uint32 {
	if cursor {
		return wlr.ImageCopyCaptureManagerV1OptionsPaintCursors
	}
	return 0
}

// copySession is an open ext-image-copy-capture session, the capture
// source it was opened on, and the release of its claim on that
// source.
type copySession struct {
	source  *wlr.ImageCaptureSourceV1
	release func()
	session *wlr.ImageCopyCaptureSessionV1
	state   *sessionState
}

// openSession opens a session on src and dispatches until its first
// constraints are done. ErrSessionStopped means the source went away
// before they were.
func (c *Client) openSession(src Source, cursor bool) (*copySession, error) {
	if err := c.usable(); err != nil {
		return nil, err
	}
	source, release, err := src.imageSource(c)
	if err != nil {
		return nil, err
	}
	session, err := c.copyCapture.CreateSession(source, sessionOptions(cursor))
	if err != nil {
		release()
		return nil, errors.Join(fmt.Errorf("capture: create a capture session: %w", err), source.Destroy())
	}
	cs := &copySession{source: source, release: release, session: session, state: &sessionState{}}
	cs.state.listen(session)
	if err := c.dispatchUntil(func() bool { return cs.state.stopped || cs.state.generation > 0 }); err != nil {
		return nil, errors.Join(err, cs.destroy())
	}
	if err := cs.usable(); err != nil {
		return nil, errors.Join(err, cs.destroy())
	}
	if k := cs.state.current; k.width <= 0 || k.height <= 0 {
		return nil, errors.Join(fmt.Errorf("capture: the capture session advertised a %dx%d buffer", k.width, k.height), cs.destroy())
	}
	return cs, nil
}

// usable fails once the session stopped or sent a malformed
// constraint.
func (cs *copySession) usable() error {
	if cs.state.stopped {
		return ErrSessionStopped
	}
	return cs.state.err
}

// destroy releases the session, its source and its claim on it.
func (cs *copySession) destroy() error {
	cs.release()
	err := errors.Join(cs.session.Destroy(), cs.source.Destroy())
	if err != nil {
		return fmt.Errorf("capture: destroy the capture session: %w", err)
	}
	return nil
}

// captureFrame runs one full-buffer frame into buf: create, attach,
// damage the whole buffer (the client tracks no damage of its own),
// capture, and dispatch until the verdict, the session's stop, or done
// says to give up. A frame failed because the session stopped marks it
// stopped, ahead of its stopped event.
func (cs *copySession) captureFrame(dispatch func(func() bool) error, buf *wl.Buffer, width, height int32) (*frameState, error) {
	frame, err := cs.session.CreateFrame()
	if err != nil {
		return nil, fmt.Errorf("capture: create a capture frame: %w", err)
	}
	defer func() { _ = frame.Destroy() }()
	fs := &frameState{}
	fs.listen(frame)
	if err := frame.AttachBuffer(buf); err != nil {
		return nil, fmt.Errorf("capture: attach the %dx%d buffer: %w", width, height, err)
	}
	if err := frame.DamageBuffer(0, 0, width, height); err != nil {
		return nil, fmt.Errorf("capture: damage the %dx%d buffer: %w", width, height, err)
	}
	if err := frame.Capture(); err != nil {
		return nil, fmt.Errorf("capture: capture a frame: %w", err)
	}
	if err := dispatch(func() bool { return fs.finished() || cs.state.stopped }); err != nil {
		return nil, err
	}
	if fs.reason == failureStopped {
		cs.state.stopped = true
	}
	return fs, nil
}

// verdict is a finished frame's outcome: nil when it is ready.
func (cs *copySession) verdict(fs *frameState) error {
	switch {
	case fs.ready:
		return nil
	case cs.state.stopped, fs.reason == failureStopped:
		return ErrSessionStopped
	case fs.reason == failureBufferConstraints:
		return fmt.Errorf("%w: the buffer does not meet the session's constraints", ErrFailed)
	}
	return ErrFailed
}
