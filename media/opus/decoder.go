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
	// ErrUnsupportedMapping is a channel mapping family other than 0, 1
	// and 255 (RFC 7845 section 5.1.1).
	ErrUnsupportedMapping = errors.New("unsupported Opus channel mapping")
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
	f20       = FrameSize
	f10       = f20 / 2
	f5        = f10 / 2
	f2p5      = f5 / 2
	// hybridStartBand is the first band CELT codes in a hybrid frame.
	hybridStartBand = 17
)

// opusMode is the coding mode of a frame (RFC 6716 section 3.1).
type opusMode int8

const (
	modeNone opusMode = iota
	modeSILK
	modeHybrid
	modeCELT
)

// bandwidth is a frame's audio bandwidth; zero is none.
type bandwidth int8

const (
	narrowband bandwidth = iota + 1
	mediumband
	wideband
	superWideband
	fullband
)

// celtEnd is the number of CELT bands a bandwidth codes.
func (b bandwidth) celtEnd() int {
	switch b {
	case narrowband:
		return 13
	case mediumband, wideband:
		return 17
	case superWideband:
		return 19
	}
	return nbBands
}

// silkKHz is SILK's internal rate in kHz for a bandwidth.
func (b bandwidth) silkKHz() int32 {
	switch b {
	case narrowband:
		return 8
	case mediumband:
		return 12
	}
	return 16
}

// toc is a packet's table-of-contents byte, decoded.
type toc struct {
	mode      opusMode
	bandwidth bandwidth
	// frameSize is each frame's duration in 48 kHz samples.
	frameSize int
	stereo    bool
}

// parseTOC decodes the TOC byte b (RFC 6716 section 3.1).
func parseTOC(b byte) toc {
	config := int(b >> 3)
	t := toc{stereo: b&4 != 0}
	switch {
	case config < 12:
		t.mode = modeSILK
		t.bandwidth = narrowband + bandwidth(config>>2)
		t.frameSize = [4]int{f10, f20, 2 * f20, 3 * f20}[config&3]
	case config < 16:
		t.mode = modeHybrid
		t.bandwidth = superWideband + bandwidth((config>>1)&1)
		t.frameSize = [2]int{f10, f20}[config&1]
	default:
		t.mode = modeCELT
		t.bandwidth = [4]bandwidth{narrowband, wideband, superWideband, fullband}[(config-16)>>2]
		t.frameSize = f2p5 << (config & 3)
	}
	return t
}

// Decoder decodes an Opus stream (RFC 6716) to 48 kHz interleaved
// signed 16-bit PCM: SILK, hybrid and CELT frames of every size,
// bandwidth and channel count, within channel mapping families 0, 1
// and 255 (RFC 7845 section 5.1.1). A frame of at most one byte
// (discontinuous transmission) is concealed as libopus conceals it. A
// Decoder decodes one stream in order and belongs to one goroutine.
type Decoder struct {
	channels int
	preSkip  int
	streams  []streamDecoder
	coupled  int
	mapping  []byte
	packets  [][]byte
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
	family := head[18]
	var streams, coupled int
	var mapping []byte
	switch family {
	case 0:
		if channels != 1 && channels != 2 {
			return nil, fmt.Errorf("opus: %d channels in mapping family 0: %w", channels, ErrInvalidPacket)
		}
		streams, coupled, mapping = 1, channels-1, []byte{0, 1}[:channels]
	case 1, 255:
		if channels == 0 || family == 1 && channels > 8 {
			return nil, fmt.Errorf("opus: %d channels in mapping family %d: %w", channels, family, ErrInvalidPacket)
		}
		if len(head) < headSize+2+channels {
			return nil, fmt.Errorf("opus: OpusHead of %d bytes has no channel mapping table for %d channels: %w", len(head), channels, ErrInvalidPacket)
		}
		streams, coupled = int(head[19]), int(head[20])
		if streams == 0 || coupled > streams || streams+coupled > 255 {
			return nil, fmt.Errorf("opus: %d streams, %d coupled: %w", streams, coupled, ErrInvalidPacket)
		}
		mapping = slices.Clone(head[21 : 21+channels])
		for c, m := range mapping {
			if m != 255 && int(m) >= streams+coupled {
				return nil, fmt.Errorf("opus: channel %d maps to coded channel %d of %d: %w", c, m, streams+coupled, ErrInvalidPacket)
			}
		}
	default:
		return nil, fmt.Errorf("opus: channel mapping family %d (%d channels): %w", family, channels, ErrUnsupportedMapping)
	}
	gainQ8 := intconv.Int32Bits(uint32(binary.LittleEndian.Uint16(head[16:]))<<16) >> 16
	gain := float32(gomath.Exp2(gainScale * float64(gainQ8)))
	d := &Decoder{
		channels: channels,
		preSkip:  int(binary.LittleEndian.Uint16(head[10:])),
		streams:  make([]streamDecoder, streams),
		coupled:  coupled,
		mapping:  mapping,
		packets:  make([][]byte, streams),
	}
	for s := range d.streams {
		ch := 1
		if s < coupled {
			ch = 2
		}
		d.streams[s].init(ch, gain)
	}
	return d, nil
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
	for s := range d.streams {
		d.streams[s].reset()
	}
}

