package opus

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// Decode fails with these, wrapped with what was being decoded.
var (
	// ErrInvalidPacket is a packet whose framing (RFC 6716 section
	// 3) is invalid.
	ErrInvalidPacket = errors.New("invalid Opus packet")
	// ErrUnsupportedMode is a packet in SILK or hybrid mode: the
	// decoder decodes CELT-only packets.
	ErrUnsupportedMode = errors.New("unsupported Opus mode")
)

const (
	// maxPacketFrames is the most frames a packet carries, 120 ms of
	// 2.5 ms frames.
	maxPacketFrames = 48
	maxPacketDur    = 6 * FrameSize
	headSize        = 19
	// gainScale turns OpusHead's output gain (Q8 dB) into a log2
	// gain.
	gainScale = 6.48814081e-4
)

// celtBandEnds are the coded band counts of CELT's bandwidths:
// narrowband, wideband, super-wideband and fullband.
var celtBandEnds = [4]int{13, 17, 19, 21}

// Decoder decodes an Opus stream's CELT-only packets to 48 kHz
// interleaved signed 16-bit PCM, every frame size (2.5 to 20 ms, up to
// 120 ms per packet), bandwidth and channel layout of mapping family 0.
// SILK and hybrid packets fail with ErrUnsupportedMode. A frame of at
// most one byte (discontinuous transmission) decodes as silence rather
// than as libopus's loss concealment. A Decoder decodes one stream in
// order and belongs to one goroutine.
type Decoder struct {
	channels int
	preSkip  int
	gain     float32
	celt     *celtDecoder
	frames   [maxPacketFrames][]byte
	pcm      []float32
	clip     [2]float32
}

// NewDecoder reads the stream's OpusHead identification header (RFC
// 7845 section 5.1), as Ogg's first packet and Matroska's CodecPrivate
// carry it.
func NewDecoder(head []byte) (*Decoder, error) {
	if len(head) < headSize || string(head[:8]) != "OpusHead" {
		return nil, fmt.Errorf("opus: identification header of %d bytes is not an OpusHead: %w", len(head), ErrInvalidPacket)
	}
	if v := head[8]; v>>4 != 0 {
		return nil, fmt.Errorf("opus: OpusHead version %d: %w", v, ErrInvalidPacket)
	}
	channels := int(head[9])
	if family := head[18]; family != 0 {
		return nil, fmt.Errorf("opus: channel mapping family %d (%d channels): %w", family, channels, ErrUnsupportedMode)
	}
	if channels != 1 && channels != 2 {
		return nil, fmt.Errorf("opus: %d channels in mapping family 0: %w", channels, ErrInvalidPacket)
	}
	gainQ8 := intconv.Int32Bits(uint32(binary.LittleEndian.Uint16(head[16:]))<<16) >> 16
	return &Decoder{
		channels: channels,
		preSkip:  int(binary.LittleEndian.Uint16(head[10:])),
		gain:     float32(gomath.Exp2(gainScale * float64(gainQ8))),
		celt:     newCELTDecoder(channels),
	}, nil
}

// Channels is the number of interleaved channels Decode appends.
func (d *Decoder) Channels() int { return d.channels }

// PreSkip is how many samples per channel at the stream's start the
// caller drops: the encoder's priming.
func (d *Decoder) PreSkip() int { return d.preSkip }

// Reset forgets the decoder's history, for decoding from a packet that
// does not follow the last one decoded (after a seek, which pre-rolls
// 80 ms to converge).
func (d *Decoder) Reset() {
	d.celt.reset()
	d.clip = [2]float32{}
}

// Decode decodes one packet and appends its interleaved samples to
// dst. After an error the decoder's history is unchanged.
func (d *Decoder) Decode(dst []int16, packet []byte) ([]int16, error) {
	frames, lm, end, stereo, err := d.parse(packet)
	if err != nil {
		return dst, err
	}
	streamChannels := 1
	if stereo {
		streamChannels = 2
	}
	d.pcm = d.pcm[:0]
	for _, f := range frames {
		d.pcm = d.celt.decode(f, lm, end, streamChannels, d.pcm)
	}
	scale := d.gain / 32768
	for i, v := range d.pcm {
		d.pcm[i] = v * scale
	}
	softClip(d.pcm, d.channels, &d.clip)
	at := len(dst)
	dst = slices.Grow(dst, len(d.pcm))[:at+len(d.pcm)]
	for i, v := range d.pcm {
		dst[at+i] = toInt16(v * 32768)
	}
	return dst, nil
}

