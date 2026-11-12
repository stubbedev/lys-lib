package gpu

import (
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/render"
)

// sharedModifiers are the modifiers a allocates a Buffer in that b both
// renders into and samples; the test skips with none.
func sharedModifiers(t *testing.T, a, b *Device) []fourcc.Modifier {
	t.Helper()
	mods, err := a.ExportModifiers(fourcc.XRGB8888)
	if err != nil {
		t.Fatal(err)
	}
	for _, takes := range []func(fourcc.Format) ([]fourcc.Modifier, error){b.TargetModifiers, b.TextureModifiers, a.TextureModifiers} {
		ms, err := takes(fourcc.XRGB8888)
		if err != nil {
			t.Fatal(err)
		}
		mods = slices.DeleteFunc(mods, func(m fourcc.Modifier) bool { return !slices.Contains(ms, m) })
	}
	if len(mods) == 0 {
		t.Skip("no XRGB8888 modifier one device allocates and both import")
	}
	return mods
}

func clearTarget(t *testing.T, d *Device, tg *Target, c color.RGBA) {
	t.Helper()
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) { p.Clear(c) })
}

// A writer on another device, as a compositor filling a screencast buffer
// is, that refills a buffer while a reader's fence is still on its dmabuf
// waits for that reader: the reader sees the frame it was handed, never
// the next one. The reader samples the buffer in a long pass and hands it
// back as the encoder's conversion does, its fence on the dmabuf and no
// wait; the writer clears the buffer to another colour at once.
func TestARefillWaitsForTheReaderBeforeIt(t *testing.T) {
	a := syncDevice(t)
	if !a.implicitFences {
		t.Skipf("%s hands buffers back by waiting", a.Name())
	}
	b := syncDevice(t)
	w, h := 640, 360
	if isCPUDevice(a) {
		w, h = 64, 64
	}
	buf, err := a.NewBuffer(w, h, fourcc.XRGB8888, sharedModifiers(t, a, b))
	if err != nil {
		t.Fatal(err)
	}
	defer buf.Close()
	tex, err := a.ImportTexture(buf.Dmabuf())
	if err != nil {
		t.Fatal(err)
	}
	defer tex.Close()
	read, err := a.NewOffscreen(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	fill, err := b.ImportTarget(buf.Dmabuf())
	if err != nil {
		t.Fatal(err)
	}
	defer fill.Close()
	frame, next := color.RGBA{0, 255, 0, 255}, color.RGBA{255, 0, 255, 255}
	all := image.Rectangle{Max: image.Pt(w, h)}
	sample := func(draws int) {
		runPass(t, a, read, []image.Rectangle{all}, func(p *Pass) {
			p.Clear(frame)
			for range draws {
				p.DrawTexture(tex, FRect{W: float64(w), H: float64(h)}, all, TransformNormal, 0.5, Mask{})
			}
		})
	}
	job := newSlowJob(t, a, sample)
	pending, raced := 0, 0
	for i := range 8 {
		clearTarget(t, b, fill, frame)
		if err := b.Finish(); err != nil {
			t.Fatal(err)
		}
		start := monotonic()
		sample(job.units)
		if err := a.signalFrame(); err != nil {
			t.Fatal(err)
		}
		if err := a.fenceDmabuf(buf.sync, true); err != nil {
			t.Fatal(err)
		}
		waits := b.BlockingWaits()
		clearTarget(t, b, fill, next)
		if err := b.Flush(); err != nil {
			t.Fatal(err)
		}
		if a.finished() < len(a.inflight) {
			pending++
		}
		if got := b.BlockingWaits(); got != waits {
			t.Errorf("round %d: the refill waited here %d times, want on the GPU", i, got-waits)
		}
		if err := a.Finish(); err != nil {
			t.Fatal(err)
		}
		job.ran(t, start, monotonic())
		if err := b.Finish(); err != nil {
			t.Fatal(err)
		}
		img := readTarget(t, read)
		for _, at := range []image.Point{{0, 0}, {w / 2, h / 2}, {w - 1, h - 1}} {
			if got := img.RGBAAt(at.X, at.Y); !within(got, frame, 2) {
				raced++
				t.Errorf("round %d: the reader saw %v at %v, want the frame it was handed, %v", i, got, at, frame)
				break
			}
		}
	}
	t.Logf("%d of 8 refills submitted with the reader still running, %d raced it", pending, raced)
	if pending == 0 && !isCPUDevice(a) {
		t.Errorf("%s finished every read before the refill was submitted: nothing raced", a.Name())
	}
}

// busyFence is a sync_file, the test's, of a job that keeps d's GPU busy
// for a while: another user's work on a dmabuf, whose fence it carries.
func busyFence(t *testing.T, d *Device) int {
	t.Helper()
	w, h := 3840, 2160
	if isCPUDevice(d) {
		w, h = 64, 64
	}
	tg, err := d.NewOffscreen(w, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tg.Close)
	tex, err := d.NewTexture(fourcc.ARGB8888, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tex.Close)
	if err := tex.Update(image.Rect(0, 0, 1, 1), 4, []byte{0, 0, 255, 255}); err != nil {
		t.Fatal(err)
	}
	all := image.Rectangle{Max: image.Pt(w, h)}
	busy := func(draws int) {
		runPass(t, d, tg, []image.Rectangle{all}, func(p *Pass) {
			for range draws {
				p.DrawTexture(tex, FRect{W: 1, H: 1}, all, TransformNormal, 0.5, Mask{})
			}
		})
	}
	job := newSlowJob(t, d, busy)
	start := monotonic()
	busy(job.units)
	fence := syncFile(t, d)
	job.endsBy(t, start, fence)
	return fence
}

// dmabufUse is a way a device takes a dmabuf from its other users: what
// it does, the dmabuf's fd, whether it writes, and whether it hands the
// dmabuf back to them when done.
type dmabufUse struct {
	name      string
	fd        int
	access    dmabuf.Access
	handsOver bool
	use       func()
}

// dmabufUses are the ways b takes a dmabuf from its other users, the
// buffers a allocates for it and the targets b allocates.
func dmabufUses(t *testing.T, a, b *Device) []dmabufUse {
	t.Helper()
	const w, h = 256, 128
	mods := sharedModifiers(t, a, b)
	newBuffer := func(mods []fourcc.Modifier) *Buffer {
		buf, err := a.NewBuffer(w, h, fourcc.XRGB8888, mods)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(buf.Close)
		return buf
	}
	targetMods, err := b.Modifiers(fourcc.XRGB8888)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := b.NewTarget(w, h, fourcc.XRGB8888, targetMods)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tg.Close)
	imported, err := b.ImportTarget(newBuffer(mods).Dmabuf())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(imported.Close)
	sampled := newBuffer(mods)
	tex, err := b.ImportTexture(sampled.Dmabuf())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tex.Close)
	dst, err := b.NewOffscreen(w, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dst.Close)
	clearTarget(t, b, dst, color.RGBA{0, 0, 255, 255})
	rec := render.NewRecorder(w, h, 1, 1)
	uses := []dmabufUse{
		{"a pass into a target", tg.Dmabuf().Planes[0].FD, dmabuf.Write, false, func() { clearTarget(t, b, tg, color.RGBA{0, 255, 0, 255}) }},
		{"a render into a target", tg.Dmabuf().Planes[0].FD, dmabuf.Write, true, func() {
			rec.ResetRecorder(w, h, 1, 1)
			rec.Clear(render.Rect{W: w, H: h}, render.RGBA(0, 255, 0, 255))
			if err := b.Render(rec.Recording(), tg); err != nil {
				t.Fatal(err)
			}
		}},
		{"a pass into an imported target", imported.sync.fds[0], dmabuf.Write, false, func() { clearTarget(t, b, imported, color.RGBA{0, 255, 0, 255}) }},
		{"a pass sampling an imported texture", sampled.Dmabuf().Planes[0].FD, dmabuf.Read, false, func() {
			runPass(t, b, dst, []image.Rectangle{{Max: dst.Size()}}, func(p *Pass) {
				p.DrawTexture(tex, FRect{W: w, H: h}, image.Rectangle{Max: dst.Size()}, TransformNormal, 1, Mask{})
			})
		}},
	}
	if slices.Contains(mods, fourcc.Linear) {
		lin, err := b.ImportLinear(newBuffer([]fourcc.Modifier{fourcc.Linear}).Dmabuf())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(lin.Close)
		uses = append(uses, dmabufUse{"a copy into a linear buffer", lin.sync.fds[0], dmabuf.Write, false, func() {
			if err := b.CopyToLinear(lin, dst, []image.Rectangle{{Max: dst.Size()}}); err != nil {
				t.Fatal(err)
			}
		}})
	}
	return uses
}

