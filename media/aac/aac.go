// Package aac decodes MPEG-4 AAC-LC (ISO/IEC 14496-3 audio object type
// 2) to interleaved signed 16-bit PCM in pure Go: one raw_data_block
// per call, as MP4 and Matroska carry them, in channel configurations
// 1 to 7 (mono to 7.1).
package aac

import (
	"errors"
	"fmt"
	"math"
	"slices"
)

// Decoding fails with these, wrapped with what was being decoded.
var (
	// ErrInvalid is a malformed AudioSpecificConfig or frame.
	ErrInvalid = errors.New("invalid AAC data")
	// ErrUnsupported is a stream outside AAC-LC: another audio object
	// type (HE-AAC's SBR and PS among them), 960-sample frames, a
	// program config element instead of a channel configuration, or a
	// coupling channel element.
	ErrUnsupported = errors.New("unsupported AAC stream")
)

// FrameSize is the samples per channel each frame decodes to.
const FrameSize = 1024

// objectTypeNames names the audio object types a stream may declare,
// for the error refusing it.
var objectTypeNames = map[int]string{
	1: "AAC Main", 3: "AAC SSR", 4: "AAC LTP", 5: "SBR (HE-AAC)", 6: "AAC Scalable",
	17: "ER AAC LC", 19: "ER AAC LTP", 20: "ER AAC Scalable", 22: "ER BSAC",
	23: "ER AAC LD", 29: "PS (HE-AAC v2)", 39: "ER AAC ELD", 42: "USAC (xHE-AAC)",
}

// sampleRates are the rates of the sampling frequency indices.
var sampleRates = [13]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

// elementKind is a syntactic element of a raw_data_block (4.4.2.1).
type elementKind uint8

const (
	elemSCE elementKind = iota
	elemCPE
	elemCCE
	elemLFE
	elemDSE
	elemPCE
	elemFIL
	elemEND
)

// channelElements are the elements of channel configurations 1 to 7
// (table 1.19), in bitstream order.
var channelElements = [8][]elementKind{
	1: {elemSCE},
	2: {elemCPE},
	3: {elemSCE, elemCPE},
	4: {elemSCE, elemCPE, elemSCE},
	5: {elemSCE, elemCPE, elemCPE},
	6: {elemSCE, elemCPE, elemCPE, elemLFE},
	7: {elemSCE, elemCPE, elemCPE, elemCPE, elemLFE},
}

// Config is a stream's AudioSpecificConfig (1.6.2.1), the part an
// AAC-LC decoder needs.
type Config struct {
	// SampleRate is the sampling rate in Hz.
	SampleRate int
	// Channels is the number of output channels.
	Channels  int
	rateIndex int
	elements  []elementKind
}

// ParseConfig reads an AudioSpecificConfig, as MP4's esds and
// Matroska's CodecPrivate carry it.
func ParseConfig(asc []byte) (Config, error) {
	r := bitReader{data: asc}
	aot := r.readInt(5)
	if aot == 31 {
		aot = 32 + r.readInt(6)
	}
	rateIndex := r.readInt(4)
	if rateIndex == 15 {
		r.skip(24)
	}
	channelConfig := r.readInt(4)
	if r.overrun {
		return Config{}, fmt.Errorf("aac: AudioSpecificConfig of %d bytes: %w", len(asc), ErrInvalid)
	}
	if aot != 2 {
		name, ok := objectTypeNames[aot]
		if !ok {
			name = "unknown"
		}
		return Config{}, fmt.Errorf("aac: audio object type %d (%s): %w", aot, name, ErrUnsupported)
	}
	if rateIndex >= len(sampleRates) {
		return Config{}, fmt.Errorf("aac: sampling frequency index %d: %w", rateIndex, ErrUnsupported)
	}
	if r.bit() {
		return Config{}, fmt.Errorf("aac: 960-sample frames: %w", ErrUnsupported)
	}
	if r.bit() {
		r.skip(14)
	}
	if r.bit() {
		return Config{}, fmt.Errorf("aac: GASpecificConfig extension flag set for AAC-LC: %w", ErrInvalid)
	}
	if channelConfig == 0 {
		return Config{}, fmt.Errorf("aac: channel layout in a program config element: %w", ErrUnsupported)
	}
	if channelConfig >= len(channelElements) {
		return Config{}, fmt.Errorf("aac: channel configuration %d: %w", channelConfig, ErrUnsupported)
	}
	if r.overrun {
		return Config{}, fmt.Errorf("aac: AudioSpecificConfig of %d bytes: %w", len(asc), ErrInvalid)
	}
	if r.left() >= 16 && r.peek(11) == 0x2b7 {
		r.skip(11)
		if ext := r.readInt(5); ext == 5 && r.bit() {
			return Config{}, fmt.Errorf("aac: audio object type 5 (SBR (HE-AAC)) signalled as an extension: %w", ErrUnsupported)
		}
	}
	elements := channelElements[channelConfig]
	channels := 0
	for _, e := range elements {
		channels++
		if e == elemCPE {
			channels++
		}
	}
	return Config{SampleRate: sampleRates[rateIndex], Channels: channels, rateIndex: rateIndex, elements: elements}, nil
}

