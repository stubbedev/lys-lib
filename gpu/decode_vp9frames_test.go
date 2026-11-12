package gpu

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"math/bits"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/internal/vp9"
	"github.com/stubbedev/lys-lib/internal/vp9/vp9test"
	"github.com/stubbedev/lys-lib/media/demux"
	"github.com/stubbedev/lys-lib/testbuild"
)

// ivfHeader and ivfFrameHeader are the sizes of an IVF file's header
// and of the header before each of its frames.
const (
	ivfHeader      = 32
	ivfFrameHeader = 12
)

// frameApart is the time between the packets the tests cut streams
// into.
const frameApart = 40 * time.Millisecond

// writeIVF writes pkts, packets of the codec fourcc names with pictures
// up to w x h, as an IVF file, for ffmpeg to decode what the tests put
// together.
func writeIVF(t *testing.T, fourcc string, w, h int, pkts []demux.Packet) string {
	t.Helper()
	out := make([]byte, ivfHeader)
	copy(out, "DKIF")
	binary.LittleEndian.PutUint16(out[6:], ivfHeader)
	copy(out[8:], fourcc)
	binary.LittleEndian.PutUint16(out[12:], uint16(w))
	binary.LittleEndian.PutUint16(out[14:], uint16(h))
	binary.LittleEndian.PutUint32(out[16:], uint32(time.Second/frameApart))
	binary.LittleEndian.PutUint32(out[20:], 1)
	binary.LittleEndian.PutUint32(out[24:], uint32(len(pkts)))
	for _, p := range pkts {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(p.Data)))
		out = binary.LittleEndian.AppendUint64(out, uint64(p.PTS/frameApart))
		out = append(out, p.Data...)
	}
	path := filepath.Join(t.TempDir(), "cut.ivf")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// readIVF reads the packets of the IVF file at path, which starts at a
// keyframe as every stream does and marks no other.
func readIVF(t *testing.T, path string) []demux.Packet {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < ivfHeader || string(b[:4]) != "DKIF" {
		t.Fatalf("%s is no IVF file", path)
	}
	tick := time.Second * time.Duration(binary.LittleEndian.Uint32(b[20:])) / time.Duration(binary.LittleEndian.Uint32(b[16:]))
	var pkts []demux.Packet
	for b = b[ivfHeader:]; len(b) > 0; {
		size := int(binary.LittleEndian.Uint32(b))
		pts := time.Duration(binary.LittleEndian.Uint64(b[4:])) * tick
		pkts = append(pkts, demux.Packet{PTS: pts, Duration: tick, Key: len(pkts) == 0, Data: b[ivfFrameHeader : ivfFrameHeader+size]})
		b = b[ivfFrameHeader+size:]
	}
	return pkts
}

// apart are packets of data, one each, a frame apart from the first, a
// keyframe.
func apart(data [][]byte) []demux.Packet {
	pkts := make([]demux.Packet, len(data))
	for i, b := range data {
		pkts[i] = demux.Packet{PTS: time.Duration(i) * frameApart, Duration: frameApart, Key: i == 0, Data: b}
	}
	return pkts
}

// datas are the packets' data.
func datas(pkts []demux.Packet) [][]byte {
	out := make([][]byte, len(pkts))
	for i, p := range pkts {
		out[i] = p.Data
	}
	return out
}

// checkSized compares frames of sizes that change, each with the frame
// ffmpeg decodes from the file at path at the frame's own size.
func checkSized(t *testing.T, path string, got []decoded) {
	t.Helper()
	raw := runFFmpeg(t, "-v", "error", "-i", path, "-noautoscale", "-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", rawFormat(nv12Samples), "-")
	for i, g := range got {
		n := pictureSize(g.size.X, g.size.Y, nv12Samples)
		if len(raw) < n {
			t.Fatalf("frame %d is %v, ffmpeg's rest %d bytes", i, g.size, len(raw))
		}
		if i > 0 && g.pts <= got[i-1].pts {
			t.Errorf("frame %d presents at %v, after frame %d at %v", i, g.pts, i-1, got[i-1].pts)
		}
		if !bytes.Equal(g.picture, raw[:n]) {
			t.Errorf("frame %d (%v, PTS %v) differs from ffmpeg's in %d of %d bytes", i, g.size, g.pts, differing(g.picture, raw[:n]), n)
		}
		raw = raw[n:]
	}
	if len(raw) != 0 {
		t.Errorf("ffmpeg decoded %d bytes more than the %d frames", len(raw), len(got))
	}
}

