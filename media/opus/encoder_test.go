package opus

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// signal is a test input: interleaved int16 PCM of some channels.
type signal struct {
	name     string
	channels int
	pcm      []int16
	// minSNR is the waveform SNR in dB each decoder must reach; zero
	// checks the spectrum instead.
	minSNR float64
}

const testSeconds = 2

func synth(channels int, f func(i, c int) float64) []int16 {
	n := testSeconds * SampleRate
	pcm := make([]int16, n*channels)
	for i := range n {
		for c := range channels {
			pcm[i*channels+c] = int16(math.Round(32767 * f(i, c)))
		}
	}
	return pcm
}

func testSignals() []signal {
	n := float64(testSeconds * SampleRate)
	sweep := func(i, c int) float64 {
		const f0, f1 = 50.0, 16000.0
		t := float64(i) / SampleRate
		k := math.Log(f1/f0) / (n / SampleRate)
		phase := 2 * math.Pi * f0 * (math.Exp(k*t) - 1) / k
		if c == 1 {
			phase += math.Pi / 3
		}
		return 0.5 * math.Sin(phase)
	}
	tone := func(i, c int) float64 {
		t := float64(i) / SampleRate
		if c == 0 {
			return 0.4*math.Sin(2*math.Pi*440*t) + 0.2*math.Sin(2*math.Pi*3000*t)
		}
		return 0.3*math.Sin(2*math.Pi*1000*t) + 0.25*math.Sin(2*math.Pi*6500*t)
	}
	gated := func(i, c int) float64 {
		if i > SampleRate/2 && i < SampleRate {
			return 0
		}
		return tone(i, c)
	}
	seed := uint32(7)
	noise := func(int, int) float64 {
		seed = 1664525*seed + 1013904223
		return 0.25 * (float64(int32(seed)) / (1 << 31))
	}
	return []signal{
		{name: "sweep", channels: 2, pcm: synth(2, sweep), minSNR: 20},
		{name: "two-tone", channels: 2, pcm: synth(2, tone), minSNR: 22},
		{name: "gated-tone", channels: 2, pcm: synth(2, gated), minSNR: 22},
		{name: "mono-tone", channels: 1, pcm: synth(1, tone), minSNR: 33},
		{name: "noise", channels: 2, pcm: synth(2, noise)},
		{name: "silence", channels: 2, pcm: make([]int16, testSeconds*SampleRate*2)},
	}
}

// encodeOgg codes pcm into an Ogg Opus file (RFC 7845), padding the
// end with silence so the lookahead is flushed and trimming it again by
// the last page's granule position.
func encodeOgg(t *testing.T, enc *Encoder, pcm []int16) []byte {
	t.Helper()
	ch := enc.Channels()
	samples := len(pcm) / ch
	frames := (samples + PreSkip + FrameSize - 1) / FrameSize
	padded := make([]int16, frames*FrameSize*ch)
	copy(padded, pcm)
	var w oggWriter
	w.page(enc.AppendHead(nil), 0, 0x02)
	tags := []byte("OpusTags")
	tags = binary.LittleEndian.AppendUint32(tags, 6)
	tags = append(tags, "lys-ui"...)
	tags = binary.LittleEndian.AppendUint32(tags, 0)
	w.page(tags, 0, 0)
	var pkt []byte
	for f := range frames {
		var err error
		pkt, err = enc.Encode(pkt[:0], padded[f*FrameSize*ch:(f+1)*FrameSize*ch])
		if err != nil {
			t.Fatalf("frame %d: %v", f, err)
		}
		if len(pkt) != enc.PacketBytes() {
			t.Fatalf("frame %d: packet of %d bytes, want %d", f, len(pkt), enc.PacketBytes())
		}
		granule := int64(f+1) * FrameSize
		flags := byte(0)
		if f == frames-1 {
			granule = int64(PreSkip + samples)
			flags = 0x04
		}
		w.page(pkt, granule, flags)
	}
	return w.buf.Bytes()
}

// oggWriter writes one packet per Ogg page.
type oggWriter struct {
	buf bytes.Buffer
	seq uint32
}

func (w *oggWriter) page(packet []byte, granule int64, flags byte) {
	h := []byte("OggS")
	h = append(h, 0, flags)
	h = binary.LittleEndian.AppendUint64(h, uint64(granule))
	h = binary.LittleEndian.AppendUint32(h, 0x6c7973)
	h = binary.LittleEndian.AppendUint32(h, w.seq)
	h = binary.LittleEndian.AppendUint32(h, 0)
	w.seq++
	var lacing []byte
	for n := len(packet); ; n -= 255 {
		if n < 255 {
			lacing = append(lacing, byte(n))
			break
		}
		lacing = append(lacing, 255)
	}
	h = append(h, byte(len(lacing)))
	h = append(h, lacing...)
	pg := append(h, packet...)
	binary.LittleEndian.PutUint32(pg[22:], oggCRC(pg))
	w.buf.Write(pg)
}