func toInt16(v float32) int16 {
	return int16(math.RoundToEven(float64(max(-32768, min(32767, v)))))
}

// parse splits packet into its frames (RFC 6716 section 3.2) and reads
// its TOC: the frames' LM, coded band count and channel count.
func (d *Decoder) parse(packet []byte) (frames [][]byte, lm, end int, stereo bool, err error) {
	if len(packet) == 0 {
		return nil, 0, 0, false, fmt.Errorf("opus: empty packet: %w", ErrInvalidPacket)
	}
	toc := packet[0]
	config := int(toc >> 3)
	if config < 16 {
		mode := "SILK"
		if config >= 12 {
			mode = "hybrid"
		}
		return nil, 0, 0, false, fmt.Errorf("opus: %s packet (configuration %d): %w", mode, config, ErrUnsupportedMode)
	}
	lm = config & 3
	end = celtBandEnds[(config-16)>>2]
	stereo = toc&4 != 0
	body := packet[1:]
	fs := d.frames[:0]
	invalid := func(why string, args ...any) error {
		return fmt.Errorf("opus: packet of %d bytes: %s: %w", len(packet), fmt.Sprintf(why, args...), ErrInvalidPacket)
	}
	switch toc & 3 {
	case 0:
		fs = append(fs, body)
	case 1:
		if len(body)%2 != 0 {
			return nil, 0, 0, false, invalid("two equal frames in %d bytes", len(body))
		}
		fs = append(fs, body[:len(body)/2], body[len(body)/2:])
	case 2:
		n, size, ok := frameLength(body)
		if !ok || size > len(body)-n {
			return nil, 0, 0, false, invalid("first frame length")
		}
		fs = append(fs, body[n:n+size], body[n+size:])
	case 3:
		if len(body) == 0 {
			return nil, 0, 0, false, invalid("no frame count")
		}
		vbr, padded, count := body[0]&0x80 != 0, body[0]&0x40 != 0, int(body[0]&0x3f)
		body = body[1:]
		if count == 0 || count*(shortMdct<<lm) > maxPacketDur {
			return nil, 0, 0, false, invalid("%d frames of %d samples", count, shortMdct<<lm)
		}
		pad := 0
		for padded {
			if len(body) == 0 {
				return nil, 0, 0, false, invalid("padding length")
			}
			p := int(body[0])
			body = body[1:]
			padded = p == 255
			pad += min(p, 254)
		}
		if pad > len(body) {
			return nil, 0, 0, false, invalid("%d bytes of padding", pad)
		}
		body = body[:len(body)-pad]
		if vbr {
			var sizeBuf [maxPacketFrames]int
			sizes := sizeBuf[:0]
			for range count - 1 {
				n, size, ok := frameLength(body)
				if !ok {
					return nil, 0, 0, false, invalid("frame length")
				}
				body = body[n:]
				sizes = append(sizes, size)
			}
			for _, size := range sizes {
				if size > len(body) {
					return nil, 0, 0, false, invalid("frame of %d bytes with %d left", size, len(body))
				}
				fs = append(fs, body[:size])
				body = body[size:]
			}
			fs = append(fs, body)
		} else {
			if len(body)%count != 0 {
				return nil, 0, 0, false, invalid("%d equal frames in %d bytes", count, len(body))
			}
			size := len(body) / count
			for i := range count {
				fs = append(fs, body[i*size:(i+1)*size])
			}
		}
	}
	for _, f := range fs {
		if len(f) > maxFrameBytes {
			return nil, 0, 0, false, invalid("frame of %d bytes", len(f))
		}
	}
	return fs, lm, end, stereo, nil
}

// frameLength reads a frame length of one or two bytes (RFC 6716
// section 3.2.1): the bytes it took, the length and whether it was
// there.
func frameLength(b []byte) (n, size int, ok bool) {
	switch {
	case len(b) == 0:
		return 0, 0, false
	case b[0] < 252:
		return 1, int(b[0]), true
	case len(b) < 2:
		return 0, 0, false
	}
	return 2, int(b[1])*4 + int(b[0]), true
}
