package pulsetest

import (
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/pulse/native"
)

const defaultTargetLength = 8192

// Playback is one playback stream a client created. The fake grants the
// requested target length as the first request, then asks for more only
// through Request.
type Playback struct {
	c             *conn
	Channel       uint32
	SinkInput     uint32
	Sink          string
	SinkIndex     uint32
	SampleSpec    native.SampleSpec
	ChannelMap    native.ChannelMap
	TargetLength  uint32
	AdjustLatency bool
	SyncID        uint32
	Props         native.PropList
	corked        bool
	data          []byte
	deleted       bool
}

// Playbacks lists the live playback streams.
func (s *Server) Playbacks() []*Playback {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Playback
	for _, p := range s.playbacks {
		if !p.deleted {
			out = append(out, p)
		}
	}
	return out
}

// WaitPlaybacks waits until n playback streams are live and returns
// them.
func (s *Server) WaitPlaybacks(t testing.TB, n int) []*Playback {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ps := s.Playbacks(); len(ps) == n {
			return ps
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pulsetest: %d playback streams, want %d", len(s.Playbacks()), n)
	return nil
}

// Data is the audio received so far.
func (p *Playback) Data() []byte {
	s := p.c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(p.data)
}

// WaitData waits until at least n bytes of audio arrived and returns
// them.
func (p *Playback) WaitData(t testing.TB, n int) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if d := p.Data(); len(d) >= n {
			return d
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pulsetest: %d bytes of playback audio, want %d", len(p.Data()), n)
	return nil
}

// Corked reports whether the client corked the stream.
func (p *Playback) Corked() bool {
	s := p.c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	return p.corked
}

// Request asks the client for n more bytes (REQUEST).
func (p *Playback) Request(n uint32) {
	var w native.Writer
	w.U32(p.Channel)
	w.U32(n)
	p.c.packet(native.CmdRequest, native.ControlChannel, w.Bytes())
}

// Kill ends the stream from the server side (PLAYBACK_STREAM_KILLED).
func (p *Playback) Kill() {
	s := p.c.s
	s.mu.Lock()
	p.deleted = true
	s.mu.Unlock()
	var w native.Writer
	w.U32(p.Channel)
	p.c.packet(native.CmdPlaybackStreamKilled, native.ControlChannel, w.Bytes())
	s.RemoveSinkInput(p.SinkInput)
}

func (s *Server) playbackData(f native.Frame) bool {
	for _, p := range s.playbacks {
		if p.Channel == f.Channel && !p.deleted {
			p.data = append(p.data, f.Payload...)
			return true
		}
	}
	return false
}

func (c *conn) createPlayback(tag uint32, r *native.Reader, failed func() bool) bool {
	v := c.negotiated()
	p := &Playback{c: c}
	p.SampleSpec = r.SampleSpec()
	p.ChannelMap = r.ChannelMap()
	sinkIndex := r.U32()
	p.Sink, _ = r.String()
	_ = r.U32()
	p.corked = r.Bool()
	p.TargetLength = r.U32()
	_ = r.U32()
	_ = r.U32()
	p.SyncID = r.U32()
	vol := r.CVolume()
	if v >= 12 {
		for range 7 {
			_ = r.Bool()
		}
	}
	if v >= 13 {
		_ = r.Bool()
		p.AdjustLatency = r.Bool()
		p.Props = r.PropList()
	}
	if v >= 14 {
		_ = r.Bool()
		_ = r.Bool()
	}
	if v >= 15 {
		for range 3 {
			_ = r.Bool()
		}
	}
	if v >= 17 {
		_ = r.Bool()
	}
	if v >= 18 {
		_ = r.Bool()
	}
	if v >= 21 {
		n := r.U8()
		for range n {
			_ = r.FormatInfo()
		}
	}
	if r.Err() != nil || !r.EOF() {
		return false
	}
	if failed() {
		return true
	}
	if sinkIndex != native.InvalidIndex || p.SampleSpec.FrameSize() == 0 ||
		len(p.ChannelMap) != int(p.SampleSpec.Channels) || len(vol) != int(p.SampleSpec.Channels) {
		c.fail(tag, native.ErrInvalid)
		return true
	}
	if p.TargetLength == native.InvalidIndex {
		p.TargetLength = defaultTargetLength
	}
	s := c.s
	s.mu.Lock()
	name := p.Sink
	if name == "" {
		name = s.info.DefaultSink
	}
	sink, found := named(s.sinks, name)
	if found {
		p.Channel = s.nextChannel
		s.nextChannel++
		p.SinkInput = s.nextIndex
		s.nextIndex++
		p.SinkIndex = sink.Index
		s.playbacks = append(s.playbacks, p)
		s.sinkInputs[p.SinkInput] = native.StreamInfo{
			Index: p.SinkInput, Name: p.Props["media.name"], OwnerModule: native.InvalidIndex,
			Client: 7, Device: sink.Index, SampleSpec: p.SampleSpec, ChannelMap: p.ChannelMap,
			Volume: vol, Props: p.Props, Corked: p.corked, HasVolume: true, VolumeWritable: true,
			Format: native.FormatInfo{Encoding: native.EncodingPCM, Props: native.PropList{}},
		}
	}
	s.mu.Unlock()
	if !found {
		c.fail(tag, native.ErrNoEntity)
		return true
	}
	var w native.Writer
	w.U32(p.Channel)
	w.U32(p.SinkInput)
	w.U32(p.TargetLength)
	w.U32(4 << 20)
	w.U32(p.TargetLength)
	w.U32(p.TargetLength)
	w.U32(p.TargetLength / 4)
	if v >= 12 {
		w.SampleSpec(p.SampleSpec)
		w.ChannelMap(p.ChannelMap)
		w.U32(sink.Index)
		w.String(sink.Name)
		w.Bool(sink.State == native.StateSuspended)
	}
	if v >= 13 {
		w.Usec(sink.Latency)
	}
	if v >= 21 {
		w.FormatInfo(native.FormatInfo{Encoding: native.EncodingPCM, Props: native.PropList{}})
	}
	c.reply(tag, &w)
	s.Emit(native.SubscribeEvent{Facility: native.FacilitySinkInput, Operation: native.OpNew, Index: p.SinkInput})
	return true
}

func (c *conn) ownPlayback(channel uint32) *Playback {
	for _, p := range c.s.playbacks {
		if p.c == c && p.Channel == channel && !p.deleted {
			return p
		}
	}
	return nil
}

func (c *conn) corkPlayback(tag uint32, r *native.Reader, failed func() bool) bool {
	channel := r.U32()
	corked := r.Bool()
	if r.Err() != nil || !r.EOF() {
		return false
	}
	if failed() {
		return true
	}
	s := c.s
	s.mu.Lock()
	p := c.ownPlayback(channel)
	if p != nil {
		p.corked = corked
		if st, ok := s.sinkInputs[p.SinkInput]; ok {
			st.Corked = corked
			s.sinkInputs[p.SinkInput] = st
		}
	}
	s.mu.Unlock()
	if p == nil {
		c.fail(tag, native.ErrNoEntity)
		return true
	}
	c.reply(tag, nil)
	s.Emit(native.SubscribeEvent{Facility: native.FacilitySinkInput, Operation: native.OpChange, Index: p.SinkInput})
	return true
}

func (c *conn) deletePlayback(tag uint32, r *native.Reader, failed func() bool) bool {
	channel := r.U32()
	if r.Err() != nil || !r.EOF() {
		return false
	}
	if failed() {
		return true
	}
	s := c.s
	s.mu.Lock()
	p := c.ownPlayback(channel)
	if p != nil {
		p.deleted = true
	}
	s.mu.Unlock()
	if p == nil {
		c.fail(tag, native.ErrNoEntity)
		return true
	}
	c.reply(tag, nil)
	s.RemoveSinkInput(p.SinkInput)
	return true
}
