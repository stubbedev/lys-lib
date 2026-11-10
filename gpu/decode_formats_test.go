package gpu

import (
	"errors"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/internal/nal"
	"github.com/stubbedev/lys-lib/media/demux"
)

// Streams past 8 and 10 bits and past 4:2:0 decode bit for bit as
// ffmpeg decodes them, where the device decodes them: 12-bit H.265 and
// VP9, and 4:4:4 H.265 at 8, 10 and 12 bits, an odd size among them.
func TestDecodeDeepAndFullChromaMatchFFmpeg(t *testing.T) {
	x265 := func(format string) [][]string {
		return [][]string{{"-c:v", "libx265", "-pix_fmt", format, "-x265-params", "log-level=none:keyint=25:bframes=3:b-pyramid=1"}}
	}
	for _, c := range []struct {
		name, file string
		codec      demux.Codec
		w, h       int
		samples    videoSamples
		passes     [][]string
	}{
		{"H.265 4:2:0 12-bit", "a.mp4", demux.H265, 192, 144, videoSamples{12, chroma420}, x265("yuv420p12le")},
		{"H.265 4:4:4 8-bit", "a.mp4", demux.H265, 192, 144, videoSamples{8, chroma444}, x265("yuv444p")},
		{"H.265 4:4:4 10-bit, odd size", "a.mp4", demux.H265, 271, 153, videoSamples{10, chroma444}, x265("yuv444p10le")},
		{"H.265 4:4:4 12-bit", "a.mp4", demux.H265, 192, 144, videoSamples{12, chroma444}, x265("yuv444p12le")},
		{"VP9 profile 2 12-bit, hidden alternate references", "c.webm", demux.VP9, 192, 144, videoSamples{12, chroma420}, libvpxTwoPass("-b:v", "200k", "-g", "25", "-auto-alt-ref", "1", "-lag-in-frames", "16", "-pix_fmt", "yuv420p12le")},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := decodingDevice(t, c.codec)
			cl := encodeClip(t, c.file, c.w, c.h, 2, c.samples, c.passes...)
			track, pkts := packets(t, cl.path)
			v, err := d.NewVideoDecoder(track)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			if err := v.Decode(pkts[0]); errors.Is(err, ErrNoVideoDecoder) {
				if !strings.Contains(err.Error(), "vkGetPhysicalDeviceVideoCapabilitiesKHR") {
					t.Fatalf("a refusal that names no capability query: %v", err)
				}
				t.Skipf("%s decodes no %s %s: %v", d.Name(), c.samples, c.codec, err)
			}
			if err := v.Reset(); err != nil {
				t.Fatal(err)
			}
			checkFrames(t, cl, decodeAll(t, v, pkts), 0)
			if v.shape.samples != c.samples {
				t.Errorf("decoded as %s, the stream is %s", v.shape.samples, c.samples)
			}
		})
	}
}

