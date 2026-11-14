package gpu

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/testbuild"
)

// passTarget is a size x size dmabuf target of d for passes.
func passTarget(t *testing.T, d *Device, size int) *Target {
	t.Helper()
	mods, err := d.Modifiers(fourcc.ARGB8888)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := d.NewTarget(size, size, fourcc.ARGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tg.Close)
	return tg
}

// fencingDevice is a device with sync_files that hands targets over by
// a fence on their dmabuf, NVIDIA's too: its driver attaches the fence
// as any does, and it is the compositor sampling the buffer that would
// not wait on it.
func fencingDevice(t *testing.T) *Device {
	t.Helper()
	d := syncDevice(t)
	d.implicitFences = true
	return d
}

// stillFencing skips the test on a kernel that attaches no fence to a
// dmabuf, which Publish found out and waits instead.
func stillFencing(t *testing.T, d *Device) {
	t.Helper()
	if !d.implicitFences {
		t.Skipf("the kernel attaches no sync_file to %s's dmabufs", d.Name())
	}
}

// A pass's target handed over by Publish carries a fence on its dmabuf
// that covers the pass, and Publish waits for nothing: a compositor
// sampling the buffer waits on that fence instead. The fence signals
// after the pass was submitted, or the pass had finished when the fence
// was exported, and once it has signalled everything submitted has.
func TestPublishAttachesThePassesFence(t *testing.T) {
	d := fencingDevice(t)
	tg := passTarget(t, d, busySize)
	plane := tg.Dmabuf().Planes[0].FD
	colors := []color.RGBA{{0, 255, 0, 255}, {0, 0, 255, 255}}
	for frame := range 8 {
		since := monotonic()
		heavyPass(t, d, tg, busyDraws, colors[frame%2])
		waits := d.BlockingWaits()
		if err := d.Publish(tg); err != nil {
			t.Fatal(err)
		}
		if got := d.BlockingWaits(); got != waits {
			t.Errorf("frame %d: Publish waited here %d times, want none", frame, got-waits)
		}
		stillFencing(t, d)
		if d.batch != 0 {
			t.Fatalf("frame %d: Publish left the pass unsubmitted", frame)
		}
		handedOut(d, dmabufFence(t, plane), since).signalled(t)
		if done := d.finished(); done != len(d.inflight) {
			t.Errorf("frame %d: the dmabuf's fence signalled with %d of %d submissions finished", frame, done, len(d.inflight))
		}
		if got := readTarget(t, tg).RGBAAt(1, 1); got != colors[frame%2] {
			t.Errorf("frame %d: the published target holds %v, want %v", frame, got, colors[frame%2])
		}
	}
}

// A pass submitted before Publish, by Flush, is covered by the fence
// Publish leaves on the dmabuf: Publish signals with a submission of its
// own, after the pass, and the fence signals after the pass was
// submitted, or the pass had finished when the fence was exported.
func TestPublishCoversPassesAlreadySubmitted(t *testing.T) {
	d := fencingDevice(t)
	tg := passTarget(t, d, busySize)
	since := monotonic()
	heavyPass(t, d, tg, busyDraws, color.RGBA{0, 0, 255, 255})
	if err := d.Flush(); err != nil {
		t.Fatal(err)
	}
	submitted := d.Submissions()
	if err := d.Publish(tg); err != nil {
		t.Fatal(err)
	}
	stillFencing(t, d)
	if got := d.Submissions(); got != submitted+1 {
		t.Errorf("Publish after Flush made %d submissions, want the one that signals", got-submitted)
	}
	handedOut(d, dmabufFence(t, tg.Dmabuf().Planes[0].FD), since).signalled(t)
	if done := d.finished(); done != len(d.inflight) {
		t.Errorf("the dmabuf's fence signalled with %d of %d submissions finished", done, len(d.inflight))
	}
	if got := readTarget(t, tg).RGBAAt(1, 1); got != (color.RGBA{0, 0, 255, 255}) {
		t.Errorf("the published target holds %v, want what the flushed pass drew", got)
	}
}

// Where the consumer cannot be told by a fence (NVIDIA's driver, a
// device without sync_files), Publish hands the target over finished.
func TestPublishWithoutAFenceFinishesThePass(t *testing.T) {
	d := exportingDevice(t)
	d.implicitFences = false
	tg := passTarget(t, d, 64)
	job := heavyPassJob(t, d, tg)
	start := monotonic()
	heavyPass(t, d, tg, job.units, color.RGBA{0, 255, 0, 255})
	if err := d.Publish(tg); err != nil {
		t.Fatal(err)
	}
	job.ran(t, start, monotonic())
	if n := len(d.inflight); n != 0 || d.batch != 0 {
		t.Errorf("%d submissions in flight and batch %v when Publish returned", n, d.batch != 0)
	}
}

// Publish refuses what it cannot hand over, by name, and submits an
// offscreen target's passes without more: it has no consumer.
func TestPublishRefusesMisuse(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 8, 8)
	clearPass(t, d, tg)
	if err := d.Publish(tg); err != nil {
		t.Fatalf("an offscreen target: %v", err)
	}
	if d.batch != 0 {
		t.Error("Publish left an offscreen target's pass unsubmitted")
	}
	p, err := d.Begin(tg, []image.Rectangle{{Max: tg.Size()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Publish(tg); err == nil || !strings.Contains(err.Error(), "pass is open") {
		t.Errorf("during a pass: %v, want an error saying a pass is open", err)
	}
	if err := p.End(); err != nil {
		t.Fatal(err)
	}
	other := openDevice(t)
	if err := other.Publish(tg); err == nil || !strings.Contains(err.Error(), "another device") {
		t.Errorf("another device's target: %v, want an error saying so", err)
	}
	closed, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	for name, tg := range map[string]*Target{"closed": closed, "nil": nil} {
		if err := d.Publish(tg); err == nil || !strings.Contains(err.Error(), "closed target") {
			t.Errorf("a %s target: %v, want an error saying it is closed", name, err)
		}
	}
}

// A frame of a pass and its Publish allocates nothing, with a fence or
// without. Each frame finishes before the next, so the semaphores it
// signals are there to use again whatever the GPU's pace.
func TestPublishDoesNotAllocate(t *testing.T) {
	if testbuild.Race {
		t.Skip("the race detector allocates on its own")
	}
	for _, fenced := range []bool{true, false} {
		name := "waiting"
		if fenced {
			name = "fenced"
		}
		t.Run(name, func(t *testing.T) {
			d := syncDevice(t)
			d.implicitFences = fenced
			tg := passTarget(t, d, 64)
			damage := []image.Rectangle{{Max: tg.Size()}}
			frame := func() {
				p, err := d.Begin(tg, damage)
				if err != nil {
					t.Fatal(err)
				}
				p.Clear(color.RGBA{0, 255, 0, 255})
				if err := p.End(); err != nil {
					t.Fatal(err)
				}
				if err := d.Publish(tg); err != nil {
					t.Fatal(err)
				}
				if err := d.Finish(); err != nil {
					t.Fatal(err)
				}
			}
			for range 4 {
				frame()
			}
			if fenced {
				stillFencing(t, d)
			}
			if n := testing.AllocsPerRun(100, frame); n != 0 {
				t.Errorf("%.1f allocations a frame, want 0", n)
			}
		})
	}
}
