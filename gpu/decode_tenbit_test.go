package gpu

import (
	"errors"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/media/demux"
)

// A 10-bit stream (H.265 Main 10, AV1 Main, VP9 profile 2) decodes into
// P010 pictures bit for bit as ffmpeg decodes it, where the device
// decodes the codec at 10 bits.
func TestDecodeTenBitMatchesFFmpeg(t *testing.T) {
	tenBit := []string{"-pix_fmt", "yuv420p10le"}
	for _, c := range []struct {
		name, file string
		codec      demux.Codec
		passes     [][]string
	}{
		{"H.265 Main 10, B-pyramid", "a.mp4", demux.H265, [][]string{append([]string{"-c:v", "libx265", "-x265-params", "log-level=none:keyint=25:bframes=3:b-pyramid=1"}, tenBit...)}},
		{"AV1 Main, hidden frames", "b.mp4", demux.AV1, [][]string{append(libaom("", "-g", "20", "-lag-in-frames", "19", "-auto-alt-ref", "1"), tenBit...)}},
		{"VP9 profile 2, hidden alternate references", "c.webm", demux.VP9, libvpxTwoPass(append([]string{"-b:v", "200k", "-g", "25", "-auto-alt-ref", "1", "-lag-in-frames", "16"}, tenBit...)...)},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := decodingDevice(t, c.codec)
			cl := encodeClip(t, c.file, 192, 144, 2, fourcc.P010, c.passes...)
			track, pkts := packets(t, cl.path)
			v, err := d.NewVideoDecoder(track)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			if err := v.Decode(pkts[0]); errors.Is(err, ErrNoVideoDecoder) {
				t.Skipf("%s decodes no 10-bit %s: %v", d.Name(), c.codec, err)
			}
			if err := v.Reset(); err != nil {
				t.Fatal(err)
			}
			checkFrames(t, cl, decodeAll(t, v, pkts), 0)
		})
	}
}

// Streams outside 4:2:0 at 8 or 10 bits are refused with an error that
// names what they are: 4:2:2 H.265, 4:4:4 AV1 (profile 1) and 12-bit
// VP9.
func TestDecodeRefusesOtherFormats(t *testing.T) {
	for _, c := range []struct {
		name, file string
		codec      demux.Codec
		args       []string
		names      string
	}{
		{"H.265 4:2:2", "a.mp4", demux.H265, []string{"-c:v", "libx265", "-pix_fmt", "yuv422p10le", "-x265-params", "log-level=none"}, "chroma_format_idc 2"},
		{"AV1 4:4:4", "b.mp4", demux.AV1, libaom("", "-pix_fmt", "yuv444p"), "profile 1"},
		{"VP9 12-bit", "c.webm", demux.VP9, libvpx("-pix_fmt", "yuv420p12le"), "at 12 bits"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := decodingDevice(t, c.codec)
			cl := ffmpegClip(t, c.file, 192, 128, 0.2, c.args...)
			track, pkts := packets(t, cl.path)
			v, err := d.NewVideoDecoder(track)
			if err == nil {
				defer v.Close()
				err = v.Decode(pkts[0])
			}
			if err == nil || !strings.Contains(err.Error(), c.names) {
				t.Errorf("decoding: %v, want an error naming %s", err, c.names)
			}
		})
	}
}
