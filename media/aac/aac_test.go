package aac

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/media/demux"
	"github.com/stubbedev/lys-lib/media/internal/pcm"
	"github.com/stubbedev/lys-lib/testbuild"
)

func TestDecodersShareTheTables(t *testing.T) {
	asc := []byte{0x12, 0x10}
	first, err := NewDecoder(asc)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDecoder(asc)
	if err != nil {
		t.Fatal(err)
	}
	if first.tables == nil || first.tables != second.tables {
		t.Errorf("two decoders hold the tables %p and %p, want the one built once", first.tables, second.tables)
	}
}

func TestIMDCTMatchesTheDefinition(t *testing.T) {
	tab := sharedTables()
	for _, p := range []*imdctPlan{tab.longPlan, tab.shortPlan} {
		n := p.n
		in := make([]float32, n/2)
		seed := uint32(3)
		for k := range in {
			seed = seed*1664525 + 1013904223
			in[k] = float32(int32(seed)>>16) / 64
		}
		out := make([]float32, n)
		var tr imdct
		tr.run(p, in, out)
		n0 := float64(n)/4 + 0.5
		var errSum, sig float64
		for i := range n {
			var want float64
			for k, x := range in {
				want += float64(x) * math.Cos(2*math.Pi/float64(n)*(float64(i)+n0)*(float64(k)+0.5))
			}
			want *= 2 / float64(n)
			d := float64(out[i]) - want
			errSum += d * d
			sig += want * want
		}
		if snr := 10 * math.Log10(sig/errSum); snr < 100 {
			t.Errorf("%d-point IMDCT: SNR %.1f dB against the definition, want at least 100", n, snr)
		}
	}
}

func TestParseConfig(t *testing.T) {
	for _, c := range []struct {
		name     string
		asc      []byte
		rate     int
		channels int
		want     error
	}{
		{"LC 44.1 kHz stereo", []byte{0x12, 0x10}, 44100, 2, nil},
		{"LC 48 kHz 5.1", []byte{0x11, 0xb0}, 48000, 6, nil},
		{"LC 8 kHz mono", []byte{0x15, 0x88}, 8000, 1, nil},
		{"LC 48 kHz 7.1", []byte{0x11, 0xb8}, 48000, 8, nil},
		{"HE-AAC explicit", []byte{0x2b, 0x92, 0x08, 0x00}, 0, 0, ErrUnsupported},
		{"HE-AAC v2", []byte{0xeb, 0x09, 0x88, 0x00}, 0, 0, ErrUnsupported},
		{"LC with SBR extension", []byte{0x13, 0x10, 0x56, 0xe5, 0x98}, 0, 0, ErrUnsupported},
		{"AAC Main", []byte{0x0a, 0x10}, 0, 0, ErrUnsupported},
		{"960-sample frames", []byte{0x12, 0x14}, 0, 0, ErrUnsupported},
		{"program config element", []byte{0x12, 0x00}, 0, 0, ErrUnsupported},
		{"truncated", []byte{0x12}, 0, 0, ErrInvalid},
	} {
		cfg, err := ParseConfig(c.asc)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: error %v, want %v", c.name, err, c.want)
			continue
		}
		if err == nil && (cfg.SampleRate != c.rate || cfg.Channels != c.channels) {
			t.Errorf("%s: %d Hz, %d channels, want %d Hz, %d", c.name, cfg.SampleRate, cfg.Channels, c.rate, c.channels)
		}
	}
	if _, err := ParseConfig([]byte{0x2b, 0x92, 0x08, 0x00}); err == nil || !bytes.Contains([]byte(err.Error()), []byte("SBR")) {
		t.Errorf("HE-AAC error %v does not name SBR", err)
	}
}

func requireFFmpeg(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
}

func runFFmpeg(t testing.TB, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg %v: %v: %s", args, err, stderr.Bytes())
	}
	return out
}

// readTrack reads the track, packets and first timestamp of a file's
// first track; the timestamp is negative by the encoder's priming.
func readTrack(t testing.TB, path string) (demux.Track, [][]byte, time.Duration) {
	t.Helper()
	f, err := demux.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var packets [][]byte
	var first time.Duration
	for {
		p, err := f.ReadPacket()
		if errors.Is(err, io.EOF) {
			return f.Tracks()[0], packets, first
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(packets) == 0 {
			first = p.PTS
		}
		packets = append(packets, bytes.Clone(p.Data))
	}
}

func ffmpegDecode(t testing.TB, path string) []int16 {
	t.Helper()
	b := runFFmpeg(t, "-i", path, "-f", "f32le", "-")
	s := make([]int16, len(b)/4)
	for i := range s {
		s[i] = pcm.Int16(32768 * math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:])))
	}
	return s
}