func oggCRC(b []byte) uint32 {
	var crc uint32
	for _, v := range b {
		crc ^= uint32(v) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// decode runs ffmpeg's decoder on an Ogg Opus file and returns the PCM;
// any message ffmpeg prints at warning level fails the test.
func decode(t *testing.T, ffmpeg, decoder, path string) []int16 {
	t.Helper()
	cmd := exec.Command(ffmpeg, "-hide_banner", "-nostdin", "-v", "warning", "-c:a", decoder, "-i", path, "-f", "s16le", "-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v: %s", decoder, err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("%s warned: %s", decoder, stderr.String())
	}
	raw := stdout.Bytes()
	pcm := make([]int16, len(raw)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	return pcm
}

// snr is the waveform SNR in dB of got against want over every channel,
// leaving out the first and last frame.
func snr(want, got []int16, channels, lag int) float64 {
	var sig, errSum float64
	n := len(want) / channels
	for i := FrameSize; i < n-FrameSize; i++ {
		j := i + lag
		if j < 0 || j >= len(got)/channels {
			continue
		}
		for c := range channels {
			w := float64(want[i*channels+c])
			d := float64(got[j*channels+c]) - w
			sig += w * w
			errSum += d * d
		}
	}
	return 10 * math.Log10(sig/max(errSum, 1e-9))
}

// alignment is the lag of got against want, within a quarter frame,
// that correlates best over a quarter second of the first channel.
func alignment(want, got []int16, channels int) int {
	best, bestLag := math.Inf(-1), 0
	for lag := -FrameSize / 4; lag <= FrameSize/4; lag++ {
		var corr float64
		for i := FrameSize; i < FrameSize+SampleRate/4; i++ {
			corr += float64(want[i*channels]) * float64(got[(i+lag)*channels])
		}
		if corr > best {
			best, bestLag = corr, lag
		}
	}
	return bestLag
}

// spectrum is the mean power of one channel in octave bands from 62.5
// Hz up, in dB, from Hann-windowed DFTs of 2048-sample blocks.
func spectrum(pcm []int16, channels, c int) []float64 {
	const block = 2048
	n := len(pcm) / channels
	bands := make([]float64, 8)
	var cos, sin [block]float64
	for i := range block {
		cos[i] = math.Cos(2 * math.Pi * float64(i) / block)
		sin[i] = math.Sin(2 * math.Pi * float64(i) / block)
	}
	for start := block; start+block <= n-block; start += 4 * block {
		var x [block]float64
		for i := range block {
			x[i] = (0.5 - 0.5*cos[i]) * float64(pcm[(start+i)*channels+c])
		}
		for k := 1; k < block/2; k++ {
			hz := float64(k) * SampleRate / block
			band := int(math.Floor(math.Log2(hz / 62.5)))
			if band < 0 || band >= len(bands) {
				continue
			}
			var re, im float64
			for i, v := range x {
				re += v * cos[k*i%block]
				im -= v * sin[k*i%block]
			}
			bands[band] += re*re + im*im
		}
	}
	for i, v := range bands {
		bands[i] = 10 * math.Log10(v+1)
	}
	return bands
}

func TestEncodeDecodesWithFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	for _, s := range testSignals() {
		t.Run(s.name, func(t *testing.T) {
			enc, err := NewEncoder(s.channels, 128000)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), s.name+".opus")
			if err := os.WriteFile(path, encodeOgg(t, enc, s.pcm), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, decoder := range []string{"libopus", "opus"} {
				got := decode(t, ffmpeg, decoder, path)
				if len(got) != len(s.pcm) {
					t.Fatalf("%s decoded %d samples, want %d", decoder, len(got), len(s.pcm))
				}
				switch {
				case s.name == "silence":
					for i, v := range got {
						if v != 0 {
							t.Fatalf("%s: sample %d of silence is %d", decoder, i, v)
						}
					}
				case s.minSNR > 0:
					aligned := snr(s.pcm, got, s.channels, 0)
					t.Logf("%s: SNR %.2f dB", decoder, aligned)
					if bestLag := alignment(s.pcm, got, s.channels); bestLag != 0 {
						t.Errorf("%s: output is %d samples off the input; PreSkip is wrong", decoder, bestLag)
					}
					if aligned < s.minSNR {
						t.Errorf("%s: SNR %.2f dB, want at least %.0f dB", decoder, aligned, s.minSNR)
					}
				default:
					for c := range s.channels {
						want, have := spectrum(s.pcm, s.channels, c), spectrum(got, s.channels, c)
						for b := range want {
							if d := have[b] - want[b]; math.Abs(d) > 1.5 {
								t.Errorf("%s: channel %d octave %d is %.2f dB off the input", decoder, c, b, d)
							}
						}
						t.Logf("%s: channel %d octave levels in %.1f out %.1f", decoder, c, want, have)
					}
				}
			}
		})
	}
}

func TestEncodeDecodesAtEveryBitrate(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	sweep := testSignals()[0]
	for _, c := range []struct {
		channels, bitrate int
		minSNR            float64
	}{
		{2, MinBitrate, -5},
		{1, MinBitrate, -5},
		{2, 24000, 4},
		{2, 64000, 10},
		{1, 64000, 20},
		{2, 256000, 27},
		{2, MaxBitrate, 32},
		{1, MaxBitrate, 32},
	} {
		t.Run(strconv.Itoa(c.channels)+"ch-"+strconv.Itoa(c.bitrate), func(t *testing.T) {
			pcm := sweep.pcm
			if c.channels == 1 {
				pcm = make([]int16, len(sweep.pcm)/2)
				for i := range pcm {
					pcm[i] = sweep.pcm[2*i]
				}
			}
			enc, err := NewEncoder(c.channels, c.bitrate)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "sweep.opus")
			if err := os.WriteFile(path, encodeOgg(t, enc, pcm), 0o600); err != nil {
				t.Fatal(err)
			}
			got := decode(t, ffmpeg, "libopus", path)
			if len(got) != len(pcm) {
				t.Fatalf("decoded %d samples, want %d", len(got), len(pcm))
			}
			v := snr(pcm, got, c.channels, 0)
			t.Logf("SNR %.2f dB", v)
			if v < c.minSNR {
				t.Errorf("SNR %.2f dB, want at least %.0f dB", v, c.minSNR)
			}
		})
	}
}

