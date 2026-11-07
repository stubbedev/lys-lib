package capture

import (
	"errors"
	"fmt"
	"slices"
	"sync"
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

// StreamFrame is one captured frame a Stream published, copied out by
// Latest. Its pixels stay in a ring buffer the capture goroutine reuses a
// couple of frames later, so copy them out (ReadInto, Frame) promptly.
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
	out.Damage = slices.Clone(f.Damage)
	return out, nil
}

// Stream captures one output continuously through
// ext-image-copy-capture on its own connection and goroutine. Unlike
// repeated screencopy, the session negotiates its constraints once,
// and a frame's capture completes only when the output's content
// changed: a static screen costs no copies at all. Consumers poll
// Latest and copy the newest frame out. Err says why the loop ended on its
// own: ErrSessionStopped, ErrReconfigured, ErrFailed after repeated
// refusals, or a dispatch failure.
type Stream struct {
	captureLoop
	info StreamInfo
	ring []*shmBuffer
	// slots are each ring buffer's frame events and number, reused frame
	// after frame.
	slots []streamSlot

	// pubMu guards latest and the published slot's events against the
	// capture goroutine, which captures into another slot meanwhile.
	pubMu  sync.Mutex
	latest int

	// memMu guards the ring mappings against Close unmapping them
	// under a consumer's copy.
	memMu    sync.RWMutex
	unmapped bool
}

type streamSlot struct {
	events frameState
	seq    uint64
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
		captureLoop: newCaptureLoop(client),
		latest:      -1,
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
	s.slots = make([]streamSlot, streamRing)
	go s.run(cs)
	return s, nil
}

// Info is the negotiated geometry.
func (s *Stream) Info() StreamInfo { return s.info }

// Latest copies the most recent published frame into dst, reusing its
// Damage's storage, and reports false before the first.
func (s *Stream) Latest(dst *StreamFrame) bool {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if s.latest < 0 {
		return false
	}
	sl := &s.slots[s.latest]
	damage := append(dst.Damage[:0], sl.events.info.Damage...)
	*dst = StreamFrame{Seq: sl.seq, FrameInfo: sl.events.info, stream: s, buf: s.ring[s.latest]}
	dst.Damage = damage
	return true
}

// Close stops the capture goroutine, waits for it, and drops the
// connection and the ring. Frames already handed out read nothing
// afterwards.
func (s *Stream) Close() error {
	if !s.stop() {
		return nil
	}
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
	wait := s.wait()
	for !s.stopping.Load() {
		buf, fs := s.ring[slot], &s.slots[slot].events
		err := cs.captureFrame(wait, buf.wl, buf.wlWidth, buf.wlHeight, fs)
		if err != nil {
			s.fail(err)
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
			s.fail(err)
			return
		case cs.state.current.width != s.info.Width || cs.state.current.height != s.info.Height:
			// The output changed mode: the ring no longer fits the
			// session's constraints, so every further copy would fail.
			s.fail(ErrReconfigured)
			return
		default:
			// A transient refusal is retried; a compositor that keeps
			// refusing would otherwise spin this loop hot.
			failures++
			if failures >= maxStreamFailures {
				s.fail(err)
				return
			}
			continue
		}
		failures = 0
		s.publish(slot, seq)
		seq++
		slot = (slot + 1) % len(s.ring)
	}
}

// publish makes the frame just captured into slot the latest, numbered
// seq.
func (s *Stream) publish(slot int, seq uint64) {
	s.pubMu.Lock()
	s.slots[slot].seq = seq
	s.latest = slot
	s.pubMu.Unlock()
}
