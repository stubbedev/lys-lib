package native

import (
	"context"
	"fmt"
	"math/bits"
	"sync"
	"time"

	"github.com/stubbedev/lys-lib/intconv"
)

// PlaybackParams describes a playback stream.
type PlaybackParams struct {
	// SampleSpec is the format Write takes; the server converts from it.
	SampleSpec SampleSpec
	// ChannelMap must have SampleSpec.Channels entries.
	ChannelMap ChannelMap
	// Sink is the sink name; empty plays on the default.
	Sink string
	// Latency is the target from a written byte to the speaker; zero
	// lets the server choose.
	Latency time.Duration
	// Props become the stream's proplist (media.name and friends).
	Props PropList
}

// PlaybackInfo is what the server granted for a playback stream.
// Latency is the sink's, in microseconds.
type PlaybackInfo struct {
	SinkInput    uint32
	MaxLength    uint32
	TargetLength uint32
	PreBuffer    uint32
	MinRequest   uint32
	SampleSpec   SampleSpec
	ChannelMap   ChannelMap
	SinkIndex    uint32
	SinkName     string
	Suspended    bool
	Latency      uint64
	Format       FormatInfo
}

// PlaybackStream is a live playback stream: Write feeds it as the
// server requests audio and Cork pauses it. Its volume and mute are
// the sink input Info().SinkInput names (SetSinkInputVolume).
type PlaybackStream struct {
	lifetime
	c         *Conn
	channel   uint32
	info      PlaybackInfo
	frameSize int
	mu        sync.Mutex
	requested int
	wake      chan struct{}
}

func decodePlaybackReply(r *Reader) (channel, missing uint32, info PlaybackInfo) {
	channel = r.U32()
	info.SinkInput = r.U32()
	missing = r.U32()
	info.MaxLength = r.U32()
	info.TargetLength = r.U32()
	info.PreBuffer = r.U32()
	info.MinRequest = r.U32()
	info.SampleSpec = r.SampleSpec()
	info.ChannelMap = r.ChannelMap()
	info.SinkIndex = r.U32()
	info.SinkName = r.Str()
	info.Suspended = r.Bool()
	info.Latency = r.Usec()
	info.Format = r.FormatInfo()
	return channel, missing, info
}

func targetLength(spec SampleSpec, latency time.Duration) (uint32, error) {
	switch {
	case latency == 0:
		return InvalidIndex, nil
	case latency < 0:
		return 0, fmt.Errorf("negative latency %v", latency)
	}
	hi, lo := bits.Mul64(uint64(latency), uint64(spec.Rate))
	if hi >= uint64(time.Second) {
		return 0, fmt.Errorf("latency %v is too long", latency)
	}
	frames, _ := bits.Div64(hi, lo, uint64(time.Second))
	if frames == 0 {
		return 0, fmt.Errorf("latency %v is shorter than a frame", latency)
	}
	if frames >= InvalidIndex {
		return 0, fmt.Errorf("latency %v is too long", latency)
	}
	n, err := intconv.Exact[uint32](frames * intconv.Must[uint64](spec.FrameSize()))
	if err != nil || n == InvalidIndex {
		return 0, fmt.Errorf("latency %v is too long", latency)
	}
	return n, nil
}