func TestNewEncoderRejectsBadArguments(t *testing.T) {
	for _, c := range []struct{ channels, bitrate int }{{0, 128000}, {3, 128000}, {2, MinBitrate - 1}, {2, MaxBitrate + 1}} {
		if _, err := NewEncoder(c.channels, c.bitrate); err == nil {
			t.Errorf("NewEncoder(%d, %d) succeeded", c.channels, c.bitrate)
		}
	}
}

func TestEncodeRejectsWrongFrameSize(t *testing.T) {
	enc, err := NewEncoder(2, 128000)
	if err != nil {
		t.Fatal(err)
	}
	dst := []byte{1, 2, 3}
	got, err := enc.Encode(dst, make([]int16, FrameSize))
	if err == nil {
		t.Fatal("a mono-sized frame encoded on a stereo encoder")
	}
	if !bytes.Equal(got, dst) {
		t.Errorf("a failed Encode changed dst to %v", got)
	}
}

func TestEncodeAppendsOneCBRPacket(t *testing.T) {
	enc, err := NewEncoder(2, 128000)
	if err != nil {
		t.Fatal(err)
	}
	prefix := []byte("prefix")
	pkt, err := enc.Encode(append([]byte(nil), prefix...), make([]int16, 2*FrameSize))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pkt, prefix) {
		t.Fatal("Encode overwrote dst")
	}
	if got := len(pkt) - len(prefix); got != 320 {
		t.Errorf("128 kb/s packet is %d bytes, want 320", got)
	}
	if toc := pkt[len(prefix)]; toc != 31<<3|1<<2 {
		t.Errorf("TOC = %#02x, want CELT FB 20 ms stereo code 0 (%#02x)", toc, 31<<3|1<<2)
	}
}

func TestEncodeAllocatesNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("-race instrumentation allocates")
	}
	enc, err := NewEncoder(2, 128000)
	if err != nil {
		t.Fatal(err)
	}
	pcm := testSignals()[0].pcm
	frames := len(pcm) / (2 * FrameSize)
	dst := make([]byte, 0, enc.PacketBytes())
	frame := 0
	allocs := testing.AllocsPerRun(50, func() {
		off := frame % frames * 2 * FrameSize
		frame++
		if _, err := enc.Encode(dst[:0], pcm[off:off+2*FrameSize]); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("Encode allocates %.1f times per packet, want 0", allocs)
	}
}

func BenchmarkEncode(b *testing.B) {
	enc, err := NewEncoder(2, 128000)
	if err != nil {
		b.Fatal(err)
	}
	pcm := testSignals()[0].pcm
	frames := len(pcm) / (2 * FrameSize)
	dst := make([]byte, 0, enc.PacketBytes())
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		off := i % frames * 2 * FrameSize
		if _, err := enc.Encode(dst[:0], pcm[off:off+2*FrameSize]); err != nil {
			b.Fatal(err)
		}
	}
}

func TestAppendHead(t *testing.T) {
	enc, err := NewEncoder(2, 128000)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 2, PreSkip, 0, 0x80, 0xbb, 0, 0, 0, 0, 0}
	if got := enc.AppendHead(nil); !bytes.Equal(got, want) {
		t.Errorf("AppendHead = %v, want %v", got, want)
	}
}
