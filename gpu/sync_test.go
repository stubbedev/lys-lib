package gpu

import (
	"errors"
	"image"
	"image/color"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

func monotonic() time.Duration {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		panic(err)
	}
	return time.Duration(ts.Nano())
}

func syncDevice(t *testing.T) *Device {
	t.Helper()
	d := exportingDevice(t)
	if !d.SyncFiles() {
		t.Skipf("%s has no sync_file semaphores", d.Name())
	}
	return d
}

func heavyPass(t *testing.T, d *Device, tg *Target, last color.RGBA) {
	t.Helper()
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
		for i := range 200 {
			p.FillRect(image.Rectangle{Max: tg.Size()}, color.RGBA{uint8(i), 0, 0, 255}, Mask{})
		}
		p.FillRect(image.Rect(0, 0, 4, 4), last, Mask{})
	})
}

func waitReadable(t *testing.T, fd int) {
	t.Helper()
	pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(pfd, 10000)
	if err != nil || n != 1 {
		t.Fatalf("poll on the sync_file: %d, %v", n, err)
	}
}

func TestSyncFileSignalsOnceThePassFinishes(t *testing.T) {
	d := syncDevice(t)
	if _, ok, err := d.FrameDone(); ok || (err != nil && !errors.Is(err, ErrNoFrameTime)) {
		t.Fatalf("FrameDone before any pass: %v, %v", ok, err)
	}
	tg, err := d.NewOffscreen(2048, 2048)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	before := monotonic()
	heavyPass(t, d, tg, color.RGBA{0, 255, 0, 255})
	fd, err := d.SyncFile()
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	waitReadable(t, fd)
	signalled, ok, err := drm.SyncFileSignalTime(fd)
	if err != nil || !ok {
		t.Fatalf("the sync_file's signal time: %v, %v", ok, err)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	after := monotonic()
	var props vk.PhysicalDeviceProperties
	d.cmds.GetPhysicalDeviceProperties(d.phys, &props)
	if props.DeviceType == vk.PhysicalDeviceTypeCpu {
		t.Logf("%s signals its sync_files on the CPU, at %v: not checked", d.Name(), signalled)
	} else if signalled < before || signalled > after {
		t.Errorf("the sync_file signalled at %v, outside the pass's %v to %v", signalled, before, after)
	}
	if got := readTarget(t, tg).RGBAAt(1, 1); got != (color.RGBA{0, 255, 0, 255}) {
		t.Errorf("the pass drew %v", got)
	}
	done, ok, err := d.FrameDone()
	if errors.Is(err, ErrNoFrameTime) {
		t.Logf("%s: %v", d.Name(), err)
		return
	}
	if err != nil || !ok {
		t.Fatalf("FrameDone after Finish: %v, %v", ok, err)
	}
	if done < before || done > after {
		t.Errorf("FrameDone %v outside the pass's %v to %v", done, before, after)
	}
	t.Logf("the pass finished %v after submit", done-before)
}

func TestWaitSyncFileOrdersAnotherDevicesWork(t *testing.T) {
	a := syncDevice(t)
	b := syncDevice(t)
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
	src, err := a.NewTarget(2048, 2048, fourcc.ARGB8888, mods)
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
	for _, want := range []color.RGBA{{0, 255, 0, 255}, {0, 0, 255, 255}} {
		heavyPass(t, a, src, want)
		fd, err := a.SyncFile()
		if err != nil {
			t.Fatal(err)
		}
		mine, err := unix.Dup(fd)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.WaitSyncFile(fd); err != nil {
			t.Fatal(err)
		}
		runPass(t, b, dst, []image.Rectangle{{Max: dst.Size()}}, func(p *Pass) {
			p.DrawTexture(tx, FRect{X: 1, Y: 1, W: 1, H: 1}, image.Rect(0, 0, 1, 1), TransformNormal, 1, Mask{})
		})
		if got := readTarget(t, dst).RGBAAt(0, 0); got != want {
			t.Errorf("the waiting device sampled %v, want %v", got, want)
		}
		signalled, ok, err := drm.SyncFileSignalTime(mine)
		unix.Close(mine)
		if err != nil || !ok {
			t.Fatalf("the sync_file's signal time: %v, %v", ok, err)
		}
		if done, ok, err := b.FrameDone(); err == nil && ok && done < signalled {
			t.Errorf("the waiting pass finished at %v, before the fence it waited on signalled at %v", done, signalled)
		}
	}
}

func TestWaitSyncFileTakesTheFD(t *testing.T) {
	d := syncDevice(t)
	if err := d.WaitSyncFile(-1); err != nil {
		t.Fatalf("an already signalled fence: %v", err)
	}
	tg, err := d.NewOffscreen(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) { p.Clear(color.RGBA{0, 0, 0, 255}) })
	var p [2]int
	if err := unix.Pipe(p[:]); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[1])
	if err := d.WaitSyncFile(p[0]); err == nil {
		t.Error("a pipe imported as a sync_file")
	}
	if _, err := unix.FcntlInt(uintptr(p[0]), unix.F_GETFD, 0); err != unix.EBADF {
		t.Errorf("the refused fd is still open: %v", err)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
}