// Decoder decodes an AAC-LC stream's raw data blocks, one 1024-sample
// frame per packet, to interleaved signed 16-bit PCM in the channel
// order of its configuration (front centre first: C, L, R, then the
// surround pairs, then LFE). Perceptual noise substitution uses the
// standard's correlated noise in mid/side bands; dynamic range
// control data is ignored. A Decoder decodes one stream in order and
// belongs to one goroutine.
type Decoder struct {
	cfg      Config
	long     *bandLayout
	short    *bandLayout
	channels []channel
	streams  [2]channelStream
	out      [][FrameSize]float32
	seed     uint32
	imdct    imdct
}

// NewDecoder reads the stream's AudioSpecificConfig.
func NewDecoder(asc []byte) (*Decoder, error) {
	cfg, err := ParseConfig(asc)
	if err != nil {
		return nil, err
	}
	d := &Decoder{
		cfg:      cfg,
		long:     &longLayouts[cfg.rateIndex],
		short:    &shortLayouts[cfg.rateIndex],
		channels: make([]channel, cfg.Channels),
		out:      make([][FrameSize]float32, cfg.Channels),
		seed:     pnsSeed,
	}
	return d, nil
}

// Config is the stream's configuration.
func (d *Decoder) Config() Config { return d.cfg }

// Reset forgets the overlap of the last frame, for decoding from a
// packet that does not follow the last one decoded.
func (d *Decoder) Reset() {
	clear(d.channels)
	d.seed = pnsSeed
}

// Decode decodes one raw_data_block and appends its FrameSize samples
// per channel, interleaved, to dst. After an error the decoder's
// history is unchanged.
func (d *Decoder) Decode(dst []int16, packet []byte) ([]int16, error) {
	r := bitReader{data: packet}
	seed := d.seed
	next, ch := 0, 0
	for {
		kind := elementKind(r.read(3) & 7)
		if r.overrun {
			return dst, d.invalid(packet, "no END element")
		}
		if kind == elemEND {
			break
		}
		switch kind {
		case elemSCE, elemCPE, elemLFE:
			if next >= len(d.cfg.elements) || d.cfg.elements[next] != kind {
				return dst, d.invalid(packet, "element %d is a %s, the configuration has %d elements", next, kindNames[kind], len(d.cfg.elements))
			}
			r.skip(4)
			var err error
			if kind == elemCPE {
				err = d.decodePair(&r, &seed, ch)
			} else {
				err = d.decodeSingle(&r, &seed, ch)
			}
			if err != nil {
				return dst, fmt.Errorf("aac: frame of %d bytes, element %d: %w", len(packet), next, err)
			}
			next++
			ch += 1 + b2i(kind == elemCPE)
		case elemCCE:
			return dst, fmt.Errorf("aac: frame of %d bytes: coupling channel element: %w", len(packet), ErrUnsupported)
		case elemDSE:
			r.skip(4)
			align := r.bit()
			n := r.readInt(8)
			if n == 255 {
				n += r.readInt(8)
			}
			if align {
				r.align()
			}
			r.skip(uint(8 * n))
		case elemPCE:
			skipPCE(&r)
		case elemFIL:
			n := r.readInt(4)
			if n == 15 {
				n += r.readInt(8) - 1
			}
			r.skip(uint(8 * n))
		}
		if r.overrun {
			return dst, d.invalid(packet, "%s element past the end", kindNames[kind])
		}
	}
	if next != len(d.cfg.elements) {
		return dst, d.invalid(packet, "%d of %d channel elements", next, len(d.cfg.elements))
	}
	for c := range d.channels {
		d.channels[c].synthesize(&d.imdct, &d.out[c])
	}
	d.seed = seed
	n := len(d.channels)
	at := len(dst)
	dst = slices.Grow(dst, FrameSize*n)[:at+FrameSize*n]
	out := dst[at:]
	for c := range n {
		src := &d.out[c]
		for i, v := range src {
			out[i*n+c] = toInt16(v)
		}
	}
	return dst, nil
}

