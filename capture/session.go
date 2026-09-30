package capture

import (
	"fmt"

	"github.com/stubbedev/gelm/wlr"
)

// sessionState accumulates an ext_image_copy_capture_session_v1's
// buffer constraints; they are final once done arrives.
type sessionState struct {
	width, height int
	// formats are the advertised shm formats in arrival order.
	formats []Format
	done    bool
	stopped bool
}

// HandleImageCopyCaptureSessionV1BufferSize implements the buffer_size
// handler.
func (s *sessionState) HandleImageCopyCaptureSessionV1BufferSize(ev wlr.ImageCopyCaptureSessionV1BufferSizeEvent) {
	s.width, s.height = int(ev.Width), int(ev.Height)
}

// HandleImageCopyCaptureSessionV1ShmFormat implements the shm_format
// handler.
func (s *sessionState) HandleImageCopyCaptureSessionV1ShmFormat(ev wlr.ImageCopyCaptureSessionV1ShmFormatEvent) {
	s.formats = append(s.formats, Format(ev.Format))
}

// HandleImageCopyCaptureSessionV1Done implements the done handler.
func (s *sessionState) HandleImageCopyCaptureSessionV1Done(wlr.ImageCopyCaptureSessionV1DoneEvent) {
	s.done = true
}

// HandleImageCopyCaptureSessionV1Stopped implements the stopped
// handler: the source went away (the window closed, the output was
// unplugged).
func (s *sessionState) HandleImageCopyCaptureSessionV1Stopped(wlr.ImageCopyCaptureSessionV1StoppedEvent) {
	s.stopped = true
}

// negotiated reports whether the constraints are usable: done arrived
// after a size and at least one shm format.
func (s *sessionState) negotiated() bool {
	return s.done && s.width > 0 && s.height > 0 && len(s.formats) > 0
}

// layout picks the shm format to allocate - the first advertised one
// Frame.Image can convert - and the tightly packed stride for it.
// Sessions leave the stride to the client, so a format of unknown
// pixel size cannot be allocated at all.
func (s *sessionState) layout() (Format, int, error) {
	for _, f := range s.formats {
		if f.Supported() {
			return f, s.width * f.BytesPerPixel(), nil
		}
	}
	return 0, 0, fmt.Errorf("%w: the session offers only %v", ErrFormat, s.formats)
}

func (s *sessionState) listen(session *wlr.ImageCopyCaptureSessionV1) {
	session.AddBufferSizeHandler(s)
	session.AddShmFormatHandler(s)
	session.AddDoneHandler(s)
	session.AddStoppedHandler(s)
}

// frameState accumulates one ext_image_copy_capture_frame_v1's
// events.
type frameState struct {
	ready, failed bool
	transform     Transform
	damage        []Rect
	// ptsNanos is the presentation time, 0 when none was reported.
	ptsNanos uint64
}

// HandleImageCopyCaptureFrameV1Transform implements the transform
// handler.
func (f *frameState) HandleImageCopyCaptureFrameV1Transform(ev wlr.ImageCopyCaptureFrameV1TransformEvent) {
	f.transform = Transform(ev.Transform)
}

// HandleImageCopyCaptureFrameV1Damage implements the damage handler;
// empty rects are dropped.
func (f *frameState) HandleImageCopyCaptureFrameV1Damage(ev wlr.ImageCopyCaptureFrameV1DamageEvent) {
	x, y := max(ev.X, 0), max(ev.Y, 0)
	w, h := max(ev.Width, 0), max(ev.Height, 0)
	if w > 0 && h > 0 {
		f.damage = append(f.damage, Rect{X: int(x), Y: int(y), Width: int(w), Height: int(h)})
	}
}

// HandleImageCopyCaptureFrameV1PresentationTime implements the
// presentation_time handler.
func (f *frameState) HandleImageCopyCaptureFrameV1PresentationTime(ev wlr.ImageCopyCaptureFrameV1PresentationTimeEvent) {
	secs := uint64(ev.TvSecHi)<<32 | uint64(ev.TvSecLo)
	f.ptsNanos = secs*1_000_000_000 + uint64(ev.TvNsec)
}

// HandleImageCopyCaptureFrameV1Ready implements the ready handler.
func (f *frameState) HandleImageCopyCaptureFrameV1Ready(wlr.ImageCopyCaptureFrameV1ReadyEvent) {
	f.ready = true
}

// HandleImageCopyCaptureFrameV1Failed implements the failed handler.
func (f *frameState) HandleImageCopyCaptureFrameV1Failed(wlr.ImageCopyCaptureFrameV1FailedEvent) {
	f.failed = true
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

// captureSessionFrame runs one full-buffer frame on a negotiated
// session into buf: create, attach, damage the whole buffer (the
// client tracks no damage of its own), capture, and dispatch until the
// verdict or stop.
func captureSessionFrame(dispatch func(func() bool) error, session *wlr.ImageCopyCaptureSessionV1, ss *sessionState, buf *shmBuffer) (*frameState, error) {
	frame, err := session.CreateFrame()
	if err != nil {
		return nil, err
	}
	defer func() { _ = frame.Destroy() }()
	fs := &frameState{}
	fs.listen(frame)
	if err := frame.AttachBuffer(buf.wl); err != nil {
		return nil, err
	}
	if err := frame.DamageBuffer(0, 0, int32(buf.width), int32(buf.height)); err != nil {
		return nil, err
	}
	if err := frame.Capture(); err != nil {
		return nil, err
	}
	if err := dispatch(func() bool { return fs.finished() || ss.stopped }); err != nil {
		return nil, err
	}
	return fs, nil
}
