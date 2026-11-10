package gpu

import (
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/media/demux"
)

// drain takes and releases the frames Next has.
func drain(v *VideoDecoder) {
	for f := v.Next(); f != nil; f = v.Next() {
		f.Release()
	}
}

// A player that holds its next packet back while the decoder is Busy
// never has Decode wait for the GPU, whatever the GPU's pace: through a
// whole clip of each codec, hidden frames and reordering included,
// nothing waits here.
func TestDecodeNeverWaitsForACallerThatHeedsBusy(t *testing.T) {
	for _, c := range []struct {
		codec demux.Codec
		args  []string
	}{
		{demux.H264, []string{"-c:v", "libx264", "-bf", "2"}},
		{demux.H265, []string{"-c:v", "libx265", "-x265-params", "log-level=none:bframes=3"}},
		{demux.AV1, libaom("", "-g", "20", "-lag-in-frames", "19")},
		{demux.VP9, libvpx("-g", "20", "-lag-in-frames", "19", "-auto-alt-ref", "1")},
	} {
		t.Run(c.codec.String(), func(t *testing.T) {
			d := decodingDevice(t, c.codec)
			cl := ffmpegClip(t, "a.mp4", 640, 360, 2, c.args...)
			track, pkts := packets(t, cl.path)
			v, err := d.NewVideoDecoder(track)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			if busy, err := v.Busy(); busy || err != nil {
				t.Fatalf("a decoder that has decoded nothing is busy: %v, %v", busy, err)
			}
			waits, held := d.BlockingWaits(), 0
			deadline := time.Now().Add(30 * time.Second)
			for i, p := range pkts {
				for {
					busy, err := v.Busy()
					if err != nil {
						t.Fatal(err)
					}
					if !busy {
						break
					}
					held++
					if time.Now().After(deadline) {
						t.Fatalf("packet %d: the decoder stayed busy", i)
					}
				}
				if err := v.Decode(p); err != nil {
					t.Fatal(err)
				}
				drain(v)
			}
			if got := d.BlockingWaits(); got != waits {
				t.Errorf("decoding %d packets waited here %d times, want none", len(pkts), got-waits)
			}
			t.Logf("%d packets, the decoder busy %d times, %d slots", len(pkts), held, len(v.slots))
			if err := v.idle(); err != nil {
				t.Fatal(err)
			}
			if busy, err := v.Busy(); busy || err != nil {
				t.Errorf("a decoder whose decodes have finished is busy: %v, %v", busy, err)
			}
		})
	}
}

// Decode makes room for the pictures of a caller that does not wait for
// the GPU instead of waiting for it, up to its limit of slots, and
// what it then decodes is still the stream's pictures.
func TestDecodeMakesRoomInsteadOfWaiting(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	cl := ffmpegClip(t, "a.mp4", 640, 360, 0.48, "-c:v", "libx264", "-bf", "2")
	track, pkts := packets(t, cl.path)
	if len(pkts) > decodeSlotLimit {
		t.Fatalf("the clip has %d packets, more than the %d a decoder makes room for", len(pkts), decodeSlotLimit)
	}
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	waits := d.BlockingWaits()
	for _, p := range pkts {
		if err := v.Decode(p); err != nil {
			t.Fatal(err)
		}
		drain(v)
	}
	if got := d.BlockingWaits(); got != waits {
		t.Errorf("decoding %d packets in a row waited here %d times, want none", len(pkts), got-waits)
	}
	if len(v.slots) > decodeSlotLimit {
		t.Errorf("%d slots, at most %d allowed", len(v.slots), decodeSlotLimit)
	}
	t.Logf("%d packets decoded through %d slots", len(pkts), len(v.slots))
	v.Flush()
	drain(v)
	if err := v.Reset(); err != nil {
		t.Fatal(err)
	}
	checkFrames(t, cl, decodeAll(t, v, pkts), 0)
}

// Busy refuses a closed decoder by name.
func TestBusyRefusesAClosedDecoder(t *testing.T) {
	_, v, _ := heldFrame(t, 174, 98)
	v.Close()
	if _, err := v.Busy(); err == nil {
		t.Error("Busy of a closed decoder: no error")
	}
}