// sizeRuns are the sizes of got's frames, each run of one size once.
func sizeRuns(got []decoded) []image.Point {
	var runs []image.Point
	for _, g := range got {
		if len(runs) == 0 || runs[len(runs)-1] != g.size {
			runs = append(runs, g.size)
		}
	}
	return runs
}

// resizedVP9 is a stream libvpx resized as it coded it: fifteen frames
// of 432x240, twenty of 324x180 and seven of 432x240 again, with one
// key frame, so the first frames of each run predict from references of
// the other size. ffmpeg's libvpx encoder resizes nothing, so the
// stream is a file: testdata/README.md says how vpxenc made it.
const resizedVP9 = "testdata/vp9-resize.ivf"

// A frame that is not the size of its references, smaller or larger,
// decodes from them scaled, bit for bit as ffmpeg decodes it, into
// frames of its own size; and a picture of another size than the frames
// around it shows a second time at its own.
func TestDecodeVP9ScalesItsReferences(t *testing.T) {
	d := decodingDevice(t, demux.VP9)
	pkts := readIVF(t, resizedVP9)
	large, small := image.Pt(432, 240), image.Pt(324, 180)
	hdrs := vp9test.Headers(t, datas(pkts))
	keys := 0
	for _, h := range hdrs {
		if h[0].FrameType == vp9.KeyFrame {
			keys++
		}
	}
	if keys != 1 {
		t.Fatalf("%d key frames, want the first alone: a key frame scales no reference", keys)
	}
	t.Run("as coded", func(t *testing.T) {
		v, err := d.NewVideoDecoder(demux.Track{Kind: demux.Video, Codec: demux.VP9})
		if err != nil {
			t.Fatal(err)
		}
		defer v.Close()
		got := decodeAll(t, v, pkts)
		checkSized(t, resizedVP9, got)
		if runs, want := sizeRuns(got), []image.Point{large, small, large}; !slices.Equal(runs, want) {
			t.Errorf("frames of sizes %v, want %v", runs, want)
		}
	})
	t.Run("showing pictures of the other size again", func(t *testing.T) {
		var held [vp9.NumRefFrames]image.Point
		var cut [][]byte
		shown := map[image.Point]int{}
		for i, p := range pkts {
			h := &hdrs[i][0]
			size := image.Pt(h.FrameWidth, h.FrameHeight)
			for slot := range held {
				if h.RefreshFrameFlags>>slot&1 != 0 {
					held[slot] = size
				}
			}
			cut = append(cut, p.Data)
			other := slices.IndexFunc(held[:], func(s image.Point) bool { return s != size })
			if other >= 0 && shown[held[other]] == 0 {
				shown[held[other]]++
				cut = append(cut, vp9test.ShowExisting(uint8(other)))
			}
		}
		if shown[large] != 1 || shown[small] != 1 {
			t.Fatalf("showed %v pictures again, want one of each size among frames of the other", shown)
		}
		again := apart(cut)
		v, err := d.NewVideoDecoder(demux.Track{Kind: demux.Video, Codec: demux.VP9})
		if err != nil {
			t.Fatal(err)
		}
		defer v.Close()
		got := decodeShownAgain(t, v, again)
		checkSized(t, writeIVF(t, "VP90", large.X, large.Y, again), got)
		if want := len(pkts) + 2; len(got) != want {
			t.Errorf("%d frames, want the stream's %d and the two shown again", len(got), len(pkts))
		}
	})
}

