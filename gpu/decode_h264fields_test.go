package gpu

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/internal/nal"
	"github.com/stubbedev/lys-lib/media/demux"
	"github.com/stubbedev/lys-lib/testbuild"
)

// x264Interlaced is ffmpeg's arguments for x264 coding the test pattern
// interlaced, two of its frames a frame's fields in the order first
// says, with args: frames whose macroblock pairs are each coded as
// frame or field macroblocks (MBAFF), which is the interlaced coding
// x264 has.
func x264Interlaced(first FieldOrder, args ...string) []string {
	scan := "interlace=scan=tff"
	if first == BottomFieldFirst {
		scan = "interlace=scan=bff"
	}
	return append([]string{"-vf", scan, "-c:v", "libx264", "-flags", "+ilme+ildct"}, args...)
}

// Frames x264 codes interlaced, their macroblock pairs frame or field
// macroblocks, decode bit for bit as ffmpeg decodes them, the fields
// woven as they are coded: with B-frames and CABAC, bottom field first,
// CAVLC, more reference frames than a field picture could name on the
// device, and a size that crops.
func TestDecodeH264MBAFFMatchesFFmpeg(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	for _, c := range []struct {
		name string
		w, h int
		args []string
	}{
		{"B-frames, CABAC", 192, 128, x264Interlaced(TopFieldFirst, "-bf", "2", "-x264-params", "keyint=12")},
		{"bottom field first", 192, 128, x264Interlaced(BottomFieldFirst, "-bf", "2", "-x264-params", "keyint=12")},
		{"CAVLC, Main", 192, 128, x264Interlaced(TopFieldFirst, "-profile:v", "main", "-coder", "0", "-bf", "1", "-x264-params", "keyint=12")},
		{"12 reference frames", 192, 128, x264Interlaced(TopFieldFirst, "-bf", "0", "-refs", "12", "-x264-params", "keyint=50")},
		{"cropped 270x152", 270, 152, x264Interlaced(TopFieldFirst, "-bf", "2", "-x264-params", "keyint=12")},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := ffmpegClip(t, "a.mp4", c.w, c.h, 2, c.args...)
			track, pkts := packets(t, cl.path)
			v, err := d.NewVideoDecoder(track)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			checkFrames(t, cl, decodeAll(t, v, pkts), 0)
			if sps := v.codec.(*h264Decoder).active; sps.FrameMbsOnly || !sps.MbAdaptiveFrameField {
				t.Errorf("x264 coded frames only: %v, macroblock-adaptive frames: %v; want the latter", sps.FrameMbsOnly, sps.MbAdaptiveFrameField)
			}
		})
	}
}

// The sizes of the streams of fields: the frames', and a field's, which
// the streams they are cut of are coded at.
const (
	fieldsW, fieldsH = 192, 128
	fieldH           = fieldsH / 2
)

// pFields is a stream of P pictures at a field's size, its picture
// order counts following frame_num, with refs reference frames.
func pFields(t *testing.T, secs float64, refs int) *h264Source {
	t.Helper()
	return x264Source(t, fieldsW, fieldH, secs, "-bf", "0", "-refs", strconv.Itoa(refs), "-x264-params", "keyint=250")
}

// unmarkShort is the memory management control operation by which a
// field of picture number curr marks the short-term field of picture
// number picNum unused, each worked out as 8.2.4.1 does for the field
// that codes the operation: twice the frame's FrameNumWrap, plus one
// for a field of its own parity.
func unmarkShort(curr, picNum uint32) nal.H264MMCO {
	return nal.H264MMCO{Op: 1, DifferenceOfPicNumsMinus1: curr - picNum - 1}
}

