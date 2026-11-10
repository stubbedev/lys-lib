package demux

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/media/mux"
)

const (
	testSPS    = "67640015acd94141feab0110000003001000000780f162d960"
	testPPS    = "68ebe3cb22c0"
	testW      = 318
	testH      = 238
	frameStep  = 40 * time.Millisecond
	opusStep   = 20 * time.Millisecond
	testFrames = 75
	keyEvery   = 10
	preSkip    = 312
	preSkipDur = 6500 * time.Microsecond
)

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// clip is what a muxed test file holds: per track, each sample's PTS,
// key flag and the payload the demuxer must return.
type clip struct {
	video, audio []want
}

type want struct {
	pts  time.Duration
	key  bool
	data []byte
}

func videoNAL(i int) []byte {
	typ := byte(0x41)
	if i%keyEvery == 0 {
		typ = 0x65
	}
	return append([]byte{typ, 0x88, byte(i)}, bytes.Repeat([]byte{byte(i), 0x5a}, 20+i%7)...)
}

func opusPacket(i int) []byte {
	return append([]byte{0xfc, byte(i)}, bytes.Repeat([]byte{byte(i * 3)}, 10+i%5)...)
}

// muxClip writes the test clip with newMuxer into a temporary file and
// returns its path and what it holds.
func muxClip(t testing.TB, ext string) (string, clip) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clip"+ext)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tracks := []mux.Track{mux.H264{SPS: mustHex(t, testSPS), PPS: mustHex(t, testPPS)}, mux.Opus{Channels: 2, PreSkip: preSkip}}
	var m interface {
		WriteSample(mux.Sample) error
		Close() error
	}
	if ext == ".mp4" {
		m, err = mux.NewMP4(f, tracks...)
	} else {
		m, err = mux.NewMKV(f, tracks...)
	}
	if err != nil {
		t.Fatal(err)
	}
	var c clip
	audioN := int(testFrames * frameStep / opusStep)
	vi, ai := 0, 0
	for vi < testFrames || ai < audioN {
		vt, at := time.Duration(vi)*frameStep, time.Duration(ai)*opusStep
		if vi < testFrames && (ai >= audioN || vt <= at) {
			nal := videoNAL(vi)
			if err := m.WriteSample(mux.Sample{Track: 0, PTS: vt, Data: append([]byte{0, 0, 0, 1}, nal...)}); err != nil {
				t.Fatal(err)
			}
			c.video = append(c.video, want{pts: vt, key: vi%keyEvery == 0, data: binary.BigEndian.AppendUint32(nil, uint32(len(nal)))})
			c.video[vi].data = append(c.video[vi].data, nal...)
			vi++
			continue
		}
		p := opusPacket(ai)
		if err := m.WriteSample(mux.Sample{Track: 1, PTS: at, Data: p}); err != nil {
			t.Fatal(err)
		}
		c.audio = append(c.audio, want{pts: at - preSkipDur, key: true, data: p})
		ai++
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return path, c
}

func readAll(t *testing.T, f *File) [2][]Packet {
	t.Helper()
	var got [2][]Packet
	for {
		p, err := f.ReadPacket()
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Fatal(err)
		}
		p.Data = bytes.Clone(p.Data)
		got[p.Track] = append(got[p.Track], p)
	}
}

func checkPackets(t *testing.T, track string, got []Packet, want []want, tol time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d packets, want %d", track, len(got), len(want))
	}
	for i, p := range got {
		w := want[i]
		if d := p.PTS - w.pts; d > tol || d < -tol {
			t.Errorf("%s packet %d: PTS %v, want %v", track, i, p.PTS, w.pts)
		}
		if p.Key != w.key {
			t.Errorf("%s packet %d: key %v, want %v", track, i, p.Key, w.key)
		}
		if !bytes.Equal(p.Data, w.data) {
			t.Errorf("%s packet %d: data % x, want % x", track, i, p.Data, w.data)
		}
	}
}