// decodeShownAgain is decodeAll for a stream that shows pictures a
// second time, which only a device that decodes into its reference
// pictures alone copies out of them again: on any other the test skips,
// once the refusal is ErrNoVideoDecoder saying so.
func decodeShownAgain(t *testing.T, v *VideoDecoder, pkts []demux.Packet) []decoded {
	t.Helper()
	var out []decoded
	for _, p := range pkts {
		if err := v.Decode(p); err != nil {
			if v.session != 0 && !v.copies && errors.Is(err, ErrNoVideoDecoder) && strings.Contains(err.Error(), "a second time") {
				t.Skipf("%s copies no picture out of its reference pictures: %v", v.d.Name(), err)
			}
			t.Fatal(err)
		}
		out = takeFrames(t, v, out)
	}
	v.Flush()
	return takeFrames(t, v, out)
}

// A frame header that shows a picture shown before shows it again, as
// ffmpeg does: a frame decoded hidden twice, a frame that was shown as
// it was decoded, and the key frame, still in a slot nothing refreshed,
// among the frames that followed it.
func TestDecodeVP9ShowsAFrameAgain(t *testing.T) {
	d := decodingDevice(t, demux.VP9)
	cl := ffmpegPasses(t, "a.webm", 192, 128, 1.2, libvpxTwoPass("-b:v", "200k", "-g", "30", "-auto-alt-ref", "1", "-lag-in-frames", "16")...)
	track, pkts := packets(t, cl.path)
	hdrs := vp9test.Headers(t, datas(pkts))
	hidden := slices.IndexFunc(hdrs, func(h []vp9.FrameHeader) bool { return len(h) == 2 && !h[0].ShowFrame })
	if hidden < 0 {
		t.Fatal("libvpx coded no hidden frame in a superframe")
	}
	const shownAt, oldest = 5, vp9.NumRefFrames - 1
	for i, h := range hdrs[1 : shownAt+1] {
		if h[0].FrameType == vp9.KeyFrame || h[0].RefreshFrameFlags>>oldest&1 != 0 {
			t.Fatalf("packet %d is a key frame or refreshes slot %d, which is to hold the first key frame still", 1+i, oldest)
		}
	}
	var scratch [vp9.MaxFrames][]byte
	both, err := vp9.Frames(pkts[hidden].Data, &scratch)
	if err != nil {
		t.Fatal(err)
	}
	slotOf := func(h *vp9.FrameHeader) uint8 { return uint8(bits.TrailingZeros8(h.RefreshFrameFlags)) }
	all := datas(pkts)
	for _, c := range []struct {
		name  string
		cut   [][]byte
		added int
	}{
		{
			"a hidden frame, twice",
			slices.Concat(all[:hidden], [][]byte{both[0], vp9test.ShowExisting(slotOf(&hdrs[hidden][0])), vp9test.ShowExisting(slotOf(&hdrs[hidden][0])), both[1]}, all[hidden+1:]),
			2,
		},
		{
			"a frame shown as it was decoded",
			slices.Concat(all[:shownAt+1], [][]byte{vp9test.ShowExisting(slotOf(&hdrs[shownAt][0]))}, all[shownAt+1:]),
			1,
		},
		{
			"the key frame, among the frames after it",
			slices.Concat(all[:shownAt+1], [][]byte{vp9test.ShowExisting(oldest)}, all[shownAt+1:]),
			1,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			again := apart(c.cut)
			v, err := d.NewVideoDecoder(track)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			got := decodeShownAgain(t, v, again)
			checkFrames(t, ffmpegDecoded(t, writeIVF(t, "VP90", cl.w, cl.h, again), cl.w, cl.h, nv12Samples), got, 0)
			if want := len(cl.frames) + c.added; len(got) != want {
				t.Errorf("%d frames, want the stream's %d and %d shown again", len(got), len(cl.frames), c.added)
			}
		})
	}
}

