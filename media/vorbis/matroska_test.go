package vorbis

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stubbedev/lys-lib/media/demux"
)

func ffmpeg(t *testing.T, args ...string) []byte {
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

func TestDecoderDecodesMatroskaLikeFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := filepath.Join(t.TempDir(), "v.mka")
	ffmpeg(t, "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-ac", "2", "-c:a", "libvorbis", path)
	f, err := demux.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	id, comment, setup, err := demux.XiphHeaders(f.Tracks()[0].Config)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDecoder(id, comment, setup)
	if err != nil {
		t.Fatal(err)
	}
	var pcm []int16
	for {
		p, err := f.ReadPacket()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if pcm, err = d.Decode(pcm, p.Data); err != nil {
			t.Fatal(err)
		}
	}
	raw := ffmpeg(t, "-i", path, "-f", "s16le", "-ac", "2", "-")
	want := make([]int16, len(raw)/2)
	for i := range want {
		want[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	if len(pcm) < len(want) || len(want) < 2*44100 {
		t.Fatalf("decoded %d samples, ffmpeg %d", len(pcm), len(want))
	}
	for i, w := range want {
		if d := int(pcm[i]) - int(w); d > 2 || d < -2 {
			t.Fatalf("sample %d: %d, ffmpeg %d", i, pcm[i], w)
		}
	}
}
