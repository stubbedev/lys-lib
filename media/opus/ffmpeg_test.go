package opus

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/media/demux"
	"github.com/stubbedev/lys-lib/media/mux"
	"github.com/stubbedev/lys-lib/testbuild"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
}

func runFFmpeg(t *testing.T, args ...string) []byte {
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

func samples(b []byte) []int16 {
	s := make([]int16, len(b)/2)
	for i := range s {
		s[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return s
}

// decodeFile decodes the first track of a Matroska file with Decoder.
func decodeFile(t *testing.T, path string) (*Decoder, []int16) {
	t.Helper()
	f, err := demux.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := NewDecoder(f.Tracks()[0].Config)
	if err != nil {
		t.Fatal(err)
	}
	var pcm []int16
	for {
		p, err := f.ReadPacket()
		if errors.Is(err, io.EOF) {
			return d, pcm
		}
		if err != nil {
			t.Fatal(err)
		}
		if pcm, err = d.Decode(pcm, p.Data); err != nil {
			t.Fatalf("packet at %v: %v", p.PTS, err)
		}
	}
}

func TestDecodeMatchesLibopus(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	for _, c := range []struct {
		name     string
		channels int
		frame    string
		bitrate  string
		source   string
	}{
		{"stereo 20 ms", 2, "20", "128k", "sine=frequency=440:duration=2,aeval=val(0)|0.7*sin(2*PI*660*t)"},
		{"mono 20 ms", 1, "20", "64k", "sine=frequency=1000:duration=2"},
		{"stereo 10 ms", 2, "10", "96k", "anoisesrc=d=2:c=pink:a=0.3:seed=11"},
		{"stereo 5 ms", 2, "5", "160k", "anoisesrc=d=2:c=white:a=0.1:seed=11"},
		{"mono 2.5 ms", 1, "2.5", "64k", "sine=frequency=3000:duration=2"},
		{"stereo 60 ms", 2, "60", "48k", "anoisesrc=d=2:c=brown:a=0.4:seed=11"},
		{"stereo low rate", 2, "20", "24k", "anoisesrc=d=2:c=pink:a=0.3:seed=11"},
		{"transients", 2, "20", "96k", "aevalsrc=exprs='if(lt(mod(t\\,0.25)\\,0.02)\\,0.8*random(0)\\,0.001*random(1))|if(lt(mod(t+0.1\\,0.3)\\,0.02)\\,0.6*random(2)\\,0)':d=2"},
		{"transients 10 ms mono", 1, "10", "64k", "aevalsrc=exprs='if(lt(mod(t\\,0.2)\\,0.01)\\,0.8*random(0)\\,0.002*random(1))':d=2"},
		{"wideband stereo", 2, "20", "12k", "anoisesrc=d=2:c=pink:a=0.3:seed=11"},
		{"soft clipping", 2, "20", "64k", "anoisesrc=d=2:c=pink:a=1:seed=3,volume=4"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, "a.mka")
			runFFmpeg(t, "-f", "lavfi", "-i", c.source, "-ac", strconv.Itoa(c.channels), "-ar", "48000",
				"-c:a", "libopus", "-application", "lowdelay", "-frame_duration", c.frame, "-b:a", c.bitrate, path)
			d, got := decodeFile(t, path)
			got = got[d.PreSkip()*c.channels:]
			want := samples(runFFmpeg(t, "-c:a", "libopus", "-i", path, "-f", "s16le", "-ac", strconv.Itoa(c.channels), "-"))
			n := min(len(got), len(want))
			if n < len(want)-2*FrameSize*c.channels || n < 48000*c.channels {
				t.Fatalf("decoded %d samples, libopus %d", len(got), len(want))
			}
			if s := snr(want[:n], got[:n], c.channels, 0); s < 90 {
				t.Errorf("SNR against libopus %.1f dB, want at least 90 (within an LSB)", s)
			}
		})
	}
}

// packetModes counts the coding modes of a Matroska file's first track
// by each packet's TOC, and its packets of discontinuous transmission.
func packetModes(t *testing.T, path string) (modes map[opusMode]int, dtx int) {
	t.Helper()
	f, err := demux.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	modes = map[opusMode]int{}
	for {
		p, err := f.ReadPacket()
		if errors.Is(err, io.EOF) {
			return modes, dtx
		}
		if err != nil {
			t.Fatal(err)
		}
		modes[parseTOC(p.Data[0]).mode]++
		if len(p.Data) <= 2 {
			dtx++
		}
	}
}

func TestDecodeSILKAndHybridMatchLibopus(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	speech := "aevalsrc=exprs='0.5*sin(2*PI*(140+40*sin(2*PI*3*t))*t)*(0.6+0.4*sin(2*PI*4*t))*gt(sin(2*PI*0.7*t)\\,-0.6)+0.05*random(0)':d=3"
	speechStereo := "aevalsrc=exprs='0.5*sin(2*PI*(140+40*sin(2*PI*3*t))*t)*(0.6+0.4*sin(2*PI*4*t))+0.05*random(0)|0.4*sin(2*PI*(220+30*sin(2*PI*2*t))*t)*gt(sin(2*PI*0.5*t)\\,-0.3)+0.03*random(1)':d=3"
	for _, c := range []struct {
		name     string
		channels int
		app      string
		frame    string
		bitrate  string
		args     []string
		source   string
		want     opusMode
	}{
		{"SILK narrowband 20 ms", 1, "voip", "20", "8k", nil, speech, modeSILK},
		{"SILK mediumband 10 ms", 1, "voip", "10", "11k", nil, speech, modeSILK},
		{"SILK wideband 40 ms", 1, "voip", "40", "16k", []string{"-cutoff", "8000"}, speech, modeSILK},
		{"SILK wideband 60 ms", 1, "voip", "60", "14k", []string{"-cutoff", "8000"}, speech, modeSILK},
		{"SILK stereo 20 ms", 2, "voip", "20", "20k", []string{"-cutoff", "8000"}, speechStereo, modeSILK},
		{"hybrid mono 20 ms", 1, "voip", "20", "32k", nil, speech, modeHybrid},
		{"hybrid mono 10 ms", 1, "audio", "10", "28k", nil, speech, modeHybrid},
		{"hybrid stereo 20 ms", 2, "voip", "20", "48k", nil, speechStereo, modeHybrid},
		{"mode switches", 2, "audio", "20", "40k", nil, speechStereo + ",volume='if(lt(mod(t\\,1)\\,0.5)\\,1\\,0.02)':eval=frame", modeNone},
		{"music and speech", 1, "audio", "20", "24k", nil, "sine=frequency=880:duration=1.5[a];" + speech + "[b];[a][b]concat=v=0:a=1", modeNone},
		{"SILK DTX", 1, "voip", "20", "12k", []string{"-dtx", "1", "-cutoff", "8000"}, speech + ",volume='if(lt(mod(t\\,1)\\,0.5)\\,1\\,0)':eval=frame", modeSILK},
		{"hybrid DTX stereo", 2, "voip", "20", "40k", []string{"-dtx", "1"}, speechStereo + ",volume='if(lt(mod(t\\,1)\\,0.5)\\,1\\,0)':eval=frame", modeHybrid},
		{"CELT DTX", 2, "audio", "20", "96k", []string{"-dtx", "1"}, "anoisesrc=d=3:c=pink:a=0.3:seed=5,volume='if(lt(mod(t\\,1)\\,0.5)\\,1\\,0)':eval=frame", modeCELT},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, "a.mka")
			args := []string{
				"-f", "lavfi", "-i", c.source, "-ac", strconv.Itoa(c.channels), "-ar", "48000",
				"-c:a", "libopus", "-application", c.app, "-frame_duration", c.frame, "-b:a", c.bitrate,
			}
			args = append(append(args, c.args...), path)
			runFFmpeg(t, args...)
			modes, dtx := packetModes(t, path)
			t.Logf("packets per mode (SILK, hybrid, CELT): %d, %d, %d; DTX: %d", modes[modeSILK], modes[modeHybrid], modes[modeCELT], dtx)
			if c.want != modeNone && modes[c.want] == 0 {
				t.Fatalf("libopus coded no packet in mode %d", c.want)
			}
			d, got := decodeFile(t, path)
			got = got[d.PreSkip()*c.channels:]
			want := samples(runFFmpeg(t, "-c:a", "libopus", "-i", path, "-f", "s16le", "-ac", strconv.Itoa(c.channels), "-"))
			n := min(len(got), len(want))
			if n < len(want)-3*FrameSize*c.channels || n < 48000*c.channels {
				t.Fatalf("decoded %d samples, libopus %d", len(got), len(want))
			}
			if s := snr(want[:n], got[:n], c.channels, 0); s < 90 {
				t.Errorf("SNR against libopus %.1f dB, want at least 90 (within an LSB)", s)
			}
		})
	}
}