// fieldCuts are the streams of fields the tests decode, each cut of
// pictures x264 coded: see h264cut_test.go.
var fieldCuts = []struct {
	name string
	cut  func(t *testing.T) *h264Cut
}{
	{"P fields, top first", func(t *testing.T) *h264Cut {
		src := x264Source(t, fieldsW, fieldH, 2, "-bf", "0", "-x264-params", "keyint=12")
		cut := newH264Cut(t, fieldSPS(t, src.sps, false), src.pps)
		cut.fieldPairs(src, h264Top)
		return cut
	}},
	{"P fields, bottom first", func(t *testing.T) *h264Cut {
		src := x264Source(t, fieldsW, fieldH, 2, "-bf", "0", "-x264-params", "keyint=12")
		cut := newH264Cut(t, fieldSPS(t, src.sps, false), src.pps)
		cut.fieldPairs(src, h264Bottom)
		return cut
	}},
	{"B fields, pairs that are no references", func(t *testing.T) *h264Cut {
		src := x264Source(t, fieldsW, fieldH, 2, "-bf", "2", "-x264-params", "keyint=12:b-pyramid=none")
		cut := newH264Cut(t, fieldSPS(t, src.sps, false), src.pps)
		cut.fieldPairs(src, h264Top)
		pairs := 0
		for i, p := range cut.made {
			if p.second && !p.ref && !cut.made[i-1].ref {
				pairs++
			}
		}
		if pairs == 0 || cut.seq.PicOrderCntType != 0 {
			t.Fatalf("%d pairs of fields that are no references, picture order count type %d; want some, and type 0", pairs, cut.seq.PicOrderCntType)
		}
		return cut
	}},
	{"B fields, bottom first", func(t *testing.T) *h264Cut {
		src := x264Source(t, fieldsW, fieldH, 2, "-bf", "2", "-x264-params", "keyint=12:b-pyramid=none")
		cut := newH264Cut(t, fieldSPS(t, src.sps, false), src.pps)
		cut.fieldPairs(src, h264Bottom)
		return cut
	}},
	{"two slices a field", func(t *testing.T) *h264Cut {
		src := x264Source(t, fieldsW, fieldH, 1, "-bf", "0", "-x264-params", "keyint=12:slices=2")
		if n := len(src.pics[1].slices); n != 2 {
			t.Fatalf("x264 coded %d slices a picture, want 2", n)
		}
		cut := newH264Cut(t, fieldSPS(t, src.sps, false), src.pps)
		cut.fieldPairs(src, h264Top)
		return cut
	}},
	{"fields without their frame's other", func(t *testing.T) *h264Cut {
		src := pFields(t, 0.52, 10)
		cut := newH264Cut(t, fieldSPS(t, src.sps, false), src.pps)
		cut.field(src, 0, h264Top, false)
		for i := 1; i+6 <= len(src.pics); i += 6 {
			cut.field(src, i, h264Top, false)
			cut.field(src, i+1, h264Bottom, true)
			cut.field(src, i+2, h264Bottom, false)
			cut.field(src, i+3, h264Bottom, false)
			cut.field(src, i+4, h264Top, true)
			cut.field(src, i+5, h264Top, false)
		}
		if len(cut.units) != 13 || cut.seq.MaxNumRefFrames < 9 {
			t.Fatalf("%d fields with %d reference frames, want 13 and room for the 9 frames they are of: ffmpeg fails where a field without its frame's other slides out of the reference frames", len(cut.units), cut.seq.MaxNumRefFrames)
		}
		return cut
	}},
	{"frames and fields", func(t *testing.T) *h264Cut {
		frames := x264Source(t, fieldsW, fieldsH, 0.6, "-bf", "0", "-refs", "3", "-x264-params", "keyint=250")
		fields := pFields(t, 1.2, 3)
		cut := newH264Cut(t, fieldSPS(t, fields.sps, false), fields.pps)
		cut.frame(frames, 0)
		for i := 1; i < len(frames.pics); i++ {
			cut.field(fields, 2*i-1, h264Top, false)
			cut.field(fields, 2*i, h264Bottom, true)
			cut.frame(frames, i)
			if i%4 == 0 {
				cut.frame(frames, i)
			}
		}
		return cut
	}},
	{"macroblock-adaptive frames and fields", func(t *testing.T) *h264Cut {
		frames := x264Source(t, fieldsW, fieldsH, 1.2, "-vf", "interlace", "-flags", "+ilme+ildct", "-bf", "0", "-refs", "3", "-x264-params", "keyint=250")
		fields := pFields(t, 1.2, 3)
		if frames.seq.FrameMbsOnly || !frames.seq.MbAdaptiveFrameField {
			t.Fatal("x264 coded no macroblock-adaptive frames")
		}
		cut := newH264Cut(t, frames.sps, frames.pps)
		cut.frame(frames, 0)
		for i := 1; i < len(frames.pics); i++ {
			cut.field(fields, 2*i-1, h264Bottom, false)
			cut.field(fields, 2*i, h264Top, true)
			cut.frame(frames, i)
		}
		return cut
	}},
	{"a PPS changed between a frame's fields", func(t *testing.T) *h264Cut {
		src := pFields(t, 0.8, 3)
		cut := newH264Cut(t, fieldSPS(t, src.sps, false), src.pps)
		const qp = 3
		coarser := ppsWithQP(t, src.pps, qp)
		for i := 0; i+1 < len(src.pics); i += 2 {
			cut.picture(cutPicture{src: src, index: i, fields: h264Top, ref: true, pps: src.pps})
			cut.picture(cutPicture{src: src, index: i + 1, fields: h264Bottom, second: true, ref: true, pps: coarser, qp: qp})
		}
		return cut
	}},
	{"fields and frames marked by memory management control operations", func(t *testing.T) *h264Cut {
		frames := x264Source(t, fieldsW, fieldsH, 0.2, "-bf", "0", "-refs", "4", "-x264-params", "keyint=250")
		fields := pFields(t, 0.8, 4)
		if fields.seq.MaxNumRefFrames != 4 {
			t.Fatalf("x264 keeps %d reference frames, want 4", fields.seq.MaxNumRefFrames)
		}
		cut := newH264Cut(t, fieldSPS(t, fields.sps, false), fields.pps)
		same, other := func(frame uint32) uint32 { return 2*frame + 1 }, func(frame uint32) uint32 { return 2 * frame }
		cut.frame(frames, 0)
		cut.field(fields, 1, h264Top, false)
		cut.field(fields, 2, h264Bottom, true)
		cut.frame(frames, 1, nal.H264MMCO{Op: 4, MaxLongTermFrameIdxPlus1: 2}, nal.H264MMCO{Op: 6, LongTermFrameIdx: 0})
		cut.field(fields, 3, h264Top, false, unmarkShort(same(3), other(1)))
		cut.field(fields, 4, h264Bottom, true)
		cut.field(fields, 5, h264Top, false, nal.H264MMCO{Op: 2, LongTermPicNum: other(0)}, nal.H264MMCO{Op: 2, LongTermPicNum: same(0)})
		cut.field(fields, 6, h264Bottom, true)
		cut.field(fields, 7, h264Top, false, nal.H264MMCO{Op: 6, LongTermFrameIdx: 1}, unmarkShort(same(5), same(1)))
		cut.field(fields, 8, h264Bottom, false)
		cut.field(fields, 9, h264Top, true)
		cut.field(fields, 10, h264Top, false, nal.H264MMCO{Op: 4, MaxLongTermFrameIdxPlus1: 1})
		cut.field(fields, 11, h264Bottom, true)
		cut.frame(frames, 2)
		for i := 12; i+1 < len(fields.pics); i += 2 {
			cut.field(fields, i, h264Top, false)
			cut.field(fields, i+1, h264Bottom, true)
		}
		return cut
	}},
}

