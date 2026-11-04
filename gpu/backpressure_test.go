package gpu

import (
	"errors"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/render"
)

func TestRenderRefusesAFrameWithoutWaitingWhenTheGPUIsBehind(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 16, 16)
	rec := render.NewRecorder(16, 16, 1, 1)
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	spare := d.spareFrameBuffers
	count := d.frameBufferCount
	never, err := d.spareFence()
	if err != nil {
		t.Fatal(err)
	}
	d.spareFrameBuffers = nil
	d.frameBufferCount = maxFrameBuffers
	d.inflight = append(d.inflight, inflight{seq: d.submitted + 1, fence: never})
	defer func() {
		d.inflight = d.inflight[:0]
		d.spareFences = append(d.spareFences, never)
		d.spareFrameBuffers, d.frameBufferCount = spare, count
	}()
	waits, frames := d.BlockingWaits(), d.images.frames
	if err := d.Render(rec.Recording(), tg); !errors.Is(err, ErrBusy) {
		t.Errorf("Render with every segment in flight: %v, want ErrBusy", err)
	}
	if n := d.BlockingWaits() - waits; n != 0 {
		t.Errorf("refusing a frame made %d blocking waits", n)
	}
	if d.images.frames != frames {
		t.Error("a refused frame started")
	}
}

func TestARunOfSmallSegmentsAloneDropsALargeStagingBuffer(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 16, 16)
	rec := render.NewRecorder(16, 16, 1, 1)
	big := func() {
		t.Helper()
		img := image.NewRGBA(image.Rect(0, 0, 600, 600))
		drawImage(rec, render.NewStillImage(img))
		renderImage(t, d, tg, rec)
	}
	small := func() {
		t.Helper()
		drawImage(rec, render.NewStillImage(rasterIn(make([]byte, 8*8*4), red)))
		renderImage(t, d, tg, rec)
	}
	big()
	large := heldStaging(d)
	for range stagingShrinkAfter - 1 {
		small()
	}
	if n := heldStaging(d); n != large {
		t.Errorf("staging went from %d to %d bytes before the run ended", large, n)
	}
	big()
	for range stagingShrinkAfter - 1 {
		small()
	}
	if n := heldStaging(d); n != large {
		t.Errorf("a large upload did not restart the run: %d bytes, want %d", n, large)
	}
}

func TestAFrameAsksTheFencesOnce(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 16, 16)
	rec := render.NewRecorder(16, 16, 1, 1)
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	before := d.reaps
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	if n := d.reaps - before; n != 1 {
		t.Errorf("a one-segment frame reaped %d times, want 1", n)
	}
}

func TestAWaitForAHungGPUEndsInAnError(t *testing.T) {
	d := openDevice(t)
	never, err := d.spareFence()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d.spareFences = append(d.spareFences, never) }()
	d.waitLimit = time.Millisecond
	if err := d.waitFence(never); err == nil {
		t.Error("a wait for a fence that never signals reported no error")
	} else if !strings.Contains(err.Error(), "1ms") {
		t.Errorf("the error %q does not say how long it waited", err)
	}
}
