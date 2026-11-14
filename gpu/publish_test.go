package gpu

import (
	"errors"
	"image"
	"image/color"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// heavyList is a frame that keeps a real GPU busy with units rounded
// rectangles in col over a clear in col.
func heavyList(rec *render.Canvas, w, h, units int, col render.Color) *render.List {
	rec.ResetRecorder(w, h, 1, 1)
	all := render.Rect{W: w, H: h}
	rec.Clear(all, col)
	for range units {
		rec.RoundedRect(all, 8, col)
	}
	return rec.Recording()
}

// dmabufFence is what a consumer using implicit synchronization waits
// on before reading the dmabuf fd: DMA_BUF_IOCTL_EXPORT_SYNC_FILE.
func dmabufFence(t *testing.T, fd int) int {
	t.Helper()
	return exportFence(t, fd, dmabuf.Read)
}

// exportFence is what a use for a of the dmabuf fd waits on, the test's.
func exportFence(t *testing.T, fd int, a dmabuf.Access) int {
	t.Helper()
	fence, err := dmabuf.ExportSyncFile(fd, a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fence) })
	return fence
}

func isCPUDevice(d *Device) bool {
	var props vk.PhysicalDeviceProperties
	d.cmds.GetPhysicalDeviceProperties(d.phys, &props)
	return props.DeviceType == vk.PhysicalDeviceTypeCpu
}