// Field pictures decode bit for bit as ffmpeg decodes them, a frame's
// two fields woven into one frame, whether a packet holds a frame's two
// fields, as MP4 does, or one, as a transport stream does: P fields
// predicting from fields of both parities and of several frames, top
// field first and bottom field first, their picture order counts
// following frame_num; B fields, with pairs that are no references;
// fields of several slices; fields whose frame's other field never
// comes, which are references and never shown; frames, and frames of
// field and frame macroblock pairs, between the field pairs, each
// predicting from the other kind; and memory management control
// operations: a frame made a long-term reference that fields predict
// from, fields that mark single fields of short-term and of long-term
// frames unused, and a field without its frame's other made a
// long-term reference; and a PPS that another of its ID replaces
// between a frame's two fields, each field decoding with the one
// before it. (ffmpeg loses a first field's marking where the
// second field of its frame is made a long-term reference, and counts
// a frame twice where its fields are made long-term one by one: what
// the decoder marks there is tested against the standard,
// decode_h264dpb_test.go, not against ffmpeg.)
func TestDecodeH264FieldsMatchFFmpeg(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	for _, c := range fieldCuts {
		for _, apart := range []bool{false, true} {
			name := c.name + ", a packet a frame"
			if apart {
				name = c.name + ", a packet a field"
			}
			t.Run(name, func(t *testing.T) {
				cut := c.cut(t)
				v, err := d.NewVideoDecoder(cut.track())
				if err != nil {
					t.Fatal(err)
				}
				defer v.Close()
				checkFrames(t, cut.reference(fieldsW, fieldsH), decodeAll(t, v, cut.packets(apart)), 0)
			})
		}
	}
}

