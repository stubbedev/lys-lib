package mux

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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

func ffmpegOutput(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, append(args, "-")...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg %v: %v: %s", args, err, stderr.Bytes())
	}
	return out
}

const (
	clipW, clipH = 318, 238
	clipFrames   = 60
	clipGOP      = 15
)

var videoEncoders = map[codec][]string{
	codecH264: {"-c:v", "libx264", "-bf", "0", "-g", strconv.Itoa(clipGOP), "-bsf:v", "h264_metadata=aud=insert", "-f", "h264"},
	codecH265: {"-c:v", "libx265", "-x265-params", "bframes=0:keyint=" + strconv.Itoa(clipGOP) + ":log-level=error", "-bsf:v", "hevc_metadata=aud=insert", "-f", "hevc"},
}

func encodeVideo(t *testing.T, c codec) [][]byte {
	t.Helper()
	src := []string{"-f", "lavfi", "-i", "testsrc2=s=" + strconv.Itoa(clipW) + "x" + strconv.Itoa(clipH) + ":r=60", "-frames:v", strconv.Itoa(clipFrames), "-pix_fmt", "yuv420p"}
	stream := ffmpegOutput(t, append(src, videoEncoders[c]...)...)
	aud := []byte{0, 0, 0, 1, 0x09}
	if c == codecH265 {
		aud = []byte{0, 0, 0, 1, nalH265AUD << 1}
	}
	var units [][]byte
	for len(stream) > 0 {
		next := bytes.Index(stream[1:], aud)
		if next < 0 {
			units = append(units, stream)
			break
		}
		units = append(units, stream[:next+1])
		stream = stream[next+1:]
	}
	if len(units) != clipFrames {
		t.Fatalf("split %d access units, want %d", len(units), clipFrames)
	}
	return units
}