// Every way a device takes a dmabuf from its other users waits, on the
// GPU, for the fence another device's still-running job left on it: the
// submission is queued without a wait here while that fence is pending,
// and finishes after it signals. The device hands targets over by a
// fence, so a Render waits for nothing more;
// TestARenderHandedOverByWaitingWaitsOnlyForItsFrame takes the hand-over
// by waiting.
func TestUsesOfADmabufWaitOnTheGPUForTheFenceOnIt(t *testing.T) {
	a := syncDevice(t)
	b := fencingDevice(t)
	for _, u := range dmabufUses(t, a, b) {
		t.Run(u.name, func(t *testing.T) {
			if err := b.Finish(); err != nil {
				t.Fatal(err)
			}
			fence := busyFence(t, a)
			if fence < 0 {
				t.Skipf("%s finished its job before handing out its fence", a.Name())
			}
			if err := dmabuf.ImportSyncFile(u.fd, fence); err != nil {
				t.Fatal(err)
			}
			waits := b.BlockingWaits()
			u.use()
			done := syncFile(t, b)
			signalled, err := drm.SyncFileSignalled(fence)
			if err != nil {
				t.Fatal(err)
			}
			if got := b.BlockingWaits(); got != waits {
				t.Errorf("the %s waited here %d times, want on the GPU", u.access, got-waits)
			}
			if signalled {
				if !isCPUDevice(a) {
					t.Errorf("%s finished its job before the %s was queued: nothing shows the wait", a.Name(), u.access)
				}
				return
			}
			waitReadable(t, done)
			if signalled, err := drm.SyncFileSignalled(fence); err != nil || !signalled {
				t.Fatalf("the %s finished with the fence on its dmabuf still pending (%v)", u.access, err)
			}
			fenceAt, doneAt := signalTime(t, fence), signalTime(t, done)
			if doneAt < fenceAt {
				t.Errorf("the %s finished at %v, %v before the fence on its dmabuf signalled", u.access, doneAt, fenceAt-doneAt)
			}
		})
	}
}