// A stream that names the Extended profile and says it obeys Main's
// constraints decodes as the Main profile stream it is.
func TestDecodeH264ExtendedWithinMain(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	src := x264Source(t, fieldsW, fieldH, 1, "-bf", "2", "-x264-params", "keyint=12:b-pyramid=none")
	cut := newH264Cut(t, extendedSPS(src.sps), src.pps)
	for i := range src.pics {
		cut.add(cutPicture{src: src, index: i, fields: h264Frame, ref: src.pics[i].ref, order: int32(src.pics[i].display)})
	}
	if cut.seq.ProfileIdc != h264Extended {
		t.Fatalf("the stream is profile_idc %d", cut.seq.ProfileIdc)
	}
	v, err := d.NewVideoDecoder(cut.track())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	checkFrames(t, cut.reference(fieldsW, fieldH), decodeAll(t, v, cut.packets(false)), 0)
}

// ffmpegFieldOrders are the field orders of the frames ffmpeg decodes
// from the stream at path, by the flags ffprobe prints of each.
func ffmpegFieldOrders(t *testing.T, path string) []FieldOrder {
	t.Helper()
	cmd := runCommand(t, "ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "frame=interlaced_frame,top_field_first", "-of", "csv=p=0", path)
	var out []FieldOrder
	for line := range strings.FieldsSeq(string(cmd)) {
		switch {
		case strings.HasPrefix(line, "0,"):
			out = append(out, Progressive)
		case strings.HasPrefix(line, "1,1"):
			out = append(out, TopFieldFirst)
		case strings.HasPrefix(line, "1,0"):
			out = append(out, BottomFieldFirst)
		default:
			t.Fatalf("ffprobe says %q of a frame", line)
		}
	}
	return out
}

// A frame says whether it is two fields woven and which is the earlier,
// as ffmpeg says of it: progressive frames, x264's interlaced frames
// top and bottom field first, and field pairs of both orders.
func TestDecodeH264FieldOrder(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	orders := func(v *VideoDecoder, pkts []demux.Packet) []FieldOrder {
		var out []FieldOrder
		take := func() {
			for f := v.Next(); f != nil; f = v.Next() {
				out = append(out, f.FieldOrder())
				f.Release()
			}
		}
		for _, p := range pkts {
			if err := v.Decode(p); err != nil {
				t.Fatal(err)
			}
			take()
		}
		v.Flush()
		take()
		return out
	}
	check := func(t *testing.T, track demux.Track, pkts []demux.Packet, path string, all FieldOrder) {
		t.Helper()
		v, err := d.NewVideoDecoder(track)
		if err != nil {
			t.Fatal(err)
		}
		defer v.Close()
		got, want := orders(v, pkts), ffmpegFieldOrders(t, path)
		if !slices.Equal(got, want) {
			t.Errorf("frames are %v, ffmpeg says %v", got, want)
		}
		if len(got) == 0 || slices.ContainsFunc(got, func(o FieldOrder) bool { return o != all }) {
			t.Errorf("frames are %v, want every one %v", got, all)
		}
	}
	for _, c := range []struct {
		name string
		args []string
		want FieldOrder
	}{
		{"progressive", []string{"-c:v", "libx264", "-bf", "2"}, Progressive},
		{"macroblock-adaptive frames, top field first", x264Interlaced(TopFieldFirst, "-bf", "2"), TopFieldFirst},
		{"macroblock-adaptive frames, bottom field first", x264Interlaced(BottomFieldFirst, "-bf", "2"), BottomFieldFirst},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := ffmpegClip(t, "a.mp4", 192, 128, 1, c.args...)
			track, pkts := packets(t, cl.path)
			check(t, track, pkts, cl.path, c.want)
		})
	}
	for _, c := range []struct {
		name string
		cut  int
		want FieldOrder
	}{
		{"field pairs of one picture order count", 0, TopFieldFirst},
		{"field pairs, top field first", 2, TopFieldFirst},
		{"field pairs, bottom field first", 3, BottomFieldFirst},
	} {
		t.Run(c.name, func(t *testing.T) {
			cut := fieldCuts[c.cut].cut(t)
			check(t, cut.track(), cut.packets(false), cut.annexB(), c.want)
		})
	}
}