func videoTrack(t *testing.T, c codec, first []byte) Track {
	t.Helper()
	sets := map[uint8][]byte{}
	err := annexB(first, func(nal []byte) error {
		typ := nalTypeH264(nal)
		if c == codecH265 {
			typ = nalTypeH265(nal)
		}
		sets[typ] = slices.Clone(nal)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if c == codecH265 {
		return H265{VPS: sets[nalH265VPS], SPS: sets[nalH265SPS], PPS: sets[nalH265PPS]}
	}
	return H264{SPS: sets[nalH264SPS], PPS: sets[nalH264PPS]}
}

type oggOpus struct {
	channels uint8
	preSkip  uint16
	packets  [][]byte
}

func encodeOpus(t *testing.T) oggOpus {
	t.Helper()
	ogg := ffmpegOutput(t, "-f", "lavfi", "-i", "sine=f=440:d=2", "-ac", "2", "-c:a", "libopus", "-b:a", "64k", "-frame_duration", "20", "-f", "ogg")
	var packets [][]byte
	var cur []byte
	for len(ogg) > 0 {
		if len(ogg) < 27 || string(ogg[:4]) != "OggS" {
			t.Fatalf("ogg page header %q", ogg[:min(len(ogg), 27)])
		}
		segs := int(ogg[26])
		lacing := ogg[27 : 27+segs]
		body := ogg[27+segs:]
		for _, l := range lacing {
			cur = append(cur, body[:l]...)
			body = body[l:]
			if l < 255 {
				packets = append(packets, cur)
				cur = nil
			}
		}
		ogg = body
	}
	head := packets[0]
	if string(head[:8]) != "OpusHead" || string(packets[1][:8]) != "OpusTags" {
		t.Fatalf("ogg starts with %q, %q", head[:8], packets[1][:8])
	}
	return oggOpus{channels: head[9], preSkip: binary.LittleEndian.Uint16(head[10:]), packets: packets[2:]}
}

func videoPTS(i int) time.Duration {
	return time.Duration(i)*33*time.Millisecond + time.Duration(i%3)*2*time.Millisecond
}

const opusPacket = 20 * time.Millisecond

func interleave(video [][]byte, audio [][]byte) []Sample {
	var out []Sample
	for i, j := 0, 0; i < len(video) || j < len(audio); {
		if j == len(audio) || i < len(video) && videoPTS(i) <= time.Duration(j)*opusPacket {
			out = append(out, Sample{Track: 0, PTS: videoPTS(i), Data: video[i]})
			i++
			continue
		}
		out = append(out, Sample{Track: 1, PTS: time.Duration(j) * opusPacket, Data: audio[j]})
		j++
	}
	return out
}

type muxer interface {
	WriteSample(Sample) error
	Close() error
}

type onlyWriter struct{ io.Writer }

type probe struct {
	Streams []struct {
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Channels  int    `json:"channels"`
		Packets   string `json:"nb_read_packets"`
		Duration  string `json:"duration"`
	} `json:"streams"`
	Packets []struct {
		Stream int    `json:"stream_index"`
		PTS    string `json:"pts_time"`
		Flags  string `json:"flags"`
	} `json:"packets"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func ffprobe(t *testing.T, path string) probe {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-count_packets", "-show_streams", "-show_packets", "-show_format", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var p probe
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	return p
}

func decodes(t *testing.T, path string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil || len(out) > 0 {
		t.Errorf("ffmpeg decode %s: %v: %s", path, err, out)
	}
}

func seconds(t *testing.T, s string) time.Duration {
	t.Helper()
	d, err := time.ParseDuration(s + "s")
	if err != nil {
		t.Fatalf("ffprobe time %q: %v", s, err)
	}
	return d
}

func near(a, b, tol time.Duration) bool { return a-b <= tol && b-a <= tol }

func TestMuxedFilesProbeAndDecode(t *testing.T) {
	requireFFmpeg(t)
	opus := encodeOpus(t)
	audioEnd := time.Duration(len(opus.packets)) * opusPacket
	videoEnd := videoPTS(clipFrames-1) + videoPTS(clipFrames-1) - videoPTS(clipFrames-2)
	for _, c := range []struct {
		name  string
		codec codec
		probe string
	}{{"h264", codecH264, "h264"}, {"h265", codecH265, "hevc"}} {
		units := encodeVideo(t, c.codec)
		samples := interleave(units, opus.packets)
		tracks := []Track{videoTrack(t, c.codec, units[0]), Opus{Channels: opus.channels, PreSkip: opus.preSkip}}
		for _, f := range []struct {
			name string
			open func(io.Writer) (muxer, error)
			wrap func(*os.File) io.Writer
			tol  time.Duration
		}{
			{"mp4", func(w io.Writer) (muxer, error) { return NewMP4(w, tracks...) }, func(f *os.File) io.Writer { return onlyWriter{f} }, 0},
			{"mkv", func(w io.Writer) (muxer, error) { return NewMKV(w, tracks...) }, func(f *os.File) io.Writer { return f }, time.Millisecond},
			{"mkv-stream", func(w io.Writer) (muxer, error) { return NewMKV(w, tracks...) }, func(f *os.File) io.Writer { return onlyWriter{f} }, time.Millisecond},
		} {
			t.Run(c.name+"/"+f.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "out."+f.name[:3])
				file, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				m, err := f.open(f.wrap(file))
				if err != nil {
					t.Fatal(err)
				}
				for _, s := range samples {
					if err := m.WriteSample(s); err != nil {
						t.Fatal(err)
					}
				}
				crashed := filepath.Join(t.TempDir(), "crashed."+f.name[:3])
				partial, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(crashed, partial, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}

				p := ffprobe(t, path)
				if len(p.Streams) != 2 {
					t.Fatalf("%d streams", len(p.Streams))
				}
				v, a := p.Streams[0], p.Streams[1]
				if v.CodecName != c.probe || v.Width != clipW || v.Height != clipH || v.Packets != strconv.Itoa(clipFrames) {
					t.Errorf("video stream %+v", v)
				}
				if a.CodecName != "opus" || a.Channels != int(opus.channels) || a.Packets != strconv.Itoa(len(opus.packets)) {
					t.Errorf("audio stream %+v", a)
				}
				var vi int
				for _, pk := range p.Packets {
					if pk.Stream != 0 {
						continue
					}
					if got := seconds(t, pk.PTS); !near(got, videoPTS(vi), f.tol+time.Microsecond) {
						t.Errorf("video packet %d at %v, want %v", vi, got, videoPTS(vi))
					}
					if key := pk.Flags[0] == 'K'; key != (vi%clipGOP == 0) {
						t.Errorf("video packet %d flags %q", vi, pk.Flags)
					}
					vi++
				}
				if f.name != "mkv-stream" {
					want := max(videoEnd, audioEnd)
					if got := seconds(t, p.Format.Duration); !near(got, want, opusPacket) {
						t.Errorf("duration %v, want %v", got, want)
					}
				}
				if f.name == "mp4" {
					if got := seconds(t, v.Duration); !near(got, videoEnd, time.Microsecond) {
						t.Errorf("video duration %v, want %v", got, videoEnd)
					}
				}
				decodes(t, path)

				cp := ffprobe(t, crashed)
				if len(cp.Packets) == 0 || len(cp.Packets) >= len(samples) {
					t.Errorf("file cut before Close has %d packets of %d", len(cp.Packets), len(samples))
				}
				decodes(t, crashed)
			})
		}
	}
}