// CreatePlaybackStream opens an uncorked playback stream
// (CREATE_PLAYBACK_STREAM with libpulse's defaults for the flags it does
// not expose). With a Latency the server adjusts its buffering so the
// whole path meets it.
func (c *Conn) CreatePlaybackStream(ctx context.Context, p PlaybackParams) (*PlaybackStream, error) {
	if err := validateFormat(p.SampleSpec, p.ChannelMap); err != nil {
		return nil, fmt.Errorf("pulse: playback: %w", err)
	}
	tlength, err := targetLength(p.SampleSpec, p.Latency)
	if err != nil {
		return nil, fmt.Errorf("pulse: playback: %w", err)
	}
	c.mu.Lock()
	syncID := c.nextSync
	c.nextSync++
	c.mu.Unlock()

	var w Writer
	w.SampleSpec(p.SampleSpec)
	w.ChannelMap(p.ChannelMap)
	w.U32(InvalidIndex)
	w.OptString(p.Sink)
	w.U32(InvalidIndex) // maxlength: server default
	w.Bool(false)       // corked
	w.U32(tlength)
	w.U32(InvalidIndex) // prebuf: server default
	w.U32(InvalidIndex) // minreq: server default
	w.U32(syncID)
	w.CVolume(unityVolume(p.SampleSpec.Channels))
	for range 7 { // no_remap .. variable_rate (v12)
		w.Bool(false)
	}
	w.Bool(false)          // muted (v13)
	w.Bool(p.Latency != 0) // adjust_latency: tlength is the latency target
	w.PropList(p.Props)
	w.Bool(false) // volume_set (v14)
	w.Bool(false) // early_requests
	w.Bool(false) // muted_set (v15)
	w.Bool(false) // dont_inhibit_auto_suspend
	w.Bool(false) // fail_on_suspend
	w.Bool(false) // relative_volume (v17)
	w.Bool(false) // passthrough (v18)
	w.U8(0)       // n_formats (v21): use the sample spec

	s := &PlaybackStream{
		ended:     make(chan struct{}),
		c:         c,
		frameSize: p.SampleSpec.FrameSize(),
		wake:      make(chan struct{}, 1),
	}
	hook := func(r *Reader) error {
		channel, missing, info := decodePlaybackReply(r)
		if r.Err() != nil {
			return r.Err()
		}
		s.channel, s.info = channel, info
		s.grant(missing)
		registerStream(c, &c.playbacks, channel, s)
		return nil
	}
	if _, err := c.request(ctx, CmdCreatePlaybackStream, &w, hook); err != nil {
		return nil, err
	}
	return s, nil
}

// Info is what the server granted.
func (s *PlaybackStream) Info() PlaybackInfo { return s.info }

// Write sends p, whole frames in the stream's sample spec, as fast as
// the server requests audio, and returns once all of it is sent. A
// corked stream requests nothing, so Write then waits until the stream
// is uncorked, ends, or ctx is done. Write does not allocate; it is for
// one writer at a time.
func (s *PlaybackStream) Write(ctx context.Context, p []byte) error {
	if len(p)%s.frameSize != 0 {
		return fmt.Errorf("pulse: playback write: %d bytes is not a whole number of %d-byte frames", len(p), s.frameSize)
	}
	for len(p) > 0 {
		n, err := s.claim(ctx, len(p))
		if err != nil {
			return fmt.Errorf("pulse: playback write: %w", err)
		}
		if err := s.c.sendAudio(ctx, s.channel, p[:n]); err != nil {
			s.c.fail(fmt.Errorf("pulse: write: %w", err))
			return fmt.Errorf("pulse: playback write: %w", err)
		}
		p = p[n:]
	}
	return nil
}

func (s *PlaybackStream) claim(ctx context.Context, want int) (int, error) {
	for {
		select {
		case <-s.ended:
			return 0, s.err
		default:
		}
		s.mu.Lock()
		n := min(want, s.requested)
		s.requested -= n
		s.mu.Unlock()
		if n > 0 {
			return n, nil
		}
		select {
		case <-s.wake:
		case <-s.ended:
			return 0, s.err
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

func (s *PlaybackStream) grant(n uint32) {
	s.mu.Lock()
	s.requested += int(n)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Cork pauses (true) or resumes (false) playback. A corked stream keeps
// what it buffered and requests nothing until it is uncorked.
func (s *PlaybackStream) Cork(ctx context.Context, corked bool) error {
	w := byIndex(s.channel)
	w.Bool(corked)
	return s.c.exec(ctx, CmdCorkPlaybackStream, w)
}

// Close deletes the stream: a blocked Write returns ErrClosed. A stream
// that already ended closes without a request.
func (s *PlaybackStream) Close(ctx context.Context) error {
	live := unregisterStream(s.c, &s.c.playbacks, s.channel, s)
	s.end(ErrClosed)
	if !live {
		return nil
	}
	return s.c.exec(ctx, CmdDeletePlaybackStream, byIndex(s.channel))
}
