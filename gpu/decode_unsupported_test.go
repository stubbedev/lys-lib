package gpu

import (
	"errors"
	"testing"

	"github.com/stubbedev/lys-lib/internal/nal"
	"github.com/stubbedev/lys-lib/media/demux"
)

// A malformed packet is an error that is not ErrNoVideoDecoder: the
// decoder decodes the format, and the stream is broken.
func TestMalformedPacketsAreNotNoVideoDecoder(t *testing.T) {
	for _, c := range []struct {
		codec demux.Codec
		args  []string
		read  error
	}{
		{demux.H264, []string{"-c:v", "libx264"}, nal.ErrH264},
		{demux.H265, []string{"-c:v", "libx265", "-x265-params", "log-level=none"}, nal.ErrHEVC},
	} {
		t.Run(c.codec.String(), func(t *testing.T) {
			d := decodingDevice(t, c.codec)
			cl := ffmpegClip(t, "a.mp4", 160, 144, 0.2, c.args...)
			track, pkts := packets(t, cl.path)
			v, err := d.NewVideoDecoder(track)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			broken := pkts[0]
			broken.Data = broken.Data[:min(len(broken.Data), 7)]
			err = v.Decode(broken)
			if err == nil || errors.Is(err, ErrNoVideoDecoder) {
				t.Errorf("a packet cut inside its first NAL unit: %v, want an error that is not ErrNoVideoDecoder", err)
			}
			t.Logf("the cut packet: %v (reads as %v: %v)", err, c.read, errors.Is(err, c.read))
		})
	}
}
