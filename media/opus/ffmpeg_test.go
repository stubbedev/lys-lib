package opus

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stubbedev/lys-lib/media/demux"
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