// A device that hands a target over by waiting, as NVIDIA does, waits
// here once, for the frame it just queued, which waited on the GPU for
// the fence another device's still-running job left on the target's
// dmabuf: the target is handed over finished, after that fence.
func TestARenderHandedOverByWaitingWaitsOnlyForItsFrame(t *testing.T) {
	a := syncDevice(t)
	b := syncDevice(t)
	uses := dmabufUses(t, a, b)
	b.implicitFences = false
	for _, u := range uses {
		if !u.handsOver {
			continue
		}
		t.Run(u.name, func(t *testing.T) {
			if err := b.Finish(); err != nil {
				t.Fatal(err)
			}
			fence := busyFence(t, a)
			if fence < 0 {
				t.Skipf("%s finished its job before handing out its fence", a.Name())
			}
			if err := dmabuf.ImportSyncFile(u.fd, fence); err != nil {
				t.Fatal(err)
			}
			waits := b.BlockingWaits()
			u.use()
			if got := b.BlockingWaits() - waits; got != 1 {
				t.Errorf("the %s waited here %d times, want once, for its frame to hand it over", u.access, got)
			}
			if n := len(b.inflight); n != 0 {
				t.Errorf("the %s handed its target over with %d submissions in flight", u.access, n)
			}
			if signalled, err := drm.SyncFileSignalled(fence); err != nil || !signalled {
				t.Errorf("the %s handed its target over with the fence on its dmabuf still pending (%v)", u.access, err)
			}
		})
	}
}