// Decode decodes one packet and appends its interleaved samples to
// dst. After an error the decoder's history is unchanged.
func (d *Decoder) Decode(dst []int16, packet []byte) ([]int16, error) {
	if len(packet) == 0 {
		return dst, fmt.Errorf("opus: empty packet: %w", ErrInvalidPacket)
	}
	data := packet
	samples := 0
	for s := range d.streams {
		last := s == len(d.streams)-1
		n, used, err := d.streams[s].parse(data, !last)
		if err != nil {
			return dst, fmt.Errorf("opus: stream %d of a %d-byte packet: %w", s, len(packet), err)
		}
		if s > 0 && n != samples {
			return dst, fmt.Errorf("opus: stream %d of a %d-byte packet has %d samples, stream 0 %d: %w", s, len(packet), n, samples, ErrInvalidPacket)
		}
		samples = n
		data = data[used:]
	}
	for s := range d.streams {
		d.streams[s].decode()
	}
	at := len(dst)
	dst = slices.Grow(dst, samples*d.channels)[:at+samples*d.channels]
	out := dst[at:]
	for c, m := range d.mapping {
		if m == 255 {
			for i := range samples {
				out[i*d.channels+c] = 0
			}
			continue
		}
		s, sc := int(m)-d.coupled, 0
		if int(m) < 2*d.coupled {
			s, sc = int(m)/2, int(m)%2
		}
		st := &d.streams[s]
		for i := range samples {
			out[i*d.channels+c] = toInt16(st.pcm[i*st.channels+sc] * 32768)
		}
	}
	return dst, nil
}

func toInt16(v float32) int16 {
	return int16(math.RoundToEven(float64(max(-32768, min(32767, v)))))
}

// streamDecoder is OpusDecoder: one elementary stream of one or two
// channels, its SILK and CELT layers and the mode history that
// transitions between them depend on.
type streamDecoder struct {
	channels       int
	gain           float32
	celt           *celtDecoder
	silk           silkDecoder
	rc             rangeDecoder
	mode           opusMode
	prevMode       opusMode
	bandwidth      bandwidth
	frameSize      int
	streamChannels int
	prevRedundancy bool
	silkChannels   int
	silkKHz        int32
	clip           [2]float32

	next       toc
	frames     [maxPacketFrames][]byte
	nframes    int
	pcm        [maxPacketDur * 2]float32
	transition [f5 * 2]float32
	redundant  [f5 * 2]float32
	silkPCM    [f10 * 2]float32
}

