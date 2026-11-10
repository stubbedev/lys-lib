package gpu

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/third_party/vk"
)

// A submission that fails takes what it was to wait for and signal with
// it: the next one waits on no fence meant for work that never ran,
// here a timeline semaphore that does not even exist, and signals for
// none, and the commands the failed one carried are gone.
func TestAFailedSubmissionTakesItsWaitsWithIt(t *testing.T) {
	d := lavapipeSyncDevice(t)
	tg := newTarget(t, d, 8, 8)
	all := []image.Rectangle{{Max: tg.Size()}}
	clearPass(t, d, tg)
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	fence, err := d.spareSemaphore()
	if err != nil {
		t.Fatal(err)
	}
	const absent = vk.Semaphore(0xdead)
	d.waits = append(d.waits, fence)
	d.awaitTimeline(timelineWait{semaphore: absent, value: 7})
	d.signalTimeline(timelineWait{semaphore: absent, value: 8})
	runPass(t, d, tg, all, func(p *Pass) { p.Clear(color.RGBA{255, 0, 0, 255}) })
	submitted := d.Submissions()
	d.fault = vk.ErrorOutOfDeviceMemory
	if err := d.Flush(); err == nil || !strings.Contains(err.Error(), "vkQueueSubmit") {
		t.Fatalf("a submission vkQueueSubmit refuses: %v, want its error", err)
	}
	if len(d.waits) != 0 || len(d.timelineWaits) != 0 || len(d.timelineSignals) != 0 {
		t.Fatalf("after a failed submission the next one still waits on %v and %v and signals %v", d.waits, d.timelineWaits, d.timelineSignals)
	}
	if d.batch != 0 || d.Submissions() != submitted {
		t.Errorf("the failed submission left a batch open (%v) or counted (%d, was %d)", d.batch != 0, d.Submissions(), submitted)
	}
	runPass(t, d, tg, all, func(p *Pass) { p.Clear(color.RGBA{0, 255, 0, 255}) })
	if err := d.Finish(); err != nil {
		t.Fatalf("the submission after a failed one: %v", err)
	}
	if got := readTarget(t, tg).RGBAAt(0, 0); got != (color.RGBA{0, 255, 0, 255}) {
		t.Errorf("the target holds %v, want what the pass after the failed submission drew", got)
	}
}
