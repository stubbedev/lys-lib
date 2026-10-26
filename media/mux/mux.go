// Package mux writes timestamped H.264, H.265 and Opus samples into
// fragmented MP4 (NewMP4) and Matroska (NewMKV) files, in pure Go.
//
// Video samples are Annex-B access units without B-frames, so
// presentation and decode order agree; audio samples are single Opus
// packets at 48 kHz. Every sample's duration runs to the next one's
// PTS on the same track, and the last repeats the one before it. Both
// muxers write whole fragments or clusters at a time, so a file cut
// short by a crash still plays up to the last one written.
//
//	m, err := mux.NewMP4(f, mux.H264{SPS: sps, PPS: pps}, mux.Opus{Channels: 2, PreSkip: 312})
//	err = m.WriteSample(mux.Sample{Track: 0, PTS: pts, Data: accessUnit})
//	err = m.Close()
package mux

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"time"
)

// Track describes one stream of a file: H264, H265 or Opus.
type Track interface{ describe() (track, error) }

// H264 is an H.264 video track. SPS and PPS are the parameter sets as
// raw NAL units (header byte included, no start code); the frame size
// comes from the SPS.
type H264 struct{ SPS, PPS []byte }

// H265 is an H.265 video track. VPS, SPS and PPS are the parameter
// sets as raw NAL units (two-byte header included, no start code); the
// frame size comes from the SPS.
type H265 struct{ VPS, SPS, PPS []byte }

// Opus is a 48 kHz Opus track with channel mapping family 0. PreSkip
// is the encoder's priming in 48 kHz samples, dropped on playback.
type Opus struct {
	Channels uint8
	PreSkip  uint16
}

// Sample is one access unit or packet. Track indexes the tracks the
// muxer was made with. PTS is the presentation time on a clock shared
// by all tracks, starting at or after zero, strictly increasing within
// a track; an Opus packet's PTS counts its pre-skip samples too. Data
// is an Annex-B access unit for video (in-band parameter sets must
// match the track's and are dropped, as are access unit delimiters)
// and one Opus packet for audio. The muxer copies Data.
type Sample struct {
	Track int
	PTS   time.Duration
	Data  []byte
}

var errClosed = errors.New("mux: the muxer is closed")

type codec uint8

const (
	codecH264 codec = iota + 1
	codecH265
	codecOpus
)

const (
	opusRate     = 48000
	opusPreRoll  = 80 * time.Millisecond
	nalLenSize   = 4
	segmentSpan  = time.Second
	videoTicksHz = 90000
)

type track struct {
	codec         codec
	width, height int
	config        []byte
	params        [][]byte
	channels      uint8
	preSkip       uint16
	n             int
	last, prev    time.Duration
}

func (t *track) video() bool { return t.codec != codecOpus }

func (t *track) timescale() uint32 {
	if t.video() {
		return videoTicksHz
	}
	return opusRate
}

func (t *track) tickGap() uint64 {
	return ticks(t.last, t.timescale()) - ticks(t.prev, t.timescale())
}

func (t *track) lastDuration() time.Duration {
	if t.n < 2 {
		return 0
	}
	return t.last - t.prev
}

func (h H264) describe() (track, error) {
	sps, err := parseAVCSPS(h.SPS)
	if err != nil {
		return track{}, err
	}
	if len(h.PPS) == 0 || nalTypeH264(h.PPS) != nalH264PPS {
		return track{}, errors.New("mux: H.264 PPS is not a PPS NAL unit")
	}
	return track{
		codec: codecH264, width: sps.width, height: sps.height,
		config: avcC(sps, h.SPS, h.PPS), params: [][]byte{h.SPS, h.PPS},
	}, nil
}

func (h H265) describe() (track, error) {
	sps, err := parseHEVCSPS(h.SPS)
	if err != nil {
		return track{}, err
	}
	if len(h.VPS) < 2 || nalTypeH265(h.VPS) != nalH265VPS {
		return track{}, errors.New("mux: H.265 VPS is not a VPS NAL unit")
	}
	if len(h.PPS) < 2 || nalTypeH265(h.PPS) != nalH265PPS {
		return track{}, errors.New("mux: H.265 PPS is not a PPS NAL unit")
	}
	return track{
		codec: codecH265, width: sps.width, height: sps.height,
		config: hvcC(sps, h.VPS, h.SPS, h.PPS), params: [][]byte{h.VPS, h.SPS, h.PPS},
	}, nil
}

func (o Opus) describe() (track, error) {
	if o.Channels != 1 && o.Channels != 2 {
		return track{}, fmt.Errorf("mux: Opus with %d channels, mapping family 0 carries 1 or 2", o.Channels)
	}
	return track{codec: codecOpus, config: opusHead(o.Channels, o.PreSkip), channels: o.Channels, preSkip: o.PreSkip}, nil
}

func opusHead(channels uint8, preSkip uint16) []byte {
	b := append([]byte("OpusHead"), 1, channels)
	b = binary.LittleEndian.AppendUint16(b, preSkip)
	b = binary.LittleEndian.AppendUint32(b, opusRate)
	return append(b, 0, 0, 0)
}

type base struct {
	tracks  []track
	open    bool
	start   time.Duration
	scratch []byte
	err     error
}

