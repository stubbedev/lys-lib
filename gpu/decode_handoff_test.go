package gpu

import (
	"image"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/media/demux"
	"github.com/stubbedev/lys-lib/testbuild"
)

// drawFrame draws all of f over all of tg in a pass of its own.
func drawFrame(t *testing.T, d *Device, tg *Target, f *VideoFrame) {
	t.Helper()
	size := f.Size()
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
		p.DrawFrame(f, FRect{W: float64(size.X), H: float64(size.Y)}, image.Rectangle{Max: tg.Size()}, TransformNormal, 1, Mask{})
	})
}

// A frame goes from the decode queue to the graphics queue by the
// decoder's timeline semaphore: Next hands it out without waiting for
// its decode, the pass that draws it makes its submission wait for that
// decode on the GPU, and nothing waits here until the caller asks for
// the pass itself, which the decode then is done before. Once the
// decode is known done a pass waits for it no more.
func TestDrawnFrameWaitsForItsDecodeOnTheGPU(t *testing.T) {
	const w, h = 1920, 1080
	d := decodingDevice(t, demux.H264)
	cl := ffmpegClip(t, "a.mp4", w, h, 0.1, "-c:v", "libx264")
	track, pkts := packets(t, cl.path)
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	tg := newTarget(t, d, 64, 36)
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	waits := d.BlockingWaits()
	if err := v.Decode(pkts[0]); err != nil {
		t.Fatal(err)
	}
	v.Flush()
	f := v.Next()
	if f == nil {
		t.Fatal("the first packet completed no frame")
	}
	defer f.Release()
	drawFrame(t, d, tg, f)
	if want := (timelineWait{semaphore: v.done, value: f.seq}); len(d.timelineWaits) != 1 || d.timelineWaits[0] != want {
		t.Fatalf("the pass's submission waits for %v, want the frame's decode %v", d.timelineWaits, want)
	}
	if err := d.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(d.timelineWaits) != 0 {
		t.Errorf("after the submission, the next one still waits for %v", d.timelineWaits)
	}
	if got := d.BlockingWaits(); got != waits {
		t.Errorf("decoding, taking, drawing and submitting a frame waited here %d times, want none", got-waits)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	done, err := f.Decoded()
	if err != nil || !done {
		t.Fatalf("after the pass that drew the frame finished, its decode has finished: %v, %v, want true", done, err)
	}
	drawFrame(t, d, tg, f)
	if len(d.timelineWaits) != 0 {
		t.Errorf("a pass drawing a decoded frame waits for %v", d.timelineWaits)
	}
}

// Decoded answers for a frame the caller holds, and says why not for
// one handed back or of a closed decoder.
func TestDecodedRefusesFramesNotHeld(t *testing.T) {
	_, v, f := heldFrame(t, 174, 98)
	if err := v.wait(f.seq); err != nil {
		t.Fatal(err)
	}
	if done, err := f.Decoded(); err != nil || !done {
		t.Errorf("a frame whose decode was waited for: %v, %v, want true", done, err)
	}
	f.Release()
	if _, err := f.Decoded(); err == nil || !strings.Contains(err.Error(), "does not hold") {
		t.Errorf("a released frame: %v, want an error saying the caller does not hold it", err)
	}
	_, v, f = heldFrame(t, 174, 98)
	v.Close()
	if _, err := f.Decoded(); err == nil || !strings.Contains(err.Error(), "decoder is closed") {
		t.Errorf("a closed decoder's frame: %v, want an error saying so", err)
	}
}

// A decoder closed with a pass that drew its frame still to be
// submitted takes its semaphore out of that submission's waits, every
// decode having finished, and the submission runs.
func TestClosedDecoderLeavesNoWaitBehind(t *testing.T) {
	d, v, f := heldFrame(t, 174, 98)
	tg := newTarget(t, d, 64, 36)
	drawFrame(t, d, tg, f)
	if len(d.timelineWaits) != 1 {
		t.Fatalf("the pass's submission waits for %v, want the frame's decode", d.timelineWaits)
	}
	v.Close()
	if len(d.timelineWaits) != 0 {
		t.Errorf("after its decoder closed, the submission still waits for %v", d.timelineWaits)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
}

// A closed decoder hands out no frame: the ones it had ready went with
// it.
func TestClosedDecoderHandsOutNothing(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	cl := ffmpegClip(t, "a.mp4", 174, 98, 0.2, "-c:v", "libx264")
	track, pkts := packets(t, cl.path)
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Decode(pkts[0]); err != nil {
		t.Fatal(err)
	}
	v.Flush()
	v.Close()
	if f := v.Next(); f != nil {
		t.Errorf("a closed decoder handed out the frame at %v", f.PTS())
	}
}

// Decoding a frame, drawing it and handing it back allocates nothing
// once the decoder has its frames.
func TestDrawnFramesDoNotAllocate(t *testing.T) {
	if testbuild.Race {
		t.Skip("the race detector allocates on its own")
	}
	d := decodingDevice(t, demux.H264)
	cl := ffmpegClip(t, "a.mp4", 160, 144, 4, "-c:v", "libx264", "-bf", "2", "-x264-params", "keyint=250")
	track, pkts := packets(t, cl.path)
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	tg := newTarget(t, d, 160, 144)
	damage := []image.Rectangle{{Max: tg.Size()}}
	i := 0
	step := func() {
		if err := v.Decode(pkts[i]); err != nil {
			t.Fatal(err)
		}
		i++
		for f := v.Next(); f != nil; f = v.Next() {
			p, err := d.Begin(tg, damage)
			if err != nil {
				t.Fatal(err)
			}
			p.DrawFrame(f, FRect{W: 160, H: 144}, damage[0], TransformNormal, 1, Mask{})
			if err := p.End(); err != nil {
				t.Fatal(err)
			}
			f.Release()
		}
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
	}
	for range 20 {
		step()
	}
	if n := testing.AllocsPerRun(len(pkts)-i-1, step); n != 0 {
		t.Errorf("%.1f allocations a frame, want 0", n)
	}
}
