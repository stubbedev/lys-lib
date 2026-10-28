package capture

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// streamRing is the number of shm buffers a Stream rotates through:
// one being captured into, the just-published one the consumer reads,
// and a spare so a slow copy never races the next capture.
const streamRing = 3

// maxStreamFailures is how many copies in a row the compositor may
// fail before a Stream gives up.
const maxStreamFailures = 8

// StreamInfo is the geometry a Stream negotiated, fixed for its
// lifetime: what a consumer (a PipeWire producer, say) advertises.
type StreamInfo struct {
	Width, Height, Stride int
	Format                Format
	// RefreshMHz is the output's current refresh rate, 0 if unknown.
	RefreshMHz int32
	// Transform is the output's transform when the stream opened.
	Transform Transform
}

// StreamFrame is one captured frame a Stream published. Its pixels
// live in a ring buffer the capture goroutine reuses a couple of
// frames later, so copy them out (ReadInto, Frame) promptly.
type StreamFrame struct {
	// Seq increments per published frame; an unchanged Seq means
	// nothing new was captured (the screen is static).
	Seq uint64
	FrameInfo

	stream *Stream
	buf    *shmBuffer
}

// ReadInto copies the frame's pixels (Info().Stride-byte rows) into
// dst, returning the bytes copied; 0 once the stream is closed.
func (f *StreamFrame) ReadInto(dst []byte) int {
	f.stream.memMu.RLock()
	defer f.stream.memMu.RUnlock()
	if f.stream.unmapped {
		return 0
	}
	return f.buf.readInto(dst)
}

// Frame copies the pixels out into a standalone Frame.
func (f *StreamFrame) Frame() (*Frame, error) {
	f.stream.memMu.RLock()
	defer f.stream.memMu.RUnlock()
	if f.stream.unmapped {
		return nil, ErrClosed
	}
	out := f.buf.frame(nil)
	out.Transform = f.Transform
	out.Damage = f.Damage
	return out, nil
}

// Stream captures one output continuously through
// ext-image-copy-capture on its own connection and goroutine. Unlike
// repeated screencopy, the session negotiates its constraints once,
// and a frame's capture completes only when the output's content
// changed: a static screen costs no copies at all. Consumers poll
// Latest and copy the newest frame out.
type Stream struct {
	info   StreamInfo
	client *Client
	ring   []*shmBuffer

	latest atomic.Pointer[StreamFrame]

	stopping atomic.Bool
	done     chan struct{}
	// err is why the capture loop ended on its own (the session
	// stopped, the connection died); read after done closes.
	err error

	// memMu guards the ring mappings against Close unmapping them
	// under a consumer's copy.
	memMu    sync.RWMutex
	unmapped bool
}

// OpenOutputStream starts continuous capture of the output with the
// given connector name, painting the cursor when cursor is set. It
// returns once the session negotiated its constraints and the buffer
// ring exists, so Info is final; the capture goroutine is running.
// ErrUnsupported means the compositor lacks the ext capture protocols:
// fall back to per-frame screencopy.
func OpenOutputStream(outputName string, cursor bool) (*Stream, error) {
	client, err := Connect()
	if err != nil {
		return nil, err
	}
	s, err := openStream(client, outputName, cursor)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return s, nil
}

func openStream(client *Client, outputName string, cursor bool) (*Stream, error) {
	out, ok := client.OutputByName(outputName)
	if !ok {
		return nil, fmt.Errorf("capture: output %q not found", outputName)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	cs, err := client.openSession(out, cursor)
	if err != nil {
		return nil, err
	}
	k := cs.state.current
	format, stride, err := k.layout()
	if err != nil {
		return nil, errors.Join(err, cs.destroy())
	}
	s := &Stream{
		client: client,
		done:   make(chan struct{}),
		info: StreamInfo{
			Width: k.width, Height: k.height, Stride: stride,
			Format: format, RefreshMHz: out.RefreshMHz, Transform: out.Transform,
		},
	}
	for range streamRing {
		buf, err := newShmBuffer(client.shm, s.info.Width, s.info.Height, s.info.Stride, s.info.Format)
		if err != nil {
			s.unmap()
			return nil, errors.Join(err, cs.destroy())
		}
		s.ring = append(s.ring, buf)
	}
	go s.run(cs)
	return s, nil
}

// Info is the negotiated geometry.
func (s *Stream) Info() StreamInfo { return s.info }

// Latest is the most recent published frame, nil before the first.
func (s *Stream) Latest() *StreamFrame { return s.latest.Load() }

// Done is closed when the capture loop ended, on Close or on its own;
// Err then says why.
func (s *Stream) Done() <-chan struct{} { return s.done }

// Err is why the capture loop ended on its own: ErrSessionStopped,
// ErrReconfigured, ErrFailed after repeated refusals, a
// dispatch failure, or nil after Close.
func (s *Stream) Err() error {
	select {
	case <-s.done:
		return s.err
	default:
		return nil
	}
}

// Close stops the capture goroutine, waits for it, and drops the
// connection and the ring. Frames already handed out read nothing
// afterwards.
func (s *Stream) Close() error {
	if s.stopping.Swap(true) {
		<-s.done
		return nil
	}
	// Wake a capture parked on a static screen: the sync's reply is an
	// event, so the loop's dispatch returns and sees the stop flag.
	_, _ = s.client.display.Sync()
	<-s.done
	s.unmap()
	return s.client.Close()
}

func (s *Stream) unmap() {
	s.memMu.Lock()
	defer s.memMu.Unlock()
	for _, b := range s.ring {
		b.unmap()
	}
	s.unmapped = true
}

// run is the capture loop: round-robin a ring slot, capture one frame
// into it, publish it when ready, until the session stops or Close.
func (s *Stream) run(cs *copySession) {
	defer close(s.done)
	defer func() { _ = cs.destroy() }()
	var seq uint64
	slot, failures := 0, 0
	dispatch := func(done func() bool) error {
		return s.client.dispatchUntil(func() bool { return s.stopping.Load() || done() })
	}
	for !s.stopping.Load() {
		buf := s.ring[slot]
		fs, err := cs.captureFrame(dispatch, buf.wl, buf.wlWidth, buf.wlHeight)
		if err != nil {
			if !s.stopping.Load() {
				s.err = err
			}
			return
		}
		if !fs.finished() && !cs.state.stopped {
			// Interrupted by Close; the loop re-checks the stop flag.
			continue
		}
		err = cs.verdict(fs)
		switch {
		case err == nil:
		case errors.Is(err, ErrSessionStopped):
			s.err = err
			return
		case cs.state.current.width != s.info.Width || cs.state.current.height != s.info.Height:
			// The output changed mode: the ring no longer fits the
			// session's constraints, so every further copy would fail.
			s.err = ErrReconfigured
			return
		default:
			// A transient refusal is retried; a compositor that keeps
			// refusing would otherwise spin this loop hot.
			failures++
			if failures >= maxStreamFailures {
				s.err = err
				return
			}
			continue
		}
		failures = 0
		s.latest.Store(&StreamFrame{Seq: seq, FrameInfo: fs.info, stream: s, buf: buf})
		seq++
		slot = (slot + 1) % len(s.ring)
	}
}
