package native

import (
	"context"
	"errors"
	"fmt"
)

// RecordParams describes a record stream.
type RecordParams struct {
	// SampleSpec is the capture format; the server converts to it.
	SampleSpec SampleSpec
	// ChannelMap must have SampleSpec.Channels entries.
	ChannelMap ChannelMap
	// Source is the source name; empty records from the default.
	Source string
	// FragSize is the preferred chunk size in bytes; zero lets the
	// server choose.
	FragSize uint32
	// Props become the stream's proplist (media.name and friends).
	Props PropList
}

// RecordStream is a live record stream. Its audio arrives at the
// handler given to CreateRecordStream.
type RecordStream struct {
	lifetime
	c       *Conn
	channel uint32
	info    RecordInfo
	onData  func([]byte)
}

// RecordInfo is what the server granted for a record stream.
type RecordInfo struct {
	SourceOutput uint32
	MaxLength    uint32
	FragSize     uint32
	SampleSpec   SampleSpec
	ChannelMap   ChannelMap
	SourceIndex  uint32
	SourceName   string
	Suspended    bool
	Latency      uint64
	Format       FormatInfo
}

func decodeRecordReply(r *Reader) (uint32, RecordInfo) {
	channel := r.U32()
	info := RecordInfo{
		SourceOutput: r.U32(),
		MaxLength:    r.U32(),
		FragSize:     r.U32(),
		SampleSpec:   r.SampleSpec(),
		ChannelMap:   r.ChannelMap(),
		SourceIndex:  r.U32(),
		SourceName:   r.Str(),
		Suspended:    r.Bool(),
		Latency:      r.Usec(),
		Format:       r.FormatInfo(),
	}
	return channel, info
}

// CreateRecordStream opens a record stream (CREATE_RECORD_STREAM with
// libpulse's defaults for the flags it does not expose). onData
// receives each audio chunk on the connection's reader goroutine: it
// must not block, and it owns the slice.
func (c *Conn) CreateRecordStream(ctx context.Context, p RecordParams, onData func([]byte)) (*RecordStream, error) {
	if err := validateFormat(p.SampleSpec, p.ChannelMap); err != nil {
		return nil, fmt.Errorf("pulse: record: %w", err)
	}
	if onData == nil {
		return nil, errors.New("pulse: record: nil data handler")
	}
	fragSize := uint32(InvalidIndex)
	if p.FragSize > 0 {
		fragSize = p.FragSize
	}
	var w Writer
	w.SampleSpec(p.SampleSpec)
	w.ChannelMap(p.ChannelMap)
	w.U32(InvalidIndex)
	w.OptString(p.Source)
	w.U32(InvalidIndex) // maxlength: server default
	w.Bool(false)       // corked
	w.U32(fragSize)
	for range 7 { // no_remap .. variable_rate (v12)
		w.Bool(false)
	}
	w.Bool(false) // peak_detect (v13)
	w.Bool(true)  // adjust_latency: fragsize is the latency target
	w.PropList(p.Props)
	w.U32(InvalidIndex) // direct_on_input
	w.Bool(false)       // early_requests (v14)
	w.Bool(false)       // dont_inhibit_auto_suspend (v15)
	w.Bool(false)       // fail_on_suspend
	w.U8(0)             // n_formats (v22): use the sample spec
	w.CVolume(unityVolume(p.SampleSpec.Channels))
	w.Bool(false) // muted
	w.Bool(false) // volume_set
	w.Bool(false) // muted_set
	w.Bool(false) // relative_volume
	w.Bool(false) // passthrough

	s := &RecordStream{ended: make(chan struct{}), c: c, onData: onData}
	hook := func(r *Reader) error {
		channel, info := decodeRecordReply(r)
		if r.Err() != nil {
			return r.Err()
		}
		s.channel, s.info = channel, info
		registerStream(c, &c.records, channel, s)
		return nil
	}
	if _, err := c.request(ctx, CmdCreateRecordStream, &w, hook); err != nil {
		return nil, err
	}
	return s, nil
}

// Info is what the server granted.
func (s *RecordStream) Info() RecordInfo { return s.info }

// Close deletes the stream. Audio stops arriving before the server
// confirms; a stream that already ended closes without a request.
func (s *RecordStream) Close(ctx context.Context) error {
	live := unregisterStream(s.c, &s.c.records, s.channel, s)
	s.end(ErrClosed)
	if !live {
		return nil
	}
	return s.c.exec(ctx, CmdDeleteRecordStream, byIndex(s.channel))
}