func (s *streamDecoder) init(channels int, gain float32) {
	s.channels = channels
	s.gain = gain
	s.celt = newCELTDecoder(channels)
	s.reset()
}

// reset is OPUS_RESET_STATE.
func (s *streamDecoder) reset() {
	s.celt.reset()
	s.silk.reset()
	s.mode, s.prevMode, s.bandwidth = modeNone, modeNone, 0
	s.prevRedundancy = false
	s.streamChannels = s.channels
	s.frameSize = f2p5
	s.clip = [2]float32{}
}

// parse splits a packet, self-delimited (RFC 6716 appendix B) unless
// it is a stream's last, into its frames (section 3.2) for decode, and
// returns its samples per channel and the bytes it took.
func (s *streamDecoder) parse(packet []byte, selfDelimited bool) (samples, used int, err error) {
	invalid := func(why string, args ...any) error {
		return fmt.Errorf("packet of %d bytes: %s: %w", len(packet), fmt.Sprintf(why, args...), ErrInvalidPacket)
	}
	if len(packet) == 0 {
		return 0, 0, invalid("no TOC")
	}
	t := parseTOC(packet[0])
	data := packet[1:]
	var sizes [maxPacketFrames]int
	count, cbr, lastSize, pad := 1, false, len(data), 0
	switch packet[0] & 3 {
	case 1:
		count, cbr = 2, true
		if !selfDelimited {
			if len(data)%2 != 0 {
				return 0, 0, invalid("two equal frames in %d bytes", len(data))
			}
			lastSize = len(data) / 2
			sizes[0] = lastSize
		}
	case 2:
		count = 2
		n, size, ok := frameLength(data)
		if !ok || size > len(data)-n {
			return 0, 0, invalid("first frame length")
		}
		data = data[n:]
		sizes[0] = size
		lastSize = len(data) - size
	case 3:
		if len(data) == 0 {
			return 0, 0, invalid("no frame count")
		}
		ch := data[0]
		data = data[1:]
		count = int(ch & 0x3f)
		if count == 0 || count*t.frameSize > maxPacketDur {
			return 0, 0, invalid("%d frames of %d samples", count, t.frameSize)
		}
		left := len(data)
		if ch&0x40 != 0 {
			for p := 255; p == 255; {
				if left <= 0 {
					return 0, 0, invalid("padding length")
				}
				p = int(data[0])
				data = data[1:]
				left--
				left -= min(p, 254)
				pad += min(p, 254)
			}
		}
		if left < 0 {
			return 0, 0, invalid("%d bytes of padding", pad)
		}
		cbr = ch&0x80 == 0
		if !cbr {
			lastSize = left
			for i := range count - 1 {
				n, size, ok := frameLength(data[:left])
				if !ok {
					return 0, 0, invalid("frame length")
				}
				left -= n
				if size > left {
					return 0, 0, invalid("frame of %d bytes with %d left", size, left)
				}
				data = data[n:]
				sizes[i] = size
				lastSize -= n + size
			}
			if lastSize < 0 {
				return 0, 0, invalid("frames past the packet")
			}
		} else if !selfDelimited {
			if left%count != 0 {
				return 0, 0, invalid("%d equal frames in %d bytes", count, left)
			}
			lastSize = left / count
			for i := range count - 1 {
				sizes[i] = lastSize
			}
		}
		data = data[:left]
	}
	if selfDelimited {
		n, size, ok := frameLength(data)
		if !ok || size > len(data)-n {
			return 0, 0, invalid("self-delimited frame length")
		}
		data = data[n:]
		sizes[count-1] = size
		if cbr {
			if size*count > len(data) {
				return 0, 0, invalid("%d frames of %d bytes in %d", count, size, len(data))
			}
			for i := range count - 1 {
				sizes[i] = size
			}
		} else if n+size > lastSize {
			return 0, 0, invalid("last frame of %d bytes with %d left", size, lastSize)
		}
	} else {
		sizes[count-1] = lastSize
	}
	for i := range count {
		if sizes[i] > maxFrameBytes {
			return 0, 0, invalid("frame of %d bytes", sizes[i])
		}
		s.frames[i] = data[:sizes[i]]
		data = data[sizes[i]:]
	}
	s.next, s.nframes = t, count
	if !selfDelimited {
		return count * t.frameSize, len(packet), nil
	}
	return count * t.frameSize, cap(packet) - cap(data) + pad, nil
}