func TestReadsWhatTheMuxerWrote(t *testing.T) {
	for _, c := range []struct {
		ext    string
		format Format
		tol    time.Duration
	}{
		{".mp4", MP4, 0},
		{".mkv", Matroska, 500 * time.Microsecond},
	} {
		t.Run(c.format.String(), func(t *testing.T) {
			path, want := muxClip(t, c.ext)
			f, err := OpenFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if f.Format() != c.format {
				t.Errorf("format %v, want %v", f.Format(), c.format)
			}
			tr := f.Tracks()
			if len(tr) != 2 {
				t.Fatalf("%d tracks, want 2", len(tr))
			}
			v, a := tr[0], tr[1]
			if v.Kind != Video || v.Codec != H264 || v.Width != testW || v.Height != testH || len(v.Config) == 0 || v.Config[0] != 1 || v.CodecDelay != 0 {
				t.Errorf("video track %+v", v)
			}
			if a.Kind != Audio || a.Codec != Opus || a.Channels != 2 || a.SampleRate != opusRate || a.SeekPreRoll != opusPreRoll || a.CodecDelay != preSkipDur {
				t.Errorf("audio track %+v", a)
			}
			if !bytes.HasPrefix(a.Config, []byte("OpusHead")) || binary.LittleEndian.Uint16(a.Config[10:]) != preSkip {
				t.Errorf("audio config % x is not the OpusHead", a.Config)
			}
			if d := f.Duration(); d != testFrames*frameStep {
				t.Errorf("duration %v, want %v", d, testFrames*frameStep)
			}
			got := readAll(t, f)
			checkPackets(t, "video", got[0], want.video, c.tol)
			checkPackets(t, "audio", got[1], want.audio, c.tol)
		})
	}
}

func TestSeekLandsOnTheKeyframeBefore(t *testing.T) {
	for _, ext := range []string{".mp4", ".mkv"} {
		t.Run(ext, func(t *testing.T) {
			path, want := muxClip(t, ext)
			f, err := OpenFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			for _, target := range []time.Duration{1250 * time.Millisecond, 0, 2900 * time.Millisecond, 400 * time.Millisecond, -time.Second, time.Hour} {
				key := min(max(target, 0)/(keyEvery*frameStep), (testFrames-1)/keyEvery) * keyEvery
				wantAt := key * frameStep
				at, err := f.Seek(target)
				if err != nil {
					t.Fatal(err)
				}
				if at != wantAt {
					t.Errorf("seek to %v landed at %v, want %v", target, at, wantAt)
				}
				got := readAll(t, f)
				checkPackets(t, "video after seek", got[0], want.video[key:], time.Millisecond)
				if len(got[1]) == 0 {
					t.Fatalf("seek to %v: no audio after", target)
				}
				i := 0
				for want.audio[i].pts < wantAt-opusPreRoll {
					i++
				}
				checkPackets(t, "audio after seek", got[1], want.audio[i:], time.Millisecond)
			}
		})
	}
}

func TestSelectSkipsTheOtherTracks(t *testing.T) {
	for _, ext := range []string{".mp4", ".mkv"} {
		t.Run(ext, func(t *testing.T) {
			path, want := muxClip(t, ext)
			f, err := OpenFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := f.Select(1); err != nil {
				t.Fatal(err)
			}
			got := readAll(t, f)
			if len(got[0]) != 0 || len(got[1]) != len(want.audio) {
				t.Errorf("%d video and %d audio packets, want 0 and %d", len(got[0]), len(got[1]), len(want.audio))
			}
			if err := f.Select(2); err == nil {
				t.Error("selecting a track the file lacks succeeded")
			}
		})
	}
}

func TestUnsupportedNamesCodecAndFile(t *testing.T) {
	path, _ := muxClip(t, ".mkv")
	f, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = f.Unsupported(0)
	var u *UnsupportedCodecError
	if !errors.As(err, &u) || u.File != path || u.Track != 0 {
		t.Fatalf("error %v", err)
	}
	if want := "demux: " + path + ": track 0: codec H.264 (V_MPEG4/ISO/AVC) is not supported"; err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
}