// dropPackets rewrites the first track of a Matroska file into dst
// with every nth packet cut to its TOC byte, a lost frame the decoder
// conceals.
func dropPackets(t *testing.T, src, dst string, channels, n int) {
	t.Helper()
	f, err := demux.OpenFile(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	head := f.Tracks()[0].Config
	var buf bytes.Buffer
	m, err := mux.NewMKV(&buf, mux.Opus{Channels: uint8(channels), PreSkip: binary.LittleEndian.Uint16(head[10:])})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		p, err := f.ReadPacket()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data := p.Data
		if i > 2 && i%n == 0 {
			data = data[:1]
		}
		if err := m.WriteSample(mux.Sample{PTS: time.Duration(i) * 20 * time.Millisecond, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeConcealsLikeLibopus(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	speech := "aevalsrc=exprs='0.5*sin(2*PI*(140+40*sin(2*PI*3*t))*t)*(0.6+0.4*sin(2*PI*4*t))+0.02*random(0)':d=3"
	for _, c := range []struct {
		name     string
		channels int
		app      string
		bitrate  string
		source   string
		minSNR   float64
	}{
		{"SILK", 1, "voip", "12k", speech, 90},
		{"hybrid", 2, "voip", "40k", speech, 90},
		{"CELT tone", 1, "lowdelay", "64k", "sine=frequency=330:duration=3", 70},
		{"CELT noise", 2, "lowdelay", "96k", "anoisesrc=d=3:c=pink:a=0.3:seed=5", 70},
	} {
		t.Run(c.name, func(t *testing.T) {
			src, path := filepath.Join(dir, "a.mka"), filepath.Join(dir, "lossy.mka")
			runFFmpeg(t, "-f", "lavfi", "-i", c.source, "-ac", strconv.Itoa(c.channels), "-ar", "48000",
				"-c:a", "libopus", "-application", c.app, "-b:a", c.bitrate, src)
			dropPackets(t, src, path, c.channels, 7)
			d, got := decodeFile(t, path)
			got = got[d.PreSkip()*c.channels:]
			want := samples(runFFmpeg(t, "-c:a", "libopus", "-i", path, "-f", "s16le", "-ac", strconv.Itoa(c.channels), "-"))
			n := min(len(got), len(want))
			if n < 48000*c.channels {
				t.Fatalf("decoded %d samples, libopus %d", len(got), len(want))
			}
			s := snr(want[:n], got[:n], c.channels, 0)
			t.Logf("SNR against libopus %.1f dB", s)
			if s < c.minSNR {
				t.Errorf("SNR against libopus %.1f dB, want at least %.0f", s, c.minSNR)
			}
		})
	}
}

func TestDecodeSurroundMatchesLibopus(t *testing.T) {
	requireFFmpeg(t)
	path := filepath.Join(t.TempDir(), "a.mka")
	runFFmpeg(t, "-f", "lavfi", "-i",
		"aevalsrc=exprs='0.4*sin(2*PI*220*t)|0.3*sin(2*PI*330*t)|0.3*random(0)-0.15|0.2*sin(2*PI*440*t)*sin(2*PI*2*t)|0.25*sin(2*PI*550*t)|0.4*sin(2*PI*60*t)':c=5.1:d=2",
		"-c:a", "libopus", "-b:a", "256k", path)
	d, got := decodeFile(t, path)
	if d.Channels() != 6 {
		t.Fatalf("%d channels, want 6", d.Channels())
	}
	got = got[d.PreSkip()*6:]
	want := samples(runFFmpeg(t, "-c:a", "libopus", "-i", path, "-f", "s16le", "-"))
	n := min(len(got), len(want)) / 6
	if n < 48000 {
		t.Fatalf("decoded %d samples, libopus %d", len(got), len(want))
	}
	vorbisOf := [6]int{0, 2, 1, 5, 3, 4}
	for c, v := range vorbisOf {
		w, g := make([]int16, n), make([]int16, n)
		for i := range n {
			w[i], g[i] = want[i*6+c], got[i*6+v]
		}
		if s := snr(w, g, 1, 0); s < 90 {
			t.Errorf("channel %d: SNR against libopus %.1f dB, want at least 90", c, s)
		}
	}
}

// filePackets reads the packets of a Matroska file's first track and
// its codec config.
func filePackets(t *testing.T, path string) (config []byte, packets [][]byte) {
	t.Helper()
	f, err := demux.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	config = f.Tracks()[0].Config
	for {
		p, err := f.ReadPacket()
		if errors.Is(err, io.EOF) {
			return config, packets
		}
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, bytes.Clone(p.Data))
	}
}

func TestDecodeAllocatesNothingInAnyMode(t *testing.T) {
	requireFFmpeg(t)
	if testbuild.Race {
		t.Skip("-race instrumentation allocates")
	}
	dir := t.TempDir()
	speech := "aevalsrc=exprs='0.5*sin(2*PI*(140+40*sin(2*PI*3*t))*t)*gt(sin(2*PI*0.7*t)\\,-0.6)+0.05*random(0)|0.3*sin(2*PI*200*t)':d=2"
	for _, c := range []struct {
		name string
		args []string
	}{
		{"SILK stereo DTX", []string{"-ac", "2", "-application", "voip", "-b:a", "16k", "-dtx", "1", "-cutoff", "8000"}},
		{"hybrid and CELT", []string{"-ac", "2", "-application", "audio", "-b:a", "40k"}},
		{"surround", []string{"-ac", "6", "-b:a", "192k"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, "a.mka")
			args := append([]string{"-f", "lavfi", "-i", speech, "-ar", "48000", "-c:a", "libopus"}, c.args...)
			runFFmpeg(t, append(args, path)...)
			config, packets := filePackets(t, path)
			d, err := NewDecoder(config)
			if err != nil {
				t.Fatal(err)
			}
			dst := make([]int16, 0, maxPacketDur*d.Channels())
			for _, p := range packets[:10] {
				if _, err := d.Decode(dst, p); err != nil {
					t.Fatal(err)
				}
			}
			i := 10
			allocs := testing.AllocsPerRun(len(packets)-11, func() {
				if _, err := d.Decode(dst[:0], packets[i]); err != nil {
					t.Fatal(err)
				}
				i++
			})
			if allocs != 0 {
				t.Errorf("Decode allocates %.1f times per packet, want 0", allocs)
			}
		})
	}
}

func BenchmarkDecodeModes(b *testing.B) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		b.Skip("ffmpeg not installed")
	}
	speech := "aevalsrc=exprs='0.5*sin(2*PI*(140+40*sin(2*PI*3*t))*t)+0.05*random(0)|0.3*sin(2*PI*200*t)+0.02*random(1)':d=2"
	for _, c := range []struct {
		name string
		args []string
	}{
		{"SILK wideband stereo", []string{"-application", "voip", "-b:a", "24k", "-cutoff", "8000"}},
		{"hybrid stereo", []string{"-application", "voip", "-b:a", "48k"}},
	} {
		b.Run(c.name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "a.mka")
			args := append([]string{"-v", "error", "-f", "lavfi", "-i", speech, "-ar", "48000", "-ac", "2", "-c:a", "libopus"}, c.args...)
			if out, err := exec.Command("ffmpeg", append(args, path)...).CombinedOutput(); err != nil {
				b.Fatalf("ffmpeg: %v: %s", err, out)
			}
			f, err := demux.OpenFile(path)
			if err != nil {
				b.Fatal(err)
			}
			defer f.Close()
			d, err := NewDecoder(f.Tracks()[0].Config)
			if err != nil {
				b.Fatal(err)
			}
			var packets [][]byte
			for {
				p, err := f.ReadPacket()
				if err != nil {
					break
				}
				packets = append(packets, bytes.Clone(p.Data))
			}
			dst := make([]int16, 0, maxPacketDur*2)
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				if _, err := d.Decode(dst[:0], packets[i%len(packets)]); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