// A frame handed to a consumer on another device is whole when the
// dmabuf's fence says it is: what a compositor sampling a window's buffer
// waits on. The fence signals after the frame was submitted, or the
// frame had finished when it was exported, and the consumer samples the
// frame behind it.
func TestARenderedDmabufCarriesItsFrame(t *testing.T) {
	a := syncDevice(t)
	b := syncDevice(t)
	if !a.implicitFences {
		t.Skipf("%s hands frames over by waiting", a.Name())
	}
	mods, err := a.Modifiers(fourcc.ARGB8888)
	if err != nil {
		t.Fatal(err)
	}
	tex, err := b.TextureModifiers(fourcc.ARGB8888)
	if err != nil {
		t.Fatal(err)
	}
	mods = slices.DeleteFunc(mods, func(m fourcc.Modifier) bool { return !slices.Contains(tex, m) })
	if len(mods) == 0 {
		t.Skip("no modifier one device exports and the other samples")
	}
	const size = busySize
	src, err := a.NewTarget(size, size, fourcc.ARGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	tx, err := b.ImportTexture(src.Dmabuf())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	dst, err := b.NewOffscreen(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	rec := render.NewRecorder(size, size, 1, 1)
	for frame := range 8 {
		want := color.RGBA{0, 255, 0, 255}
		col := render.RGBA(0, 255, 0, 255)
		if frame%2 == 1 {
			want, col = color.RGBA{0, 0, 255, 255}, render.RGBA(0, 0, 255, 255)
		}
		since := monotonic()
		if err := a.Render(heavyList(rec, size, size, busyDraws, col), src); err != nil {
			t.Fatal(err)
		}
		plane := src.Dmabuf().Planes[0].FD
		fence := dmabufFence(t, plane)
		frameFence := handedOut(a, fence, since)
		signalled, err := drm.SyncFileSignalled(fence)
		if err != nil {
			t.Fatal(err)
		}
		if !signalled {
			pfd := []unix.PollFd{{Fd: int32(plane), Events: unix.POLLIN}}
			if n, err := unix.Poll(pfd, 0); err != nil || n != 0 {
				t.Errorf("frame %d: the dmabuf polls ready (%d, %v) with its fence pending", frame, n, err)
			}
		}
		if err := b.WaitSyncFile(dupFD(t, fence)); err != nil {
			t.Fatal(err)
		}
		runPass(t, b, dst, []image.Rectangle{{Max: dst.Size()}}, func(p *Pass) {
			p.DrawTexture(tx, FRect{X: 1, Y: 1, W: 1, H: 1}, image.Rect(0, 0, 1, 1), TransformNormal, 1, Mask{})
		})
		if got := readTarget(t, dst).RGBAAt(0, 0); got != want {
			t.Errorf("frame %d: the consumer sampled %v, want %v", frame, got, want)
		}
		frameFence.signalled(t)
	}
}

// Frames ahead of the GPU do not share memory: each draws its own
// primitives, gradient stops and sprite pixels however late it runs.
func TestFramesInFlightKeepTheirOwnMemory(t *testing.T) {
	d := openDevice(t)
	const size, frames = 64, 12
	targets := make([]*Target, frames)
	rec := render.NewRecorder(size, size, 1, 1)
	for i := range targets {
		tg, err := d.NewOffscreen(size, size)
		if err != nil {
			t.Fatal(err)
		}
		defer tg.Close()
		targets[i] = tg
		v := uint8(20 * (i + 1))
		rec.ResetRecorder(size, size, 1, 1)
		rec.Clear(rec.Rect(), render.RGBA(v, 0, 0, 255))
		sprite := image.NewRGBA(image.Rect(0, 0, 8, 8))
		for p := 0; p < len(sprite.Pix); p += 4 {
			sprite.Pix[p+1], sprite.Pix[p+3] = v, 255
		}
		rec.DrawImage(sprite, 32, 32)
		stop := render.RGBA(0, 0, v, 255)
		rec.PaintGradient(render.Rect{X: 8, Y: 8, W: 8, H: 8}, render.Corners{}, render.Gradient{Stops: []render.GradientStop{{Pos: 0, Color: stop}, {Pos: 1, Color: stop}}})
		err = d.Render(rec.Recording(), tg)
		for errors.Is(err, ErrBusy) {
			if err := d.waitOldest(); err != nil {
				t.Fatal(err)
			}
			err = d.Render(rec.Recording(), tg)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if d.frameBufferCount > maxFrameBuffers {
		t.Errorf("%d segments' buffers, at most %d allowed", d.frameBufferCount, maxFrameBuffers)
	}
	for i, tg := range targets {
		v := uint8(20 * (i + 1))
		px := readTarget(t, tg)
		if got, want := px.RGBAAt(1, 1), (color.RGBA{v, 0, 0, 255}); got != want {
			t.Errorf("frame %d's clear drew %v, want %v", i, got, want)
		}
		if got, want := px.RGBAAt(35, 35), (color.RGBA{0, v, 0, 255}); got != want {
			t.Errorf("frame %d's sprite drew %v, want %v", i, got, want)
		}
		if got, want := px.RGBAAt(11, 11), (color.RGBA{0, 0, v, 255}); got != want {
			t.Errorf("frame %d's gradient drew %v, want %v", i, got, want)
		}
	}
}

// Where the consumer cannot be told by a fence, Render still hands the
// target over finished.
func TestAFrameWithoutAFenceIsFinishedWhenRenderReturns(t *testing.T) {
	d := exportingDevice(t)
	d.implicitFences = false
	mods, err := d.Modifiers(fourcc.ARGB8888)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := d.NewTarget(64, 64, fourcc.ARGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	rec := render.NewRecorder(64, 64, 1, 1)
	draw := func(units int) {
		if err := d.Render(heavyList(rec, 64, 64, units, render.RGBA(0, 255, 0, 255)), tg); err != nil {
			t.Fatal(err)
		}
	}
	job := newSlowJob(t, d, draw)
	start := monotonic()
	draw(job.units)
	job.ran(t, start, monotonic())
	if n := len(d.inflight); n != 0 {
		t.Errorf("%d submissions in flight when Render returned", n)
	}
}

// A sync_file covers what came before it and nothing after, as the
// kernel sees it on a driver that exports real ones: each signals after
// the pass it follows was submitted (or that pass had finished when it
// was handed out), the newer cannot signal before the older, and one that
// waits on another device's fence signals after it.
func TestSyncFilesSignalInTheOrderOfTheirWork(t *testing.T) {
	a := syncDevice(t)
	b := syncDevice(t)
	if isCPUDevice(a) {
		t.Skipf("%s signals on the CPU: nothing here to observe", a.Name())
	}
	ta, err := a.NewOffscreen(busySize, busySize)
	if err != nil {
		t.Fatal(err)
	}
	defer ta.Close()
	tb, err := b.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tb.Close()
	green := color.RGBA{0, 255, 0, 255}
	since1 := monotonic()
	heavyPass(t, a, ta, busyDraws, green)
	first := syncFile(t, a)
	older := handedOut(a, first, since1)
	since2 := monotonic()
	heavyPass(t, a, ta, busyDraws, green)
	second := syncFile(t, a)
	newer := handedOut(a, second, since2)
	if first < 0 || second < 0 {
		t.Fatalf("%s exported -1 for a pass's sync_file (%d, %d): nothing here to order", a.Name(), first, second)
	}
	if err := b.WaitSyncFile(dupFD(t, second)); err != nil {
		t.Fatal(err)
	}
	clearPass(t, b, tb)
	third := syncFile(t, b)
	if third < 0 {
		t.Fatalf("%s exported -1 for the sync_file of a pass waiting on another device's", b.Name())
	}
	t1, ownOlder := older.signalled(t)
	t2, ownNewer := newer.signalled(t)
	t3 := signalTime(t, third)
	if !ownNewer {
		return
	}
	if ownOlder && t2 < t1 {
		t.Errorf("the newer pass's fence signalled at %v, before the older's at %v", t2, t1)
	}
	if t3 < t2 {
		t.Errorf("the waiting device's fence signalled at %v, before the fence it waits on at %v", t3, t2)
	}
}
