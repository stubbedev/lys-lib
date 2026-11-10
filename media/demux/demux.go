// Package demux reads the tracks and packets of MP4 (ISO BMFF) and
// Matroska/WebM files, in pure Go.
//
// Open sniffs the container, enumerates its tracks (codec, frame size
// or sample rate and channels, the codec's configuration record) and
// returns a File that reads packets in file order with their
// timestamps and seeks to keyframes: through the sample tables (stss)
// of an MP4, its fragments' sample flags, or a Matroska file's Cues.
// A Matroska file without Cues for the track seeks through an index
// built by scanning its clusters once, on the first seek.
//
//	f, err := demux.OpenFile("clip.mkv")
//	for _, t := range f.Tracks() { ... }
//	p, err := f.ReadPacket() // io.EOF after the last packet
//	at, err := f.Seek(30 * time.Second)
//
// Every size and count read from the file is checked against the
// bytes around it before anything is allocated for it, so a malformed
// file fails with an error instead of exhausting memory.
package demux

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Format is a container format.
type Format uint8

// The formats Open reads.
const (
	MP4 Format = iota + 1
	Matroska
	WebM
)

func (f Format) String() string {
	switch f {
	case MP4:
		return "MP4"
	case Matroska:
		return "Matroska"
	case WebM:
		return "WebM"
	}
	return fmt.Sprintf("Format(%d)", uint8(f))
}

// Kind is what a track carries.
type Kind uint8

// The kinds of track.
const (
	Other Kind = iota
	Video
	Audio
	Subtitle
)

func (k Kind) String() string {
	switch k {
	case Video:
		return "video"
	case Audio:
		return "audio"
	case Subtitle:
		return "subtitle"
	}
	return "other"
}

// Codec is a coding format a track is in.
type Codec uint8

// The codecs the demuxer recognises. Unknown is any other, named by
// the track's CodecName.
const (
	Unknown Codec = iota
	H264
	H265
	AV1
	VP8
	VP9
	Opus
	Vorbis
	AAC
	FLAC
	MP3
)

var codecNames = [...]string{
	Unknown: "unknown",
	H264:    "H.264",
	H265:    "H.265",
	AV1:     "AV1",
	VP8:     "VP8",
	VP9:     "VP9",
	Opus:    "Opus",
	Vorbis:  "Vorbis",
	AAC:     "AAC",
	FLAC:    "FLAC",
	MP3:     "MP3",
}

func (c Codec) String() string {
	if int(c) < len(codecNames) {
		return codecNames[c]
	}
	return fmt.Sprintf("Codec(%d)", uint8(c))
}

// Track is one stream of a file.
type Track struct {
	// Index is the track's position in Tracks, which Packet.Track
	// and Select name it by.
	Index int
	Kind  Kind
	Codec Codec
	// CodecName is the container's own name for the codec: an MP4
	// sample entry type ("avc1") or a Matroska codec ID
	// ("V_MPEG4/ISO/AVC").
	CodecName string
	// Config is the codec's configuration record: avcC for H.264,
	// hvcC for H.265, av1C for AV1, vpcC for VP9 in MP4 (empty in
	// Matroska), an OpusHead for Opus (built from dOps in MP4), the
	// Xiph-laced identification, comment and setup headers for Vorbis
	// (see XiphHeaders), the AudioSpecificConfig for AAC and the
	// FLAC stream info for FLAC.
	Config []byte
	// Width and Height are a video track's coded frame size.
	Width, Height int
	// SampleRate and Channels describe an audio track.
	SampleRate, Channels int
	// Duration is the track's length; zero when the file does not
	// say.
	Duration time.Duration
	// CodecDelay is the decoder output to drop at the stream's start
	// as the container states it: an MP4 edit list's media time (AAC
	// encoder priming, Opus pre-skip) or a Matroska CodecDelay. Packet
	// timestamps have it subtracted, so the first packet of such a
	// track starts before zero. An Opus track whose container states
	// none still has its pre-skip in the OpusHead.
	CodecDelay time.Duration
	// SeekPreRoll is how long before a seek target the decoder has
	// to start to converge (80 ms for Opus).
	SeekPreRoll time.Duration
}

// Packet is one access unit or audio frame of a track.
type Packet struct {
	Track int
	// PTS is when the packet's first frame is presented. Packets come
	// in decode order, so reordered video's PTS do not increase
	// monotonically.
	PTS time.Duration
	// Duration is how long the packet presents; zero when the
	// container does not say.
	Duration time.Duration
	// Key is set on packets a decoder can start at.
	Key bool
	// Data is the packet as the container stores it: length-prefixed
	// NAL units for H.264 and H.265 (the prefix size is in Config).
	// It is valid until the next ReadPacket or Seek.
	Data []byte
}

// UnsupportedCodecError is a track whose codec has no decoder.
type UnsupportedCodecError struct {
	File  string
	Track int
	Codec string
}

func (e *UnsupportedCodecError) Error() string {
	return fmt.Sprintf("demux: %s: track %d: codec %s is not supported", e.File, e.Track, e.Codec)
}

// ErrMalformed is a file whose structure is invalid; errors wrap it
// with what was being read and where.
var ErrMalformed = errors.New("malformed file")