func TestDecodeVP9RetriesASuperframeWhoseShownFrameFailed(t *testing.T) {
	d := decodingDevice(t, demux.VP9)
	cl := ffmpegPasses(t, "a.webm", 192, 128, 1.2, libvpxTwoPass("-b:v", "200k", "-g", "30", "-auto-alt-ref", "1", "-lag-in-frames", "16", "-frame-parallel", "0")...)
	track, pkts := packets(t, cl.path)
	hdrs := vp9test.Headers(t, datas(pkts))
	at := 1 + slices.IndexFunc(hdrs[1:], func(h []vp9.FrameHeader) bool {
		return len(h) > 1 && !slices.ContainsFunc(h, func(f vp9.FrameHeader) bool { return f.ShowExistingFrame })
	})
	if at == 0 {
		t.Fatal("libvpx coded no superframe of decoded frames after the first packet")
	}
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	checkFrames(t, cl, decodeRetryingTheLastPicture(t, v, pkts, at, len(hdrs[at]), &v.codec.(*vp9Decoder).resume), 0)
}

// Showing a picture again, copied out of its reference picture into a
// frame of its own, allocates nothing once the decoder has its frames.
func TestDecodeVP9ShowingAFrameAgainDoesNotAllocate(t *testing.T) {
	if testbuild.Race {
		t.Skip("the race detector allocates")
	}
	d := decodingDevice(t, demux.VP9)
	cl := ffmpegClip(t, "a.webm", 192, 128, 4, libvpx("-g", "250")...)
	track, pkts := packets(t, cl.path)
	hdrs := vp9test.Headers(t, datas(pkts))
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	i := 0
	var again demux.Packet
	step := func() {
		p := pkts[i]
		if err := v.Decode(p); err != nil {
			t.Fatal(err)
		}
		again.PTS, again.Data[0] = p.PTS+p.Duration/2, vp9test.ShowExisting(uint8(bits.TrailingZeros8(hdrs[i][0].RefreshFrameFlags)))[0]
		if err := v.Decode(again); err != nil {
			if !v.copies && errors.Is(err, ErrNoVideoDecoder) {
				t.Skipf("%s copies no picture out of its reference pictures: %v", d.Name(), err)
			}
			t.Fatal(err)
		}
		i++
		for n := 0; ; n++ {
			f := v.Next()
			if f == nil {
				if n != 2 {
					t.Fatalf("packet %d and showing its frame again gave %d frames, want 2", i-1, n)
				}
				return
			}
			f.Release()
		}
	}
	again.Data = make([]byte, 1)
	for range 20 {
		step()
	}
	if n := testing.AllocsPerRun(len(pkts)-i-1, step); n != 0 {
		t.Errorf("%.1f allocations a frame and its second showing, want 0", n)
	}
}

// secondKey is the second keyframe of pkts.
func secondKey(t *testing.T, pkts []demux.Packet) int {
	t.Helper()
	key := slices.IndexFunc(pkts[1:], func(p demux.Packet) bool { return p.Key }) + 1
	if key == 0 {
		t.Fatal("no keyframe after the first")
	}
	return key
}

// hidden is what makes of a key frame the hidden intra-only frame to
// says and a frame after it that shows it.
func hidden(to vp9test.IntraOnly) func(*testing.T, []byte) [][]byte {
	return func(t *testing.T, key []byte) [][]byte {
		t.Helper()
		return [][]byte{to.Of(t, key), vp9test.ShowExisting(0)}
	}
}

// recoded is pkts with the key frame at key given way to what with
// makes of it, a frame apart.
func recoded(t *testing.T, pkts []demux.Packet, key int, with func(*testing.T, []byte) [][]byte) []demux.Packet {
	t.Helper()
	all := datas(pkts)
	return apart(slices.Concat(all[:key], with(t, all[key]), all[key+1:]))
}

// The streams libvpx codes for the tests of frame contexts: one whose
// hidden frames decode from frame context 1 and save to it, one that
// uses context 0 alone, and one of error resilient frames, which reset
// every context and save to none.
func vp9TwoContexts() [][]string {
	return libvpxTwoPass("-b:v", "200k", "-g", "20", "-keyint_min", "20", "-auto-alt-ref", "1", "-lag-in-frames", "16")
}
func vp9OneContext() [][]string { return [][]string{libvpx("-g", "12")} }
func vp9NoContext() [][]string  { return [][]string{libvpx("-g", "12", "-error-resilient", "1")} }