// A stream in a format the device does not decode, or says it decodes
// and nothing has verified, is ErrNoVideoDecoder naming the format and
// the capability query that answered for the device, with the query's
// result where it refused: H.264 beyond 8-bit 4:2:0, 4:2:2 H.265, AV1
// and VP9 beyond 4:2:0.
func TestRefusedFormatsNameTheCapabilityQuery(t *testing.T) {
	for _, c := range []struct {
		name, file string
		codec      demux.Codec
		args       []string
		names      string
	}{
		{"H.264 High 10", "a.mp4", demux.H264, []string{"-c:v", "libx264", "-pix_fmt", "yuv420p10le"}, "profile 110 at 10-bit 4:2:0"},
		{"H.264 High 4:2:2", "a.mp4", demux.H264, []string{"-c:v", "libx264", "-pix_fmt", "yuv422p"}, "profile 122 at 8-bit 4:2:2"},
		{"H.264 High 4:4:4", "a.mp4", demux.H264, []string{"-c:v", "libx264", "-pix_fmt", "yuv444p"}, "profile 244 at 8-bit 4:4:4"},
		{"H.265 4:2:2 10-bit", "a.mp4", demux.H265, []string{"-c:v", "libx265", "-pix_fmt", "yuv422p10le", "-x265-params", "log-level=none"}, "profile 4 at 10-bit 4:2:2"},
		{"AV1 High 4:4:4", "b.mp4", demux.AV1, libaom("", "-pix_fmt", "yuv444p"), "profile 1 at 8-bit 4:4:4"},
		{"AV1 Professional 4:2:2", "b.mp4", demux.AV1, libaom("", "-pix_fmt", "yuv422p"), "profile 2 at 8-bit 4:2:2"},
		{"VP9 profile 1 4:4:4", "c.webm", demux.VP9, libvpx("-pix_fmt", "yuv444p"), "profile 1 at 8-bit 4:4:4"},
		{"VP9 profile 3 4:2:2 10-bit", "c.webm", demux.VP9, libvpx("-pix_fmt", "yuv422p10le"), "profile 3 at 10-bit 4:2:2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := decodingDevice(t, c.codec)
			cl := ffmpegClip(t, c.file, 192, 144, 0.2, c.args...)
			track, pkts := packets(t, cl.path)
			v, err := d.NewVideoDecoder(track)
			if err == nil {
				defer v.Close()
				err = v.Decode(pkts[0])
			}
			const query = "vkGetPhysicalDeviceVideoCapabilitiesKHR"
			if !errors.Is(err, ErrNoVideoDecoder) || !strings.Contains(err.Error(), c.names) || !strings.Contains(err.Error(), query) {
				t.Fatalf("%v, want ErrNoVideoDecoder naming %q and %s", err, c.names, query)
			}
			refused := strings.Contains(err.Error(), query+" answers VK_ERROR_VIDEO_PROFILE_")
			unverified := strings.Contains(err.Error(), query+" succeeds") && strings.Contains(err.Error(), "verified on no device")
			if !refused && !unverified {
				t.Errorf("%v, want the query's refusal by name, or its success and that the format is unverified", err)
			}
			t.Log(err)
		})
	}
}

// A stream whose pictures have no format at all (monochrome) is
// ErrNoVideoDecoder too, with what the parser read of it: a caller
// tells it from a malformed stream, which is not, with errors.Is.
func TestUnsupportedFormatsAreNoVideoDecoder(t *testing.T) {
	for _, c := range []struct {
		name  string
		codec demux.Codec
		args  []string
		read  error
	}{
		{"H.264 monochrome", demux.H264, []string{"-c:v", "libx264", "-pix_fmt", "gray"}, nal.ErrH264},
		{"H.265 monochrome", demux.H265, []string{"-c:v", "libx265", "-pix_fmt", "gray", "-x265-params", "log-level=none"}, nal.ErrHEVC},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := decodingDevice(t, c.codec)
			cl := ffmpegClip(t, "a.mp4", 160, 144, 0.2, c.args...)
			track, pkts := packets(t, cl.path)
			v, err := d.NewVideoDecoder(track)
			if err == nil {
				defer v.Close()
				err = v.Decode(pkts[0])
			}
			if !errors.Is(err, ErrNoVideoDecoder) || !errors.Is(err, c.read) || !strings.Contains(err.Error(), "chroma_format_idc 0") {
				t.Errorf("%v, want ErrNoVideoDecoder wrapping %v and naming chroma_format_idc 0", err, c.read)
			}
		})
	}
}

// A profile the device says it decodes, of a format nothing has
// verified, is refused all the same, saying so, and no session is made
// for it.
func TestAnUnverifiedFormatIsRefusedThoughTheDeviceAcceptsIt(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	cl := ffmpegClip(t, "a.mp4", 192, 144, 0.2, "-c:v", "libx264")
	track, pkts := packets(t, cl.path)
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if err := v.Decode(pkts[0]); err != nil {
		t.Fatal(err)
	}
	shape := v.shape
	shape.unverified = "decoding this test's stream"
	err = v.shapeFor(shape)
	if !errors.Is(err, ErrNoVideoDecoder) || !strings.Contains(err.Error(), "vkGetPhysicalDeviceVideoCapabilitiesKHR succeeds") || !strings.Contains(err.Error(), "decoding this test's stream is verified on no device") {
		t.Errorf("%v, want ErrNoVideoDecoder saying the query succeeds and the format is unverified", err)
	}
	if v.session != 0 {
		t.Error("a session was made for the unverified format")
	}
}