// After a seek, Reset and the packets from a keyframe decode the frames
// from it on as a decode from the start does, though the field decoded
// last before the reset waited for its frame's other.
func TestDecodeH264FieldsResetAtAKeyframe(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	cut := fieldCuts[0].cut(t)
	pkts := cut.packets(true)
	v, err := d.NewVideoDecoder(cut.track())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	decodeSome(t, v, pkts[:9])
	if err := v.Reset(); err != nil {
		t.Fatal(err)
	}
	key := slices.IndexFunc(pkts[1:], func(p demux.Packet) bool { return p.Key }) + 1
	if key == 0 || key%2 != 0 {
		t.Fatalf("the second keyframe is packet %d, want one that starts a frame", key)
	}
	checkFrames(t, cut.reference(fieldsW, fieldsH), decodeAll(t, v, pkts[key-3:]), key/2)
}

// A packet of a frame's two fields whose second field finds every frame
// held fails, leaving the decoder as it was before the packet: retried
// once the frames are released, it decodes as though it had not failed,
// and so do the packets after it, though its first field had decoded.
// That holds where a PPS sent between the fields replaces the one of
// its ID the first field decodes with: retried, the first field decodes
// with the PPS before the packet again, not the one its second field
// sent.
func TestDecodeH264RetriesAPacketWhoseSecondFieldFailed(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	for _, c := range []struct {
		name string
		cut  func(t *testing.T) *h264Cut
	}{
		{fieldCuts[0].name, fieldCuts[0].cut},
		{"a PPS sent before each second field alone", func(t *testing.T) *h264Cut {
			src := pFields(t, 0.8, 3)
			cut := newH264Cut(t, fieldSPS(t, src.sps, false), src.pps)
			const qp = 3
			sets := [2][]byte{src.pps, ppsWithQP(t, src.pps, qp)}
			for i := 0; i+1 < len(src.pics); i += 2 {
				k := i / 2 % 2
				cut.picture(cutPicture{src: src, index: i, fields: h264Top, ref: true, qp: int32(k) * qp})
				cut.picture(cutPicture{src: src, index: i + 1, fields: h264Bottom, second: true, ref: true, pps: sets[1-k], qp: int32(1-k) * qp})
			}
			return cut
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cut := c.cut(t)
			pkts := cut.packets(false)
			v, err := d.NewVideoDecoder(cut.track())
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			var (
				out  []decoded
				held []*VideoFrame
			)
			failed := -1
			for i := 0; i < len(pkts); i++ {
				err := v.Decode(pkts[i])
				if err == nil {
					if failed >= 0 {
						out = takeFrames(t, v, out)
						continue
					}
					for f := v.Next(); f != nil; f = v.Next() {
						held = append(held, f)
					}
					continue
				}
				if failed >= 0 || !strings.Contains(err.Error(), "frames are held") {
					t.Fatalf("packet %d: %v", i, err)
				}
				if part := v.codec.(*h264Decoder).part; part != 2 {
					t.Fatalf("packet %d failed at its picture %d, want its second field: %v", i, part, err)
				}
				failed = i
				for _, f := range held {
					out = append(out, decoded{f.PTS(), f.Size(), readPicture(t, f)})
					f.Release()
				}
				held = nil
				i--
			}
			if failed < 0 {
				t.Fatalf("holding every frame, all %d packets decoded", len(pkts))
			}
			v.Flush()
			checkFrames(t, cut.reference(fieldsW, fieldsH), takeFrames(t, v, out), 0)
		})
	}
}