func newBase(descs []Track) (base, error) {
	if len(descs) == 0 {
		return base{}, errors.New("mux: no tracks")
	}
	tracks := make([]track, len(descs))
	for i, d := range descs {
		if d == nil {
			return base{}, fmt.Errorf("mux: track %d is nil", i)
		}
		t, err := d.describe()
		if err != nil {
			return base{}, fmt.Errorf("mux: track %d: %w", i, err)
		}
		tracks[i] = t
	}
	return base{tracks: tracks}, nil
}

type unit struct {
	t    *track
	data []byte
	key  bool
}

func (b *base) accept(s Sample) (unit, error) {
	if b.err != nil {
		return unit{}, b.err
	}
	if s.Track < 0 || s.Track >= len(b.tracks) {
		return unit{}, fmt.Errorf("mux: sample for track %d, the file has %d", s.Track, len(b.tracks))
	}
	t := &b.tracks[s.Track]
	if s.PTS < 0 {
		return unit{}, fmt.Errorf("mux: track %d sample at %v is before zero", s.Track, s.PTS)
	}
	if t.n > 0 && s.PTS <= t.last {
		return unit{}, fmt.Errorf("mux: track %d sample at %v does not follow the one at %v", s.Track, s.PTS, t.last)
	}
	data, key, err := t.payload(b.scratch[:0], s.Data)
	if err != nil {
		return unit{}, fmt.Errorf("mux: track %d sample at %v: %w", s.Track, s.PTS, err)
	}
	b.scratch = data
	t.prev, t.last = t.last, s.PTS
	t.n++
	return unit{t: t, data: data, key: key}, nil
}

func (b *base) cut(u unit) bool {
	due := b.open && (u.t.video() && u.key || u.t.last-b.start >= segmentSpan)
	if due || !b.open {
		b.open, b.start = true, u.t.last
	}
	return due
}

func (b *base) fail(err error) error {
	if b.err == nil {
		b.err = err
	}
	return err
}

func (t *track) payload(dst, data []byte) ([]byte, bool, error) {
	if len(data) == 0 {
		return nil, false, errors.New("empty sample")
	}
	if !t.video() {
		return append(dst, data...), true, nil
	}
	key := false
	err := annexB(data, func(nal []byte) error {
		typ, drop := t.classify(nal)
		switch {
		case typ == nalParamSet:
			for _, p := range t.params {
				if bytes.Equal(p, nal) {
					return nil
				}
			}
			return errors.New("an in-band parameter set differs from the track's")
		case drop:
			return nil
		case typ == nalKey:
			key = true
		}
		dst = binary.BigEndian.AppendUint32(dst, uint32(len(nal)))
		dst = append(dst, nal...)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if len(dst) == 0 {
		return nil, false, errors.New("access unit has no picture NAL units")
	}
	return dst, key, nil
}

type nalRole uint8

const (
	nalPicture nalRole = iota
	nalKey
	nalParamSet
)

const (
	nalH264IDR = 5
	nalH264SPS = 7
	nalH264PPS = 8
	nalH264AUD = 9

	nalH265IRAPFirst = 16
	nalH265IRAPLast  = 23
	nalH265VPS       = 32
	nalH265SPS       = 33
	nalH265PPS       = 34
	nalH265AUD       = 35
)

func nalTypeH264(nal []byte) uint8 { return nal[0] & 0x1f }

func nalTypeH265(nal []byte) uint8 { return nal[0] >> 1 & 0x3f }

func (t *track) classify(nal []byte) (role nalRole, drop bool) {
	if t.codec == codecH264 {
		switch nalTypeH264(nal) {
		case nalH264IDR:
			return nalKey, false
		case nalH264SPS, nalH264PPS:
			return nalParamSet, true
		case nalH264AUD:
			return nalPicture, true
		}
		return nalPicture, false
	}
	switch typ := nalTypeH265(nal); {
	case typ >= nalH265IRAPFirst && typ <= nalH265IRAPLast:
		return nalKey, false
	case typ == nalH265VPS || typ == nalH265SPS || typ == nalH265PPS:
		return nalParamSet, true
	case typ == nalH265AUD:
		return nalPicture, true
	}
	return nalPicture, false
}

var startCode = []byte{0, 0, 1}

func annexB(au []byte, each func(nal []byte) error) error {
	i := bytes.Index(au, startCode)
	if i < 0 || bytes.IndexFunc(au[:i], func(r rune) bool { return r != 0 }) >= 0 {
		return errors.New("access unit does not start with an Annex-B start code")
	}
	rest := au[i+len(startCode):]
	for len(rest) > 0 {
		end := bytes.Index(rest, startCode)
		nal := rest
		if end >= 0 {
			nal, rest = rest[:end], rest[end+len(startCode):]
		} else {
			rest = nil
		}
		nal = bytes.TrimRight(nal, "\x00")
		if len(nal) == 0 {
			continue
		}
		if err := each(nal); err != nil {
			return err
		}
	}
	return nil
}

func ticks(d time.Duration, hz uint32) uint64 {
	hi, lo := bits.Mul64(uint64(d), uint64(hz))
	q, _ := bits.Div64(hi, lo, uint64(time.Second))
	return q
}