func snr(want, got []int16) float64 {
	var sig, errSum float64
	for i, w := range want {
		d := float64(got[i]) - float64(w)
		sig += float64(w) * float64(w)
		errSum += d * d
	}
	return 10 * math.Log10(sig/max(errSum, 1e-9))
}

func TestDecodeMatchesFFmpeg(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	for _, c := range []struct {
		name     string
		channels int
		rate     int
		bitrate  string
		source   string
		args     []string
		minSNR   float64
	}{
		{"stereo 44.1 kHz", 2, 44100, "128k", "sine=frequency=440:duration=3,aeval=val(0)|0.6*sin(2*PI*660*t)", nil, 90},
		{"mono 48 kHz noise", 1, 48000, "96k", "anoisesrc=d=3:c=pink:a=0.3:seed=7", nil, 90},
		{"stereo transients", 2, 48000, "128k", "aevalsrc=exprs='if(lt(mod(t\\,0.25)\\,0.02)\\,0.8*random(0)\\,0.01*sin(2*PI*300*t))|if(lt(mod(t+0.1\\,0.3)\\,0.02)\\,0.6*random(1)\\,0.01*sin(2*PI*500*t))':d=3", nil, 90},
		{"stereo low rate", 2, 22050, "32k", "anoisesrc=d=3:c=pink:a=0.3:seed=3", nil, 90},
		{"mono 8 kHz", 1, 8000, "16k", "sine=frequency=600:duration=3", nil, 90},
		{"mid/side and intensity", 2, 44100, "48k", "anoisesrc=d=3:c=pink:a=0.3:seed=5,aeval=val(0)|0.9*val(0)", []string{"-aac_is", "1", "-aac_ms", "1"}, 90},
		{"5.1", 6, 48000, "320k", "aevalsrc=exprs='0.4*sin(2*PI*220*t)|0.3*sin(2*PI*330*t)|0.3*random(0)-0.15|0.2*sin(2*PI*55*t)|0.25*sin(2*PI*550*t)|0.2*sin(2*PI*660*t)*sin(2*PI*2*t)':c=5.1:d=3", nil, 90},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, "a.m4a")
			args := []string{
				"-f", "lavfi", "-i", c.source, "-ac", strconv.Itoa(c.channels), "-ar", strconv.Itoa(c.rate),
				"-c:a", "aac", "-aac_pns", "0", "-b:a", c.bitrate,
			}
			runFFmpeg(t, append(append(args, c.args...), path)...)
			track, packets, first := readTrack(t, path)
			d, err := NewDecoder(track.Config)
			if err != nil {
				t.Fatal(err)
			}
			if d.Config().Channels != c.channels || d.Config().SampleRate != c.rate {
				t.Fatalf("config %+v, want %d channels at %d Hz", d.Config(), c.channels, c.rate)
			}
			var got []int16
			for i, p := range packets {
				if got, err = d.Decode(got, p); err != nil {
					t.Fatalf("packet %d: %v", i, err)
				}
			}
			skip := int((-first*time.Duration(c.rate) + time.Second/2) / time.Second)
			got = got[skip*c.channels:]
			want := ffmpegDecode(t, path)
			if c.channels == 6 {
				got = toFFmpegOrder51(got)
			}
			n := min(len(got), len(want))
			if n < c.rate*2*c.channels {
				t.Fatalf("decoded %d samples, ffmpeg %d", len(got), len(want))
			}
			if s := snr(want[:n], got[:n]); s < c.minSNR {
				t.Errorf("SNR against ffmpeg %.1f dB, want at least %.0f", s, c.minSNR)
			}
		})
	}
}

// toFFmpegOrder51 reorders 5.1 from AAC's C, L, R, Ls, Rs, LFE to
// ffmpeg's L, R, C, LFE, Ls, Rs.
func toFFmpegOrder51(s []int16) []int16 {
	out := make([]int16, len(s))
	from := [6]int{1, 2, 0, 5, 3, 4}
	for i := 0; i+6 <= len(s); i += 6 {
		for c, f := range from {
			out[i+c] = s[i+f]
		}
	}
	return out
}