func lavapipeSyncDevice(t *testing.T) *Device {
	t.Helper()
	d := openDevice(t)
	if !d.SyncFiles() {
		t.Skipf("%s has no sync_file semaphores", d.Name())
	}
	return d
}

func TestWaitSyncFileOfASignalledFenceHoldsNoSemaphore(t *testing.T) {
	d := lavapipeSyncDevice(t)
	tg, err := d.NewOffscreen(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) { p.Clear(color.RGBA{A: 255}) })
	fd, err := d.SyncFile()
	if err != nil {
		t.Fatal(err)
	}
	waitReadable(t, fd)
	if err := d.WaitSyncFile(fd); err != nil {
		t.Fatal(err)
	}
	if err := d.WaitSyncFile(-1); err != nil {
		t.Fatal(err)
	}
	if len(d.waits) != 0 {
		t.Errorf("%d semaphores wait on fences already signalled", len(d.waits))
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != unix.EBADF {
		t.Errorf("the signalled fd is still open: %v", err)
	}
}

// Frame after frame, what each frame exports the next waits on, drawing
// through the same semaphores: every wait really does order, or the
// colors would land out of order, and the semaphores do not pile up.
func TestSemaphoresServeFrameAfterFrame(t *testing.T) {
	d := lavapipeSyncDevice(t)
	tg, err := d.NewOffscreen(64, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	all := []image.Rectangle{{Max: tg.Size()}}
	for frame := range 3 * queryRing {
		want := color.RGBA{uint8(frame), 0, 0, 255}
		runPass(t, d, tg, all, func(p *Pass) {
			for i := range 20 {
				p.FillRect(all[0], color.RGBA{uint8(i), 0, 0, 255}, Mask{})
			}
			p.FillRect(all[0], want, Mask{})
		})
		fd, err := d.SyncFile()
		if err != nil {
			t.Fatal(err)
		}
		if frame%16 == 0 {
			if err := d.Finish(); err != nil {
				t.Fatal(err)
			}
			if got := readTarget(t, tg).RGBAAt(3, 3); got != want {
				t.Fatalf("frame %d drew %v, want %v", frame, got, want)
			}
		}
		if err := d.WaitSyncFile(fd); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if n := len(d.spareSemaphores) + len(d.heldSemaphores); n > 2*queryRing {
		t.Errorf("%d semaphores for at most %d submissions in flight", n, queryRing)
	}
}

func TestASteadyFrameCreatesNoSemaphoreOrFence(t *testing.T) {
	d := lavapipeSyncDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	all := []image.Rectangle{{Max: tg.Size()}}
	for range 8 {
		runPass(t, d, tg, all, func(p *Pass) { p.Clear(color.RGBA{A: 255}) })
		fd, err := d.SyncFile()
		if err != nil {
			t.Fatal(err)
		}
		if err := d.WaitSyncFile(fd); err != nil {
			t.Fatal(err)
		}
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
	}
	sems, fences := len(d.spareSemaphores), len(d.spareFences)
	if sems > 2 || fences > 2 {
		t.Errorf("idle after every frame, the device holds %d semaphores and %d fences", sems, fences)
	}
}

func TestFrameDoneReadsTheLastPassOnce(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	all := []image.Rectangle{{Max: tg.Size()}}
	if !d.calibrated {
		t.Skip("no calibrated timestamps")
	}
	var last time.Duration
	for range 2 * queryRing {
		runPass(t, d, tg, all, func(p *Pass) { p.Clear(color.RGBA{A: 255}) })
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
		at, ok, err := d.FrameDone()
		if err != nil || !ok {
			t.Fatalf("FrameDone after Finish: %v, %v", ok, err)
		}
		if at < last {
			t.Fatalf("frame finished at %v, before the previous at %v", at, last)
		}
		last = at
	}
}

func clearPass(t *testing.T, d *Device, tg *Target) {
	t.Helper()
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) { p.Clear(color.RGBA{A: 255}) })
}