// decode is opus_decode_native on the packet parse split: it decodes
// its frames into pcm, applies the head's gain and soft-clips the
// result into [-1, 1].
func (s *streamDecoder) decode() {
	t := s.next
	s.mode, s.bandwidth, s.frameSize = t.mode, t.bandwidth, t.frameSize
	s.streamChannels = 1
	if t.stereo {
		s.streamChannels = 2
	}
	n := 0
	for _, f := range s.frames[:s.nframes] {
		n += s.decodeFrame(f, s.pcm[n*s.channels:], maxPacketDur-n)
	}
	pcm := s.pcm[:n*s.channels]
	scale := s.gain / 32768
	for i, v := range pcm {
		pcm[i] = v * scale
	}
	softClip(pcm, s.channels, &s.clip)
}

// decodeFrame is opus_decode_frame: one frame (nil or of at most one
// byte when lost) of at most frameSize samples per channel into out,
// in int16 scale, with the SILK, hybrid and CELT layers, redundant
// CELT frames and cross-fades of mode transitions. It returns the
// samples per channel decoded.
func (s *streamDecoder) decodeFrame(data []byte, out []float32, frameSize int) int {
	ch := s.channels
	frameSize = min(frameSize, maxPacketDur)
	if len(data) <= 1 {
		data = nil
		frameSize = min(frameSize, s.frameSize)
	}
	var audiosize int
	var mode opusMode
	var bw bandwidth
	if data != nil {
		audiosize, mode, bw = s.frameSize, s.mode, s.bandwidth
		s.rc.reset(data)
	} else {
		audiosize, mode = frameSize, s.prevMode
		if s.prevRedundancy {
			mode = modeCELT
		}
		if mode == modeNone {
			clear(out[:audiosize*ch])
			return audiosize
		}
		if audiosize > f20 {
			for done := 0; done < frameSize; {
				done += s.decodeFrame(nil, out[done*ch:], min(frameSize-done, f20))
			}
			return frameSize
		}
		if audiosize < f20 {
			if audiosize > f10 {
				audiosize = f10
			} else if mode != modeSILK && audiosize > f5 && audiosize < f10 {
				audiosize = f5
			}
		}
	}
	accum := mode != modeCELT
	transition := data != nil && s.prevMode != modeNone &&
		(mode == modeCELT && s.prevMode != modeCELT && !s.prevRedundancy ||
			mode != modeCELT && s.prevMode == modeCELT)
	if transition && mode == modeCELT {
		s.decodeFrame(nil, s.transition[:], min(f5, audiosize))
	}
	frameSize = audiosize
	pcm := out[:frameSize*ch]
	if mode != modeCELT {
		dst := pcm
		if frameSize < f10 {
			dst = s.silkPCM[:f10*ch]
		}
		if s.prevMode == modeCELT {
			s.silk.reset()
		}
		if data != nil {
			s.silkChannels = s.streamChannels
			s.silkKHz = 16
			if mode == modeSILK {
				s.silkKHz = bw.silkKHz()
			}
		}
		f := silkFrame{channels: s.silkChannels, fsKHz: s.silkKHz, ms: max(10, 1000*audiosize/48000)}
		for done := 0; done < frameSize; {
			done += s.silk.decode(&s.rc, f, ch, data == nil, done == 0, dst[done*ch:])
		}
		if frameSize < f10 {
			copy(pcm, dst)
		}
	}
	redundancy, celtToSILK := false, false
	var redundant []byte
	celtData := data
	if mode != modeCELT && data != nil && s.rc.tell()+17+20*b2i(mode == modeHybrid) <= 8*len(data) {
		redundancy = mode == modeSILK || s.rc.bitLogp(12)
		if redundancy {
			celtToSILK = s.rc.bitLogp(1)
			var bytes int
			if mode == modeHybrid {
				bytes = int(s.rc.uint(256)) + 2
			} else {
				bytes = len(data) - (s.rc.tell()+7)>>3
			}
			length := len(data) - bytes
			if length*8 < s.rc.tell() {
				celtData = data[:0]
				redundancy = false
			} else {
				celtData = data[:length]
				redundant = data[length:]
				s.rc.buf = s.rc.buf[:length]
			}
		}
	}
	startBand := 0
	if mode != modeCELT {
		startBand = hybridStartBand
	}
	if redundancy {
		transition = false
	}
	if transition && mode != modeCELT {
		s.decodeFrame(nil, s.transition[:], min(f5, audiosize))
	}
	if bw != 0 {
		s.celt.end = bw.celtEnd()
	}
	s.celt.streamChannels = s.streamChannels
	if redundancy && celtToSILK {
		s.celt.start = 0
		s.celt.decodeFrame(redundant, 1, s.redundant[:f5*ch], false)
	}
	s.celt.start = startBand
	if mode != modeSILK {
		celtSize := min(f20, frameSize)
		if mode != s.prevMode && s.prevMode != modeNone && !s.prevRedundancy {
			s.celt.reset()
		}
		lm := lmOf(celtSize)
		if len(celtData) <= 1 {
			s.celt.conceal(lm, pcm, accum)
		} else {
			s.celt.decode(&s.rc, lm, pcm, accum)
		}
	} else if s.prevMode == modeHybrid && (!redundancy || !celtToSILK || !s.prevRedundancy) {
		s.celt.start = 0
		s.celt.decodeFrame(celtSilence, 0, pcm, accum)
	}
	if redundancy && !celtToSILK {
		s.celt.reset()
		s.celt.start = 0
		s.celt.decodeFrame(redundant, 1, s.redundant[:f5*ch], false)
		end := pcm[ch*(frameSize-f2p5):]
		s.celt.tables.smoothFade(end, s.redundant[ch*f2p5:], end, ch)
	}
	if redundancy && celtToSILK && (s.prevMode != modeSILK || s.prevRedundancy) {
		copy(pcm[:f2p5*ch], s.redundant[:f2p5*ch])
		s.celt.tables.smoothFade(s.redundant[ch*f2p5:], pcm[ch*f2p5:], pcm[ch*f2p5:], ch)
	}
	if transition {
		if audiosize >= f5 {
			copy(pcm[:f2p5*ch], s.transition[:f2p5*ch])
			s.celt.tables.smoothFade(s.transition[ch*f2p5:], pcm[ch*f2p5:], pcm[ch*f2p5:], ch)
		} else {
			s.celt.tables.smoothFade(s.transition[:], pcm, pcm, ch)
		}
	}
	s.prevMode = mode
	s.prevRedundancy = redundancy && !celtToSILK
	return audiosize
}

// celtSilence is the CELT frame a hybrid-to-SILK transition decodes so
// the MDCT fades out.
var celtSilence = []byte{0xff, 0xff}

// lmOf is the LM of a CELT frame of n samples.
func lmOf(n int) int {
	lm := 0
	for shortMdct<<lm < n {
		lm++
	}
	return lm
}

// smoothFade is smooth_fade over 2.5 ms: out cross-fades from in1 to
// in2 with the squared CELT window.
func (t *tables) smoothFade(in1, in2, out []float32, channels int) {
	w := &t.window
	for c := range channels {
		for i := range f2p5 {
			g := float32(w[i] * w[i])
			j := i*channels + c
			out[j] = float32(g*in2[j]) + float32((1-g)*in1[j])
		}
	}
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