// ErrUnknownFormat is a file that is neither MP4 nor Matroska.
var ErrUnknownFormat = errors.New("not an MP4 or Matroska file")

// container is what a format implements: its tracks, packets in file
// order, and positioning before a keyframe.
type container interface {
	tracks() []Track
	duration() time.Duration
	next(selected []bool, buf []byte) (Packet, error)
	seek(track int, t time.Duration, selected []bool) (time.Duration, error)
}

// File is an open media file. A File belongs to one goroutine.
type File struct {
	name     string
	format   Format
	c        container
	selected []bool
	buf      []byte
	closer   io.Closer
}

// OpenFile opens and demuxes the file at path. Close closes it.
func OpenFile(path string) (*File, error) {
	fd, err := os.Open(path) //nolint:gosec // the media file the caller names
	if err != nil {
		return nil, fmt.Errorf("demux: %w", err)
	}
	st, err := fd.Stat()
	if err != nil {
		_ = fd.Close()
		return nil, fmt.Errorf("demux: %w", err)
	}
	f, err := Open(fd, st.Size(), path)
	if err != nil {
		_ = fd.Close()
		return nil, err
	}
	f.closer = fd
	return f, nil
}

// Open demuxes the size bytes of r. name is how errors refer to the
// file.
func Open(r io.ReaderAt, size int64, name string) (*File, error) {
	var head [12]byte
	n, err := r.ReadAt(head[:], 0)
	if n < len(head) {
		if err == nil || errors.Is(err, io.EOF) {
			err = ErrUnknownFormat
		}
		return nil, fmt.Errorf("demux: %s: read the header: %w", name, err)
	}
	src := &source{r: r, size: size}
	f := &File{name: name}
	switch {
	case bytes.Equal(head[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}):
		m, webm, err := openMKV(src)
		if err != nil {
			return nil, fmt.Errorf("demux: %s: %w", name, err)
		}
		f.c, f.format = m, Matroska
		if webm {
			f.format = WebM
		}
	case isMP4Box(head[4:8]):
		m, err := openMP4(src)
		if err != nil {
			return nil, fmt.Errorf("demux: %s: %w", name, err)
		}
		f.c, f.format = m, MP4
	default:
		return nil, fmt.Errorf("demux: %s: %w", name, ErrUnknownFormat)
	}
	f.selected = make([]bool, len(f.c.tracks()))
	for i := range f.selected {
		f.selected[i] = true
	}
	return f, nil
}

// Name is the name the file was opened with.
func (f *File) Name() string { return f.name }

// Format is the file's container format.
func (f *File) Format() Format { return f.format }

// Tracks are the file's tracks, in the container's order.
func (f *File) Tracks() []Track { return f.c.tracks() }

// Duration is the file's length; zero when the file does not say.
func (f *File) Duration() time.Duration { return f.c.duration() }

// Select limits ReadPacket to the tracks given; the others' data is
// not read. Every track is selected after Open.
func (f *File) Select(tracks ...int) error {
	sel := make([]bool, len(f.selected))
	for _, t := range tracks {
		if t < 0 || t >= len(sel) {
			return fmt.Errorf("demux: %s: select track %d of %d", f.name, t, len(sel))
		}
		sel[t] = true
	}
	f.selected = sel
	return nil
}

// ReadPacket returns the next packet of the selected tracks in file
// order, or io.EOF after the last.
func (f *File) ReadPacket() (Packet, error) {
	p, err := f.c.next(f.selected, f.buf[:0])
	if err != nil {
		if errors.Is(err, io.EOF) {
			return Packet{}, io.EOF
		}
		return Packet{}, fmt.Errorf("demux: %s: %w", f.name, err)
	}
	f.buf = p.Data[:0]
	return p, nil
}

// Seek positions the file so the next packets start at the latest
// keyframe of the seek track presented at or before t less its
// SeekPreRoll: the first selected video track, else the first selected
// track. Other selected tracks resume at their packets presented from
// that keyframe's time less their own SeekPreRoll. It returns the
// keyframe's PTS; a caller wanting output from t decodes from there
// and drops what presents before t.
func (f *File) Seek(t time.Duration) (time.Duration, error) {
	track := -1
	for i, tr := range f.c.tracks() {
		if !f.selected[i] {
			continue
		}
		if tr.Kind == Video {
			track = i
			break
		}
		if track < 0 {
			track = i
		}
	}
	if track < 0 {
		return 0, fmt.Errorf("demux: %s: seek to %v with no track selected", f.name, t)
	}
	at, err := f.c.seek(track, t-f.c.tracks()[track].SeekPreRoll, f.selected)
	if err != nil {
		return 0, fmt.Errorf("demux: %s: seek track %d to %v: %w", f.name, track, t, err)
	}
	return at, nil
}

// Unsupported is the error for a track whose codec the caller cannot
// decode, naming the codec and the file.
func (f *File) Unsupported(track int) error {
	t := f.c.tracks()[track]
	name := t.CodecName
	if t.Codec != Unknown {
		name = fmt.Sprintf("%s (%s)", t.Codec, t.CodecName)
	}
	return &UnsupportedCodecError{File: f.name, Track: track, Codec: name}
}

// Close closes the file OpenFile opened; for a File from Open it does
// nothing.
func (f *File) Close() error {
	if f.closer == nil {
		return nil
	}
	return f.closer.Close()
}
