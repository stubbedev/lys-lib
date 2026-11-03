package gpu

import "testing"

func TestBeginningARenderPassAllocatesNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates on its own")
	}
	d := openDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	if err := d.ensureScratch(tg.s.w, tg.s.h); err != nil {
		t.Fatal(err)
	}
	if err := d.takeFrameBuffers(); err != nil {
		t.Fatal(err)
	}
	if err := tg.s.ready(); err != nil {
		t.Fatal(err)
	}
	cb, err := d.recording()
	if err != nil {
		t.Fatal(err)
	}
	f := &frame{d: d}
	if n := testing.AllocsPerRun(100, func() {
		f.begin(cb, &tg.s)
		d.cmds.CmdEndRenderPass(cb)
	}); n != 0 {
		t.Errorf("beginning a render pass allocated %v times", n)
	}
	d.batch = cb
	if err := d.flushBatch(); err != nil {
		t.Fatal(err)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
}