func TestDecodeNoiseSubstitutionKeepsTheEnergy(t *testing.T) {
	requireFFmpeg(t)
	path := filepath.Join(t.TempDir(), "a.m4a")
	runFFmpeg(t, "-f", "lavfi", "-i", "anoisesrc=d=3:c=white:a=0.2:seed=9", "-ac", "2", "-ar", "44100",
		"-c:a", "aac", "-aac_pns", "1", "-b:a", "40k", path)
	track, packets, _ := readTrack(t, path)
	d, err := NewDecoder(track.Config)
	if err != nil {
		t.Fatal(err)
	}
	var got []int16
	for _, p := range packets {
		if got, err = d.Decode(got, p); err != nil {
			t.Fatal(err)
		}
	}
	want := ffmpegDecode(t, path)
	energy := func(s []int16) float64 {
		var e float64
		for _, v := range s[len(s)/4 : 3*len(s)/4] {
			e += float64(v) * float64(v)
		}
		return e
	}
	n := min(len(got), len(want))
	if ratio := energy(got[:n]) / energy(want[:n]); ratio < 0.9 || ratio > 1.1 {
		t.Errorf("energy %.3f of ffmpeg's, want within 10 %%", ratio)
	}
}

func TestDecodeRejects(t *testing.T) {
	d, err := NewDecoder([]byte{0x12, 0x10})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		packet []byte
		want   error
	}{
		{"empty", nil, ErrInvalid},
		{"no END", []byte{0x20}, ErrInvalid},
		{"SCE in a stereo configuration", []byte{0x00, 0x00, 0x00}, ErrInvalid},
		{"coupling channel", []byte{0x40}, ErrUnsupported},
		{"no channel elements", []byte{0xe0}, ErrInvalid},
	} {
		if _, err := d.Decode(nil, c.packet); !errors.Is(err, c.want) {
			t.Errorf("%s: error %v, want %v", c.name, err, c.want)
		}
	}
}

// ffmpegPackets encodes a stereo signal with ffmpeg and returns the
// decoder and packets.
func ffmpegPackets(t testing.TB) (*Decoder, [][]byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.m4a")
	runFFmpeg(t, "-f", "lavfi", "-i", "anoisesrc=d=2:c=pink:a=0.3:seed=1,aeval=val(0)|0.5*val(0)+0.1*sin(2*PI*440*t)",
		"-ac", "2", "-ar", "44100", "-c:a", "aac", "-b:a", "128k", path)
	track, packets, _ := readTrack(t, path)
	d, err := NewDecoder(track.Config)
	if err != nil {
		t.Fatal(err)
	}
	return d, packets
}

func TestDecodeAllocatesNothing(t *testing.T) {
	requireFFmpeg(t)
	if testbuild.Race {
		t.Skip("-race instrumentation allocates")
	}
	d, packets := ffmpegPackets(t)
	dst := make([]int16, 0, FrameSize*2)
	i := 0
	allocs := testing.AllocsPerRun(len(packets)-1, func() {
		if _, err := d.Decode(dst[:0], packets[i]); err != nil {
			t.Fatal(err)
		}
		i++
	})
	if allocs != 0 {
		t.Errorf("Decode allocates %.1f times per frame, want 0", allocs)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte{0x21, 0x10, 0x05, 0x00, 0xa0, 0x19, 0x33, 0x87, 0xc0, 0x00, 0x7e})
	f.Add([]byte{0x01, 0x40, 0x20, 0x07})
	seed := uint32(5)
	for range 16 {
		p := make([]byte, 64)
		for i := range p {
			seed = seed*1664525 + 1013904223
			p[i] = byte(seed >> 24)
		}
		p[0] = 0x20 | p[0]&0x0f
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, packet []byte) {
		for _, asc := range [][]byte{{0x12, 0x08}, {0x12, 0x10}, {0x11, 0xb0}} {
			d, err := NewDecoder(asc)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				pcm, err := d.Decode(nil, packet)
				if err != nil {
					continue
				}
				if len(pcm) != FrameSize*d.Config().Channels {
					t.Fatalf("%d samples from a %d-byte packet", len(pcm), len(packet))
				}
			}
		}
	})
}

func BenchmarkDecode(b *testing.B) {
	requireFFmpeg(b)
	d, packets := ffmpegPackets(b)
	dst := make([]int16, 0, FrameSize*2)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		if _, err := d.Decode(dst[:0], packets[i%len(packets)]); err != nil {
			b.Fatal(err)
		}
	}
}