// lengthPrefixed is units, each after its length in four bytes, as a
// packet holds them.
func lengthPrefixed(units ...[]byte) []byte {
	var out []byte
	for _, u := range units {
		out = append(out, byte(len(u)>>24), byte(len(u)>>16), byte(len(u)>>8), byte(len(u)))
		out = append(out, u...)
	}
	return out
}

// A packet that holds more than a frame or the two fields of one, or a
// parameter set that does not parse after its pictures, fails before
// any of its pictures decodes: no frame of it comes out, and the
// packets it was made of, given after it, decode as though it had never
// been given.
func TestDecodeH264RefusesAMalformedPacketBeforeDecodingIt(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	joined := func(pkts []demux.Packet, extra ...[]byte) demux.Packet {
		p := pkts[0]
		p.Data = nil
		for _, q := range pkts {
			p.Data = append(p.Data, q.Data...)
		}
		p.Data = append(p.Data, lengthPrefixed(extra...)...)
		return p
	}
	frames := func(t *testing.T) (demux.Track, []demux.Packet, stream) {
		cl := ffmpegClip(t, "a.mp4", fieldsW, fieldsH, 1, "-c:v", "libx264", "-bf", "0", "-x264-params", "keyint=250")
		track, pkts := packets(t, cl.path)
		return track, pkts, cl
	}
	fields := func(t *testing.T) (demux.Track, []demux.Packet, stream) {
		cut := fieldCuts[0].cut(t)
		return cut.track(), cut.packets(true), cut.reference(fieldsW, fieldsH)
	}
	for _, c := range []struct {
		name   string
		stream func(t *testing.T) (demux.Track, []demux.Packet, stream)
		at, n  int
		extra  [][]byte
		want   string
	}{
		{"two frames", frames, 3, 2, nil, "a frame and another picture"},
		{"a frame's two fields and another", fields, 4, 3, nil, "3 pictures"},
		{"a frame and a PPS that does not parse", frames, 3, 1, [][]byte{{0x68}}, "parameter set of 1 bytes"},
		{"a frame's two fields and a PPS that does not parse", fields, 4, 2, [][]byte{{0x68}}, "parameter set of 1 bytes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			track, pkts, ref := c.stream(t)
			v, err := d.NewVideoDecoder(track)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			out := decodeSome(t, v, pkts[:c.at])
			err = v.Decode(joined(pkts[c.at:c.at+c.n], c.extra...))
			if !errors.Is(err, nal.ErrH264) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("the joined packet: %v, want nal.ErrH264 saying %q", err, c.want)
			}
			if f := v.Next(); f != nil {
				t.Fatalf("the packet that failed completed the frame at %v", f.PTS())
			}
			out = append(out, decodeSome(t, v, pkts[c.at:])...)
			v.Flush()
			checkFrames(t, ref, takeFrames(t, v, out), 0)
		})
	}
}

// Decoding fields, handing their frames out and releasing them
// allocates nothing once the decoder has its frames.
func TestDecodeH264FieldsDoNotAllocate(t *testing.T) {
	if testbuild.Race {
		t.Skip("the race detector allocates")
	}
	d := decodingDevice(t, demux.H264)
	src := pFields(t, 6, 3)
	cut := newH264Cut(t, fieldSPS(t, src.sps, false), src.pps)
	cut.fieldPairs(src, h264Top)
	pkts := cut.packets(true)
	v, err := d.NewVideoDecoder(cut.track())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	i := 0
	step := func() {
		for range 2 {
			if err := v.Decode(pkts[i]); err != nil {
				t.Fatal(err)
			}
			i++
		}
		for f := v.Next(); f != nil; f = v.Next() {
			f.Release()
		}
	}
	for range 20 {
		step()
	}
	if n := testing.AllocsPerRun((len(pkts)-i)/2-1, step); n != 0 {
		t.Errorf("%.1f allocations a frame of two fields, want 0", n)
	}
}