// A reader waits for a dmabuf's writers, a writer for its readers too:
// a read fence holds back the write intent's sync_file, not the read's.
func TestExportSyncFileHoldsWhatTheUseWaitsFor(t *testing.T) {
	a := syncDevice(t)
	b := syncDevice(t)
	plane := dmabufUses(t, a, b)[0].fd
	fence := busyFence(t, a)
	if fence < 0 {
		t.Skipf("%s finished its job before handing out its fence", a.Name())
	}
	const importSyncFile = 0x40086203
	arg := struct {
		flags uint32
		fd    int32
	}{uint32(dmabuf.Read), int32(fence)}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(plane), importSyncFile, uintptr(unsafe.Pointer(&arg))); errno != 0 {
		t.Fatalf("DMA_BUF_IOCTL_IMPORT_SYNC_FILE of a read fence: %v", errno)
	}
	exported := func(a dmabuf.Access) bool {
		fd, err := dmabuf.ExportSyncFile(plane, a)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		signalled, err := drm.SyncFileSignalled(fd)
		if err != nil {
			t.Fatal(err)
		}
		return signalled
	}
	read, write := exported(dmabuf.Read), exported(dmabuf.Write)
	signalled, err := drm.SyncFileSignalled(fence)
	if err != nil {
		t.Fatal(err)
	}
	if signalled {
		t.Skip("the read fence signalled before the sync_files were checked")
	}
	if !read || write {
		t.Errorf("with a reader running, a read's sync_file is signalled %v and a write's %v, want true and false", read, write)
	}
}

// A device with no sync_file semaphores waits for a dmabuf's fence here
// before the submission that writes it, never past it.
func TestAWriteWithoutSyncFileSemaphoresWaitsHere(t *testing.T) {
	a := syncDevice(t)
	b := syncDevice(t)
	u := dmabufUses(t, a, b)[0]
	fence := busyFence(t, a)
	if fence < 0 {
		t.Skipf("%s finished its job before handing out its fence", a.Name())
	}
	if err := dmabuf.ImportSyncFile(u.fd, fence); err != nil {
		t.Fatal(err)
	}
	b.syncFiles = false
	defer func() { b.syncFiles = true }()
	waits := b.BlockingWaits()
	u.use()
	if signalled, err := drm.SyncFileSignalled(fence); err != nil || !signalled {
		t.Errorf("the fence on the dmabuf is signalled %v (%v) once the write is recorded, want true", signalled, err)
	}
	if b.BlockingWaits() == waits {
		t.Error("the write did not wait here")
	}
	if err := b.Finish(); err != nil {
		t.Fatal(err)
	}
}