// An intra frame decodes as ffmpeg decodes it, and so do the frames
// after it, from the frame contexts and the reference pictures it
// leaves them. libvpx codes no intra-only frame and a key frame only
// with context 0, so each stream is one of libvpx's with its second key
// frame coding another context, or made a hidden intra-only frame the
// next frame shows, which decodes to the key frame's picture wherever
// context 0, the one every intra frame decodes from, is the default
// one then:
//   - resetting every context (reset_frame_context 3), which the hidden
//     frames after it, coded in a context of their own, decode right
//     only from, and coding a context of its own with it, which is not
//     the one it saves to;
//   - resetting the context it names alone (2), refreshing three of the
//     reference slots, the others keeping pictures from before it;
//   - error resilient, which codes no reset_frame_context and resets
//     every context;
//   - resetting no context (0 and 1), or one nothing saved to and that
//     it does not decode from, among error resilient frames, which
//     leave every context the default one;
//   - a key frame coding context 2, which decodes from context 0 and
//     saves to it all the same, where the frames after it find what it
//     saved.
func TestDecodeVP9IntraFramesAndTheirContexts(t *testing.T) {
	d := decodingDevice(t, demux.VP9)
	for _, c := range []struct {
		name   string
		passes [][]string
		with   func(*testing.T, []byte) [][]byte
	}{
		{"intra-only, resetting every context", vp9TwoContexts(), hidden(vp9test.IntraOnly{Reset: vp9.ResetAllContexts, Refresh: 0xff})},
		{"intra-only, resetting every context and coding another", vp9TwoContexts(), hidden(vp9test.IntraOnly{Reset: vp9.ResetAllContexts, Refresh: 0xff, Context: 2})},
		{"intra-only, resetting its context, refreshing three slots", vp9OneContext(), hidden(vp9test.IntraOnly{Reset: vp9.ResetOneContext, Refresh: 0x07})},
		{"intra-only, error resilient", vp9NoContext(), hidden(vp9test.IntraOnly{Resilient: true, Refresh: 0xff})},
		{"intra-only, resetting no context (0)", vp9NoContext(), hidden(vp9test.IntraOnly{Reset: 0, Refresh: 0xff})},
		{"intra-only, resetting no context (1)", vp9NoContext(), hidden(vp9test.IntraOnly{Reset: 1, Refresh: 0x07})},
		{"intra-only, resetting a context nothing saved to", vp9NoContext(), hidden(vp9test.IntraOnly{Reset: vp9.ResetOneContext, Refresh: 0xff, Context: 1})},
		{"key frame coding another context", vp9TwoContexts(), func(t *testing.T, key []byte) [][]byte { return [][]byte{vp9test.KeyContext(t, key, 2)} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := ffmpegPasses(t, "a.webm", 192, 128, 1.4, c.passes...)
			track, pkts := packets(t, cl.path)
			cut := recoded(t, pkts, secondKey(t, pkts), c.with)
			v, err := d.NewVideoDecoder(track)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			got := decodeAll(t, v, cut)
			checkFrames(t, ffmpegDecoded(t, writeIVF(t, "VP90", cl.w, cl.h, cut), cl.w, cl.h, nv12Samples), got, 0)
			checkFrames(t, cl, got, 0)
		})
	}
}

// An intra-only frame that resets a context frames saved to, and which
// is not context 0, the one it decodes from, is refused, naming both: a
// Vulkan Video picture names one context, for a device to reset and to
// decode from.
func TestDecodeVP9RefusesToResetASavedContextApart(t *testing.T) {
	d := decodingDevice(t, demux.VP9)
	cl := ffmpegPasses(t, "a.webm", 192, 128, 1.4, vp9TwoContexts()...)
	track, pkts := packets(t, cl.path)
	key := secondKey(t, pkts)
	cut := recoded(t, pkts, key, hidden(vp9test.IntraOnly{Reset: vp9.ResetOneContext, Refresh: 0xff, Context: 1}))
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	decodeSome(t, v, cut[:key])
	err = v.Decode(cut[key])
	if !errors.Is(err, ErrNoVideoDecoder) || !strings.Contains(err.Error(), "resets frame context 1, which holds saved probabilities, and decodes from context 0") {
		t.Errorf("decoding an intra-only frame that resets context 1: %v, want ErrNoVideoDecoder naming contexts 1 and 0", err)
	}
}