func syncFile(t *testing.T, d *Device) int {
	t.Helper()
	fd, err := d.SyncFile()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

// Once SyncFile is in use, a frame's fences come from its pass: no
// submission of their own, however many surfaces ask, and each caller's
// fd stands alone.
func TestSyncFilesComeFromThePassTheyFollow(t *testing.T) {
	d := lavapipeSyncDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	clearPass(t, d, tg)
	syncFile(t, d)
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	clearPass(t, d, tg)
	if len(d.inflight) != 1 {
		t.Fatalf("%d submissions after a pass", len(d.inflight))
	}
	first := syncFile(t, d)
	second := syncFile(t, d)
	third, err := d.SyncFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(third); err != nil {
		t.Fatal(err)
	}
	if n := len(d.inflight); n > 1 {
		t.Errorf("%d submissions for a pass and three sync_files, want the pass's only", n)
	}
	if first == second || first == third || second == third {
		t.Errorf("sync_files share a descriptor: %d, %d, %d", first, second, third)
	}
	waitReadable(t, first)
	waitReadable(t, second)
}

// A sync_file taken after a pass that has finished, and after the fence
// that answered for it was reaped, still works, and the semaphore comes
// back unsignalled for the frames after it.
func TestASyncFileAfterTheFrameFinished(t *testing.T) {
	d := lavapipeSyncDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	clearPass(t, d, tg)
	syncFile(t, d)
	for range 2 * queryRing {
		clearPass(t, d, tg)
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(d.spareSemaphores, d.frameSignal) {
			t.Fatal("the signalled semaphore of an unexported pass is among the spares")
		}
		waitReadable(t, syncFile(t, d))
	}
}

// A pass nobody asks about stops the next ones signalling, consuming
// what it signalled, and asking again starts over.
func TestPassesWithNoSyncFileStopSignalling(t *testing.T) {
	d := lavapipeSyncDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	clearPass(t, d, tg)
	syncFile(t, d)
	clearPass(t, d, tg)
	if d.frameSignal == 0 {
		t.Fatal("a pass after SyncFile signals nothing")
	}
	clearPass(t, d, tg)
	if d.frameSignal != 0 {
		t.Error("a pass after an unasked one still signals")
	}
	clearPass(t, d, tg)
	waitReadable(t, syncFile(t, d))
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
}

// A newer submission is not covered by an older fence, and pending waits
// and uploads are carried by the fence that covers them.
func TestASyncFileCoversWhatCameBeforeIt(t *testing.T) {
	d := lavapipeSyncDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	tx, err := d.NewTexture(fourcc.ARGB8888, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	clearPass(t, d, tg)
	syncFile(t, d)
	if !d.lastSyncFileSet {
		t.Fatal("the sync_file is not kept for the next caller")
	}
	clearPass(t, d, tg)
	if d.lastSyncFileSet {
		t.Error("a new pass left the older sync_file answering")
	}
	syncFile(t, d)
	if err := tx.Update(image.Rect(0, 0, 2, 2), 8, make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	syncFile(t, d)
	if d.batch != 0 {
		t.Error("a sync_file left an upload unsubmitted")
	}
	wait := syncFile(t, d)
	dup, err := unix.FcntlInt(uintptr(wait), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	clearPass(t, d, tg)
	before := len(d.inflight)
	d.waits = append(d.waits, mustSemaphore(t, d, dup))
	syncFile(t, d)
	if len(d.waits) != 0 {
		t.Errorf("a sync_file left %d waits pending (%d submissions before it)", len(d.waits), before)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
}

func mustSemaphore(t *testing.T, d *Device, fd int) vk.Semaphore {
	t.Helper()
	s, err := d.spareSemaphore()
	if err != nil {
		t.Fatal(err)
	}
	info := vk.ImportSemaphoreFdInfoKHR{
		SType: vk.StructureTypeImportSemaphoreFdInfoKhr, Semaphore: s, Flags: vk.SemaphoreImportFlags(vk.SemaphoreImportTemporaryBit),
		HandleType: vk.ExternalSemaphoreHandleTypeSyncFdBit, Fd: int32(fd),
	}
	if r := d.cmds.ImportSemaphoreFdKHR(d.device, &info); r != vk.Success {
		t.Fatalf("import: %d", r)
	}
	return s
}

// Closing with a pass's signal unconsumed and a sync_file kept leaks
// neither.
func TestDeviceCloseWithAFrameSignal(t *testing.T) {
	d := lavapipeSyncDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	clearPass(t, d, tg)
	syncFile(t, d)
	kept := d.lastSyncFile
	clearPass(t, d, tg)
	if d.frameSignal == 0 {
		t.Fatal("no frame signal to close with")
	}
	d.Close()
	if d.frameSignal != 0 || d.lastSyncFileSet {
		t.Errorf("after Close: signal %d, sync_file kept %v", d.frameSignal, d.lastSyncFileSet)
	}
	if _, err := unix.FcntlInt(uintptr(kept), unix.F_GETFD, 0); err != unix.EBADF {
		t.Errorf("the kept sync_file %d is still open: %v", kept, err)
	}
}