// A wait for a dmabuf's fences that cannot be set up fails the
// submission it was for, naming the dmabuf, and goes with it: the next
// submission runs.
func TestAFailedDmabufWaitFailsItsSubmission(t *testing.T) {
	b := syncDevice(t)
	mods, err := b.Modifiers(fourcc.XRGB8888)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := b.NewTarget(16, 16, fourcc.XRGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	fds := tg.sync.fds
	tg.sync.fds = []int{-1}
	clearTarget(t, b, tg, color.RGBA{0, 255, 0, 255})
	tg.sync.fds = fds
	err = b.Flush()
	if err == nil || !strings.Contains(err.Error(), "EXPORT_SYNC_FILE for a write of dmabuf -1") {
		t.Fatalf("the submission after a failed wait: %v, want the export's error", err)
	}
	clearTarget(t, b, tg, color.RGBA{0, 0, 255, 255})
	if err := b.Finish(); err != nil {
		t.Fatalf("the submission after the failed one: %v", err)
	}
	if got := readTarget(t, tg).RGBAAt(0, 0); got != (color.RGBA{0, 0, 255, 255}) {
		t.Errorf("the target holds %v after the second pass", got)
	}
}

// An imported target is published as an exported one is: its fence on
// the dmabuf it was imported from covers the pass that drew it.
func TestPublishFencesAnImportedTarget(t *testing.T) {
	a := syncDevice(t)
	b := syncDevice(t)
	buf, err := a.NewBuffer(64, 64, fourcc.XRGB8888, sharedModifiers(t, a, b))
	if err != nil {
		t.Fatal(err)
	}
	defer buf.Close()
	tg, err := b.ImportTarget(buf.Dmabuf())
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	clearTarget(t, b, tg, color.RGBA{0, 255, 0, 255})
	if err := b.Publish(tg); err != nil {
		t.Fatal(err)
	}
	fence := dmabufFence(t, buf.Dmabuf().Planes[0].FD)
	defer unix.Close(fence)
	waitReadable(t, fence)
	if b.implicitFences {
		if done := b.finished(); done != len(b.inflight) {
			t.Errorf("the imported target's fence signalled with %d of %d submissions finished", done, len(b.inflight))
		}
	}
	img, err := buf.ReadPixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := img.RGBAAt(1, 1); got != (color.RGBA{0, 255, 0, 255}) {
		t.Errorf("the buffer holds %v, want the published pass's green", got)
	}
}

// A compositor on another device that fills an encoder's buffer with the
// next frame as soon as Encode returns does not reach the frame being
// converted, and the conversion does not read a frame the compositor is
// still drawing, in a long pass that ends in the frame's colour: each
// hands the buffer over by its fence alone, and every
// frame decodes as the one the buffer held at its Encode, on every
// backend the device encodes on.
func TestARefilledSourceEncodesTheFrameItHeld(t *testing.T) {
	d, ffmpeg := encodingDevice(t, H264)
	writer := syncDevice(t)
	mods := sharedModifiers(t, d, writer)
	colors := []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}, {255, 255, 255, 255}, {0, 0, 0, 255}, {255, 0, 255, 255}, {0, 255, 255, 255}, {255, 255, 0, 255}}
	for _, b := range encodingBackends(t, d, H264) {
		t.Run(b.kind.String(), func(t *testing.T) {
			cfg := EncoderConfig{Codec: H264, Width: 1920, Height: 1080, FrameRate: 30, KeyInterval: 30, QP: 20}
			buf, err := d.NewBuffer(cfg.Width, cfg.Height, fourcc.XRGB8888, mods)
			if err != nil {
				t.Fatal(err)
			}
			defer buf.Close()
			enc, err := b.open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer closeEncoder(t, enc)
			fill, err := writer.ImportTarget(buf.Dmabuf())
			if err != nil {
				t.Fatal(err)
			}
			defer fill.Close()
			c := &clip{t: t, cfg: cfg, enc: enc, buf: buf}
			src := make([]*image.RGBA, len(colors))
			all := image.Rectangle{Max: fill.Size()}
			draw := func(fills int, col color.RGBA) {
				runPass(t, writer, fill, []image.Rectangle{all}, func(p *Pass) {
					p.Clear(color.RGBA{A: 255})
					for range fills {
						p.FillRect(all, color.RGBA{A: 128}, Mask{})
					}
					p.FillRect(all, color.RGBA{col.R / 255 * 254, col.G / 255 * 254, col.B / 255 * 254, 254}, Mask{})
				})
			}
			job := newSlowJob(t, writer, func(fills int) { draw(fills, colors[0]) })
			publish := func(col color.RGBA) {
				start := monotonic()
				draw(job.units, col)
				if err := writer.Publish(fill); err != nil {
					t.Fatal(err)
				}
				fence := dmabufFence(t, buf.Dmabuf().Planes[0].FD)
				defer unix.Close(fence)
				job.endsBy(t, start, fence)
			}
			publish(colors[0])
			for i, col := range colors {
				src[i] = solid(cfg.Width, cfg.Height, col)
				c.frame(nil)
				publish(colors[(i+1)%len(colors)])
			}
			checkDecodes(t, ffmpeg, c, src)
		})
	}
}
