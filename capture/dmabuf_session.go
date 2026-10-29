package capture

import (
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
)

// DmabufSession is a persistent ext-image-copy-capture session on one
// Source (a window, an output) that copies frames into dmabufs the
// caller allocates and imports: the zero-copy path a window share
// streams over. Its constraints hold until the source resizes, which
// Capture reports as ErrReconfigured.
//
// The session runs on its Client and holds the Client while Capture
// waits for the source to change, so a share runs its session on a
// Client of its own. Close may be called from any goroutine and wakes a
// waiting Capture.
type DmabufSession struct {
	client *Client
	cs     *copySession
	// formats are the dmabuf layouts of the constraints generation
	// formatsGen names.
	formats    []DmabufFormat
	formatsGen uint64
	// openGen is the constraints generation the session opened with.
	openGen uint64
	closing atomic.Bool
}

// OpenDmabufSession opens a capture session on src, painting the
// cursor when cursor is set, and waits for its buffer constraints.
// ErrUnsupported means the compositor lacks the session's protocols or
// offers this source no dmabuf layout with an explicit modifier, and
// ErrSessionStopped that the source went away first.
//
// A window takes one session at a time: while the session is open,
// CaptureToplevel and another OpenDmabufSession on the same window and
// Client fail with ErrToplevelBusy, as wlroots compositors (sway) would
// copy the joining session's first frame only once the window's content
// next changes. A session on the window from another Client or process
// cannot be detected here and has that wait.
func (c *Client) OpenDmabufSession(src Source, cursor bool) (*DmabufSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dmabuf == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, ifaceDmabuf)
	}
	cs, err := c.openSession(src, cursor)
	if err != nil {
		return nil, err
	}
	formats, err := cs.state.current.dmabufFormats()
	if err != nil {
		return nil, errors.Join(err, cs.destroy())
	}
	gen := cs.state.generation
	return &DmabufSession{client: c, cs: cs, formats: formats, formatsGen: gen, openGen: gen}, nil
}

// Formats are the dmabuf layouts the session currently takes, in the
// compositor's preference order, each with the buffer size, the
// explicit modifiers and the device to allocate on. They change when
// Capture reports ErrReconfigured; an error means the new constraints
// take no dmabuf (ErrUnsupported) or the session ended.
func (s *DmabufSession) Formats() ([]DmabufFormat, error) {
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	if err := s.usable(); err != nil {
		return nil, err
	}
	formats, err := s.currentFormats()
	if err != nil {
		return nil, err
	}
	return slices.Clone(formats), nil
}

// currentFormats derives the dmabuf layouts of the latest constraints,
// once per generation.
func (s *DmabufSession) currentFormats() ([]DmabufFormat, error) {
	st := s.cs.state
	if s.formatsGen == st.generation {
		return s.formats, nil
	}
	formats, err := st.current.dmabufFormats()
	if err != nil {
		return nil, err
	}
	s.formats, s.formatsGen = formats, st.generation
	return formats, nil
}

// usable fails once the session or its Client closed, or the session
// ended.
func (s *DmabufSession) usable() error {
	if s.closing.Load() {
		return fmt.Errorf("%w: the dmabuf session is closed", ErrClosed)
	}
	if err := s.client.usable(); err != nil {
		return err
	}
	return s.cs.usable()
}

// Capture copies the source's next frame into buf, an import of a
// dmabuf in one of Formats, and reports the frame. The session's first
// frame is copied at once; every later one waits until the source's
// content changes, and its Damage is what changed since the previous
// one. A buffer that no longer fits because the source resized is
// ErrReconfigured: allocate against Formats again. A frame the
// compositor fails for its buffer constraints waits one roundtrip for
// the constraints that made it fail before it is told from a refusal.
// ErrSessionStopped
// means the source went away, ErrFailed that the compositor refused
// the copy (a GPU that cannot render into the buffer's layout), and
// ErrClosed that Close ended the wait. Fencing is the caller's: reuse
// a buffer only after its consumer finished reading it.
func (s *DmabufSession) Capture(buf *DmabufBuffer) (FrameInfo, error) {
	if buf == nil || buf.client != s.client {
		return FrameInfo{}, errors.New("capture: the dmabuf buffer is not from the session's client")
	}
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	if err := s.usable(); err != nil {
		return FrameInfo{}, err
	}
	if buf.wl == nil {
		return FrameInfo{}, errors.New("capture: the dmabuf buffer was destroyed")
	}
	if err := s.checkFit(buf); err != nil {
		return FrameInfo{}, err
	}
	size, err := int32s(buf.Attrs.Width, buf.Attrs.Height)
	if err != nil {
		return FrameInfo{}, fmt.Errorf("capture: dmabuf %dx%d: %w", buf.Attrs.Width, buf.Attrs.Height, err)
	}
	dispatch := func(done func() bool) error {
		return s.client.dispatchUntil(func() bool { return s.closing.Load() || done() })
	}
	fs, err := s.cs.captureFrame(dispatch, buf.wl, size[0], size[1])
	if err != nil {
		return FrameInfo{}, err
	}
	if !fs.finished() && !s.cs.state.stopped {
		return FrameInfo{}, fmt.Errorf("%w: the dmabuf session closed during the capture", ErrClosed)
	}
	if fs.reason == failureBufferConstraints {
		if err := s.client.roundtrip(); err != nil {
			return FrameInfo{}, err
		}
		if err := s.checkFit(buf); err != nil {
			return FrameInfo{}, err
		}
	}
	if err := s.cs.verdict(fs); err != nil {
		return FrameInfo{}, fmt.Errorf("capture: a %dx%d %s dmabuf frame: %w", buf.Attrs.Width, buf.Attrs.Height, buf.Attrs.Format, err)
	}
	return fs.info, nil
}

// checkFit refuses a buffer the current constraints do not take:
// ErrReconfigured once they changed since the session opened, a plain
// error while they are the ones the caller allocated against.
func (s *DmabufSession) checkFit(buf *DmabufBuffer) error {
	st := s.cs.state
	if err := s.cs.usable(); err != nil {
		return err
	}
	k := st.current
	if k.fits(buf.Attrs) {
		return nil
	}
	a := buf.Attrs
	if st.generation != s.openGen {
		return fmt.Errorf("%w: the session now takes %dx%d buffers, not this %dx%d %s %s one", ErrReconfigured, k.width, k.height, a.Width, a.Height, a.Format, a.Modifier)
	}
	return fmt.Errorf("capture: a %dx%d %s %s dmabuf does not fit the session's %dx%d constraints; allocate against Formats", a.Width, a.Height, a.Format, a.Modifier, k.width, k.height)
}

// Close ends the session, waking a Capture waiting for the source to
// change, and releases the compositor-side session and source. The
// imported buffers stay the caller's to destroy. Closing twice is a
// no-op.
func (s *DmabufSession) Close() error {
	if s.closing.Swap(true) {
		return nil
	}
	var wake error
	if _, err := s.client.display.Sync(); err != nil {
		wake = fmt.Errorf("capture: wake the dmabuf session's capture: %w", err)
	}
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	if s.client.closed {
		return nil
	}
	return errors.Join(wake, s.cs.destroy())
}