// A field that predicts from more reference fields than the device
// decodes a picture from is refused, saying how many of each: every
// field before it decodes, each a reference picture of its own.
func TestDecodeH264RefusesMoreReferenceFieldsThanTheDeviceDecodesFrom(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	src := pFields(t, 0.8, 10)
	cut := newH264Cut(t, fieldSPS(t, src.sps, false), src.pps)
	cut.fieldPairs(src, h264Top)
	pkts := cut.packets(true)
	v, err := d.NewVideoDecoder(cut.track())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if err := v.Decode(pkts[0]); err != nil {
		t.Fatal(err)
	}
	limit, fields := v.activeRefs(), min(2*int(cut.seq.MaxNumRefFrames), len(pkts)-1)
	if limit >= fields {
		t.Skipf("the device decodes a picture from %d reference pictures, as many as the %d fields the stream predicts from", limit, fields)
	}
	for i, p := range pkts[1 : limit+1] {
		if err := v.Decode(p); err != nil {
			t.Fatalf("field %d, predicting from %d fields: %v", i+1, i+1, err)
		}
	}
	err = v.Decode(pkts[limit+1])
	want := strconv.Itoa(limit+1) + " reference pictures"
	if !errors.Is(err, ErrNoVideoDecoder) || !strings.Contains(err.Error(), want) {
		t.Errorf("field %d: %v, want ErrNoVideoDecoder saying %q", limit+1, err, want)
	}
}

// What the decoder does not decode of fields is refused, saying what:
// a frame whose fields are a reference and none, as no decoder pairs
// them; a frame with a short-term and a long-term reference field,
// which a Vulkan Video reference picture cannot say; and a gap in
// frame_num, which the decoder infers frames for in sequences of
// frames only.
func TestDecodeH264RefusesFieldsItCannotDecode(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	for _, c := range []struct {
		name string
		cut  func(t *testing.T, src *h264Source) *h264Cut
		at   int
		want string
	}{
		{"a reference field and one that is none", func(t *testing.T, src *h264Source) *h264Cut {
			cut := newH264Cut(t, fieldSPS(t, src.sps, false), src.pps)
			cut.field(src, 0, h264Top, false)
			cut.field(src, 1, h264Bottom, true)
			cut.field(src, 2, h264Top, false)
			cut.picture(cutPicture{src: src, index: 3, fields: h264Bottom, second: true})
			return cut
		}, 3, "a reference field and one that is none"},
		{"a short-term and a long-term field", func(t *testing.T, src *h264Source) *h264Cut {
			cut := newH264Cut(t, fieldSPS(t, src.sps, false), src.pps)
			cut.field(src, 0, h264Top, false)
			cut.field(src, 1, h264Bottom, true)
			cut.field(src, 2, h264Top, false, nal.H264MMCO{Op: 6})
			cut.field(src, 3, h264Bottom, true)
			cut.field(src, 4, h264Top, false)
			return cut
		}, 4, "a short-term reference and one that is a long-term reference"},
		{"one field of a frame decoded as one picture", func(t *testing.T, src *h264Source) *h264Cut {
			frames := x264Source(t, fieldsW, fieldsH, 0.2, "-bf", "0", "-refs", "3", "-x264-params", "keyint=250")
			cut := newH264Cut(t, fieldSPS(t, src.sps, false), src.pps)
			cut.frame(frames, 0)
			cut.field(src, 1, h264Top, false, unmarkShort(3, 0))
			cut.field(src, 2, h264Bottom, true)
			return cut
		}, 2, "decoded as one picture, has its top field alone left a reference"},
		{"a gap in frame_num", func(t *testing.T, src *h264Source) *h264Cut {
			cut := newH264Cut(t, fieldSPS(t, src.sps, true), src.pps)
			cut.field(src, 0, h264Top, false)
			cut.field(src, 1, h264Bottom, true)
			cut.prevRef++
			cut.field(src, 2, h264Top, false)
			return cut
		}, 2, "frame_num skips 1 frames"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cut := c.cut(t, pFields(t, 0.4, 3))
			pkts := cut.packets(true)
			v, err := d.NewVideoDecoder(cut.track())
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			for i, p := range pkts {
				err := v.Decode(p)
				if i < c.at && err != nil {
					t.Fatalf("packet %d: %v", i, err)
				}
				if i == c.at && (!errors.Is(err, ErrNoVideoDecoder) || !strings.Contains(err.Error(), c.want)) {
					t.Errorf("packet %d: %v, want ErrNoVideoDecoder saying %q", i, err, c.want)
				}
			}
		})
	}
}