var kindNames = [8]string{"SCE", "CPE", "CCE", "LFE", "DSE", "PCE", "FIL", "END"}

func (d *Decoder) invalid(packet []byte, why string, args ...any) error {
	return fmt.Errorf("aac: frame of %d bytes: %s: %w", len(packet), fmt.Sprintf(why, args...), ErrInvalid)
}

// decodeSingle decodes a single channel or LFE element into channel ch.
func (d *Decoder) decodeSingle(r *bitReader, seed *uint32, ch int) error {
	cs := &d.streams[0]
	cs.coef = &d.channels[ch].coef
	if err := d.readStream(r, cs, false); err != nil {
		return err
	}
	cs.dequantise(seed, nil, nil)
	d.stage(cs, ch)
	return nil
}

// decodePair decodes a channel pair element into channels ch and
// ch+1, undoing mid/side and intensity stereo.
func (d *Decoder) decodePair(r *bitReader, seed *uint32, ch int) error {
	l, rt := &d.streams[0], &d.streams[1]
	l.coef, rt.coef = &d.channels[ch].coef, &d.channels[ch+1].coef
	common := r.bit()
	var ms msMask
	if common {
		if err := d.readInfo(r, &l.info); err != nil {
			return err
		}
		rt.info = l.info
		ms.present = r.readInt(2)
		switch ms.present {
		case 1:
			for g := range l.info.groups {
				for sfb := range l.info.maxSFB {
					ms.used[g*l.info.maxSFB+sfb] = r.bit()
				}
			}
		case 3:
			return fmt.Errorf("reserved mid/side mode: %w", ErrInvalid)
		}
	}
	if err := d.readStream(r, l, common); err != nil {
		return fmt.Errorf("left channel: %w", err)
	}
	if err := d.readStream(r, rt, common); err != nil {
		return fmt.Errorf("right channel: %w", err)
	}
	l.dequantise(seed, nil, nil)
	if common {
		rt.dequantise(seed, l, &ms)
		midSide(l, rt, &ms)
	} else {
		rt.dequantise(seed, nil, nil)
	}
	intensity(l, rt, &ms)
	d.stage(l, ch)
	d.stage(rt, ch+1)
	return nil
}

// stage applies TNS to a decoded stream and hands its spectrum and
// window to channel ch for synthesis.
func (d *Decoder) stage(cs *channelStream, ch int) {
	cs.applyTNS(d.long, d.short)
	c := &d.channels[ch]
	c.seq = cs.info.seq
	c.shape = cs.info.shape
}

func toInt16(v float32) int16 {
	return int16(math.RoundToEven(float64(max(-32768, min(32767, v)))))
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// skipPCE reads past a program_config_element (4.4.1.1).
func skipPCE(r *bitReader) {
	r.skip(4 + 2 + 4)
	front, side, back := r.readInt(4), r.readInt(4), r.readInt(4)
	lfe, assoc, cc := r.readInt(2), r.readInt(3), r.readInt(4)
	if r.bit() {
		r.skip(4)
	}
	if r.bit() {
		r.skip(4)
	}
	if r.bit() {
		r.skip(3)
	}
	r.skip(uint(5*(front+side+back) + 4*lfe + 4*assoc + 5*cc))
	r.align()
	r.skip(uint(8 * r.readInt(8)))
}