func TestRejectsOtherFormats(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("RIFF\x00\x00\x00\x00WAVEfmt "), bytes.Repeat([]byte{0}, 64)} {
		if _, err := Open(bytes.NewReader(b), int64(len(b)), "x"); !errors.Is(err, ErrUnknownFormat) {
			t.Errorf("% x: error %v, want ErrUnknownFormat", b, err)
		}
	}
}

func TestXiphHeadersSplit(t *testing.T) {
	id, comment := bytes.Repeat([]byte{1}, 30), bytes.Repeat([]byte{3}, 300)
	cfg := append([]byte{2, 30, 255, 45}, id...)
	cfg = append(cfg, comment...)
	cfg = append(cfg, 5, 5, 5)
	a, b, c, err := XiphHeaders(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, id) || !bytes.Equal(b, comment) || !bytes.Equal(c, []byte{5, 5, 5}) {
		t.Errorf("split into %d, %d and %d bytes", len(a), len(b), len(c))
	}
	if _, _, _, err := XiphHeaders(cfg[:40]); !errors.Is(err, ErrMalformed) {
		t.Errorf("truncated headers: error %v", err)
	}
}

func FuzzOpen(f *testing.F) {
	for _, ext := range []string{".mp4", ".mkv"} {
		path, _ := muxClip(f, ext)
		b, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
		f.Add(b[:len(b)/2])
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		file, err := Open(bytes.NewReader(b), int64(len(b)), "fuzz")
		if err != nil {
			return
		}
		for range 1000 {
			if _, err := file.ReadPacket(); err != nil {
				break
			}
		}
		if _, err := file.Seek(time.Second); err != nil {
			return
		}
		for range 100 {
			if _, err := file.ReadPacket(); err != nil {
				break
			}
		}
	})
}

func TestLacingSplitsFrames(t *testing.T) {
	for _, c := range []struct {
		name  string
		flags byte
		head  []byte
		body  int
		want  []int64
	}{
		{"xiph", laceXiph, []byte{2, 255, 45, 2}, 343, []int64{300, 2, 41}},
		{"ebml", laceEBML, []byte{2, 0x41, 0x2c, 0x5e, 0xd5}, 343, []int64{300, 2, 41}},
		{"fixed", laceFixed, []byte{2}, 99, []int64{33, 33, 33}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var b mkvBlock
			full := append(append([]byte(nil), c.head...), make([]byte, c.body)...)
			if err := b.lace(full, c.flags, 0, int64(len(full))); err != nil {
				t.Fatal(err)
			}
			if len(b.frames) != len(c.want) {
				t.Fatalf("%d frames, want %d", len(b.frames), len(c.want))
			}
			at := int64(len(c.head))
			for i, f := range b.frames {
				if f.size != c.want[i] || f.off != at {
					t.Errorf("frame %d of %d bytes at %d, want %d at %d", i, f.size, f.off, c.want[i], at)
				}
				at += f.size
			}
			if err := b.lace(full[:len(full)-c.body+10], c.flags, 0, int64(len(c.head)+10)); !errors.Is(err, ErrMalformed) {
				t.Errorf("frames past the block: error %v", err)
			}
		})
	}
}

func TestHugeCountsFailWithoutAllocating(t *testing.T) {
	for _, c := range []struct {
		ext, at string
		patch   []byte
	}{
		{".mp4", "trun", []byte{0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}},
		{".mkv", "\x16\x54\xae\x6b", []byte{0x01, 0x0f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}},
	} {
		path, _ := muxClip(t, c.ext)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		i := bytes.LastIndex(b, []byte(c.at))
		if i < 0 {
			t.Fatalf("%s: no %q", c.ext, c.at)
		}
		copy(b[i+len(c.at):], c.patch)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err = Open(bytes.NewReader(b), int64(len(b)), "x")
		runtime.ReadMemStats(&after)
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: error %v, want ErrMalformed", c.ext, err)
		}
		if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
			t.Errorf("%s: %d bytes allocated before failing", c.ext, n)
		}
	}
}
