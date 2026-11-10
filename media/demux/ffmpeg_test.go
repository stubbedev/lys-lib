package demux

import (
	"bytes"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
}

// ffmpegFile encodes two seconds of test pattern and tone with args
// into dir/name; with pipe set ffmpeg writes to a pipe, so the muxer
// cannot seek back to write sizes or an index.
func ffmpegFile(t *testing.T, dir, name string, pipe bool, args ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	full := []string{
		"-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=25:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
	}
	full = append(full, args...)
	out := path
	if pipe {
		out = "pipe:1"
	}
	cmd := exec.Command("ffmpeg", append(full, out)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if pipe {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		cmd.Stdout = f
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg %v: %v: %s", args, err, stderr.Bytes())
	}
	return path
}

type probed struct {
	pts  time.Duration
	size int
	key  bool
	crc  uint32
}

func ffprobePackets(t *testing.T, path string) map[int][]probed {
	t.Helper()
	cmd := exec.Command("ffprobe", "-v", "error", "-of", "json", "-show_data_hash", "CRC32",
		"-show_entries", "packet=stream_index,pts_time,size,flags,data_hash", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v: %s", path, err, stderr.Bytes())
	}
	var r struct {
		Packets []struct {
			Stream int    `json:"stream_index"`
			PTS    string `json:"pts_time"`
			Size   string `json:"size"`
			Flags  string `json:"flags"`
			Hash   string `json:"data_hash"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	got := map[int][]probed{}
	for _, p := range r.Packets {
		secs, err := strconv.ParseFloat(p.PTS, 64)
		if err != nil {
			t.Fatalf("ffprobe pts %q: %v", p.PTS, err)
		}
		size, err := strconv.Atoi(p.Size)
		if err != nil {
			t.Fatal(err)
		}
		crc, err := strconv.ParseUint(strings.TrimPrefix(p.Hash, "CRC32:"), 16, 32)
		if err != nil {
			t.Fatalf("ffprobe hash %q: %v", p.Hash, err)
		}
		got[p.Stream] = append(got[p.Stream], probed{
			pts: time.Duration(secs * float64(time.Second)), size: size,
			key: strings.HasPrefix(p.Flags, "K"), crc: uint32(crc),
		})
	}
	return got
}

var ffmpegCases = []struct {
	name   string
	file   string
	pipe   bool
	args   []string
	format Format
	codecs [2]Codec
}{
	{"mp4 with B-frames, moov last", "a.mp4", false, []string{"-c:v", "libx264", "-bf", "2", "-g", "12", "-c:a", "aac"}, MP4, [2]Codec{H264, AAC}},
	{"mp4 faststart", "b.mp4", false, []string{"-c:v", "libx264", "-bf", "2", "-g", "12", "-c:a", "aac", "-movflags", "+faststart"}, MP4, [2]Codec{H264, AAC}},
	{"fragmented mp4", "c.mp4", false, []string{"-c:v", "libx265", "-x265-params", "log-level=none:keyint=12", "-c:a", "libopus", "-movflags", "frag_keyframe+empty_moov+default_base_moof"}, MP4, [2]Codec{H265, Opus}},
	{"mp4 av1 flac", "d.mp4", false, []string{"-c:v", "libsvtav1", "-preset", "12", "-g", "12", "-c:a", "flac", "-strict", "-2"}, MP4, [2]Codec{AV1, FLAC}},
	{"matroska with B-frames", "e.mkv", false, []string{"-c:v", "libx264", "-bf", "2", "-g", "12", "-c:a", "libvorbis"}, Matroska, [2]Codec{H264, Vorbis}},
	{"webm through a pipe", "f.webm", true, []string{"-c:v", "libvpx-vp9", "-deadline", "realtime", "-cpu-used", "8", "-g", "12", "-c:a", "libopus", "-f", "webm"}, WebM, [2]Codec{VP9, Opus}},
}

func TestPacketsMatchFFprobe(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	for _, c := range ffmpegCases {
		t.Run(c.name, func(t *testing.T) {
			path := ffmpegFile(t, dir, c.file, c.pipe, c.args...)
			want := ffprobePackets(t, path)
			f, err := OpenFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if f.Format() != c.format {
				t.Errorf("format %v, want %v", f.Format(), c.format)
			}
			tr := f.Tracks()
			if len(tr) != 2 || tr[0].Codec != c.codecs[0] || tr[1].Codec != c.codecs[1] {
				t.Fatalf("tracks %+v, want codecs %v", tr, c.codecs)
			}
			if tr[0].Width != 160 || tr[0].Height != 120 || tr[1].SampleRate == 0 || tr[1].Channels != 1 {
				t.Errorf("tracks %+v", tr)
			}
			got := map[int][]probed{}
			for {
				p, err := f.ReadPacket()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				got[p.Track] = append(got[p.Track], probed{pts: p.PTS, size: len(p.Data), key: p.Key, crc: crc32.ChecksumIEEE(p.Data)})
			}
			for s := range 2 {
				if len(got[s]) != len(want[s]) {
					t.Fatalf("track %d: %d packets, ffprobe %d", s, len(got[s]), len(want[s]))
				}
				for i, g := range got[s] {
					w := want[s][i]
					if d := g.pts - w.pts; d > time.Millisecond || d < -time.Millisecond || g.size != w.size || g.crc != w.crc || (s == 0 && g.key != w.key) {
						t.Errorf("track %d packet %d: %+v, ffprobe %+v", s, i, g, w)
					}
				}
			}
			checkSeeks(t, f, want[0])
		})
	}
}

func checkSeeks(t *testing.T, f *File, video []probed) {
	t.Helper()
	for _, target := range []time.Duration{1300 * time.Millisecond, 500 * time.Millisecond, 0, 1900 * time.Millisecond} {
		var key *probed
		for i := range video {
			if video[i].key && video[i].pts <= target && (key == nil || video[i].pts > key.pts) {
				key = &video[i]
			}
		}
		for i := 0; key == nil; i++ {
			if video[i].key {
				key = &video[i]
			}
		}
		at, err := f.Seek(target)
		if err != nil {
			t.Fatal(err)
		}
		if d := at - key.pts; d > time.Millisecond || d < -time.Millisecond {
			t.Errorf("seek to %v landed at %v, the keyframe before is at %v", target, at, key.pts)
		}
		for {
			p, err := f.ReadPacket()
			if err != nil {
				t.Fatalf("after seek to %v: %v", target, err)
			}
			if p.Track != 0 {
				continue
			}
			if !p.Key || crc32.ChecksumIEEE(p.Data) != key.crc {
				t.Errorf("seek to %v: first video packet at %v (key %v) is not the keyframe at %v", target, p.PTS, p.Key, key.pts)
			}
			break
		}
	}
}