// splicedAt is where spliced's second stream starts.
const splicedAt = 6

// spliced is the first splicedAt packets of a stream libvpx codes with
// args, then another of another size, its key frame made the hidden
// intra-only frame to says and shown by the frame after it.
func spliced(t *testing.T, first, second image.Point, to vp9test.IntraOnly, args ...string) []demux.Packet {
	t.Helper()
	args = libvpx(append([]string{"-g", "250"}, args...)...)
	a := ffmpegClip(t, "a.webm", first.X, first.Y, 0.4, args...)
	b := ffmpegClip(t, "b.webm", second.X, second.Y, 0.4, args...)
	_, head := packets(t, a.path)
	_, tail := packets(t, b.path)
	return apart(slices.Concat(datas(head[:splicedAt]), datas(recoded(t, tail, 0, hidden(to)))))
}

// An intra-only frame changes the size of the stream's frames as a key
// frame does, while reference slots it does not refresh keep pictures
// of the size before: to a smaller size within the video session there
// is; to a larger one, where it leaves nothing of the frames before it,
// neither a reference picture nor a frame context, in another session.
func TestDecodeVP9IntraOnlyFramesChangeTheSize(t *testing.T) {
	d := decodingDevice(t, demux.VP9)
	large, small := image.Pt(256, 144), image.Pt(192, 128)
	for _, c := range []struct {
		name          string
		first, second image.Point
		to            vp9test.IntraOnly
		args          []string
	}{
		{"smaller, keeping reference pictures", large, small, vp9test.IntraOnly{Reset: vp9.ResetOneContext, Refresh: 0x07}, nil},
		{"larger, keeping nothing", small, large, vp9test.IntraOnly{Reset: vp9.ResetAllContexts, Refresh: 0xff}, nil},
		{"larger and error resilient, keeping nothing", small, large, vp9test.IntraOnly{Resilient: true, Refresh: 0xff}, []string{"-error-resilient", "1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cut := spliced(t, c.first, c.second, c.to, c.args...)
			v, err := d.NewVideoDecoder(demux.Track{Kind: demux.Video, Codec: demux.VP9})
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			got := decodeAll(t, v, cut)
			checkSized(t, writeIVF(t, "VP90", large.X, large.Y, cut), got)
			if runs, want := sizeRuns(got), []image.Point{c.first, c.second}; !slices.Equal(runs, want) {
				t.Errorf("frames of sizes %v, want %v", runs, want)
			}
		})
	}
}

// A frame larger than the key frame its video session was made for,
// which leaves reference pictures or frame contexts of the frames
// before it for the frames after it, is refused, saying both sizes: a
// session's pictures do not grow, and another session has neither.
func TestDecodeVP9RefusesToGrowAmongReferences(t *testing.T) {
	d := decodingDevice(t, demux.VP9)
	for _, c := range []struct {
		name string
		to   vp9test.IntraOnly
	}{
		{"keeping reference pictures", vp9test.IntraOnly{Reset: vp9.ResetAllContexts, Refresh: 0x07}},
		{"keeping frame contexts", vp9test.IntraOnly{Reset: vp9.ResetOneContext, Refresh: 0xff}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cut := spliced(t, image.Pt(192, 128), image.Pt(256, 144), c.to)
			v, err := d.NewVideoDecoder(demux.Track{Kind: demux.Video, Codec: demux.VP9})
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			for i, p := range cut {
				err = v.Decode(p)
				if err != nil {
					if i != splicedAt {
						t.Errorf("packet %d failed, want the intra-only frame, packet %d", i, splicedAt)
					}
					break
				}
			}
			if !errors.Is(err, ErrNoVideoDecoder) || !strings.Contains(err.Error(), "256x144, larger than the 192x128 pictures of its video session") {
				t.Errorf("decoding a larger frame among references: %v, want ErrNoVideoDecoder naming both sizes", err)
			}
		})
	}
}
