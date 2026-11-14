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
	"github.com/stubbedev/lys-lib/render"
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

func heavyPass(t *testing.T, d *Device, tg *Target, units int, last color.RGBA) {
	t.Helper()
	all := image.Rectangle{Max: tg.Size()}
	runPass(t, d, tg, []image.Rectangle{all}, func(p *Pass) {
		p.Clear(color.RGBA{A: 255})
		for range units {
			p.FillRect(all, color.RGBA{A: 128}, Mask{})
		}
		p.FillRect(image.Rect(0, 0, 4, 4), last, Mask{})
	})
}

// busyDraws is how many draws over a whole target of busySize keep a GPU
// busy for a while: long enough that the CPU usually hands out the work's
// fence before it finishes, which is what lets a test see a fence that
// does not cover it. Nothing asserts how long it takes.
const (
	busyDraws = 64
	busySize  = 1024
)

func heavyPassJob(t *testing.T, d *Device, tg *Target) slowJob {
	t.Helper()
	return newSlowJob(t, d, func(units int) { heavyPass(t, d, tg, units, color.RGBA{A: 255}) })
}

// handout is a sync_file d handed out for the work it submitted after
// since, and whether all d had submitted had finished right after: a
// fence that had signalled is handed out as a stub (the kernel exports
// one once a dmabuf's fences have, a driver may for a finished
// submission), which stands for that work only if it had all finished.
type handout struct {
	fd      int
	since   time.Duration
	allDone bool
}

func handedOut(d *Device, fd int, since time.Duration) handout {
	return handout{fd: fd, since: since, allDone: d.finished() == len(d.inflight)}
}

// signalled waits for the fence and fails the test unless it covers the
// work: it signals after since, the fence of that work, or it is a stub
// handed out with that work finished. It reports when it signalled and
// whether that is the work's own time rather than a stub's.
func (h handout) signalled(t *testing.T) (time.Duration, bool) {
	t.Helper()
	at := signalTime(t, h.fd)
	if at > h.since {
		return at, true
	}
	if !h.allDone {
		t.Errorf("sync_file %d had signalled at %v, before the work since %v was submitted, and was handed out with that work unfinished: it does not cover it", h.fd, at, h.since)
	}
	return at, false
}

func dupFD(t *testing.T, fd int) int {
	t.Helper()
	dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("duplicating fd %d: %v", fd, err)
	}
	return dup
}

func waitReadable(t *testing.T, fd int) {
	t.Helper()
	if fd < 0 {
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(pfd, int(time.Until(deadline).Milliseconds()))
		if errors.Is(err, unix.EINTR) && time.Now().Before(deadline) {
			continue
		}
		if err != nil || n != 1 {
			t.Fatalf("poll on the sync_file: %d, %v", n, err)
		}
		return
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
	job := heavyPassJob(t, d, tg)
	before := monotonic()
	heavyPass(t, d, tg, job.units, color.RGBA{0, 255, 0, 255})
	fd, err := d.SyncFile()
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	signalled := signalTime(t, fd)
	job.ran(t, before, signalled)
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
	src, err := a.NewTarget(busySize, busySize, fourcc.ARGB8888, mods)
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
		since := monotonic()
		heavyPass(t, a, src, busyDraws, want)
		fd := syncFile(t, a)
		wait := fd
		if fd >= 0 {
			wait = dupFD(t, fd)
		}
		if err := b.WaitSyncFile(wait); err != nil {
			t.Fatal(err)
		}
		runPass(t, b, dst, []image.Rectangle{{Max: dst.Size()}}, func(p *Pass) {
			p.DrawTexture(tx, FRect{X: 1, Y: 1, W: 1, H: 1}, image.Rect(0, 0, 1, 1), TransformNormal, 1, Mask{})
		})
		if got := readTarget(t, dst).RGBAAt(0, 0); got != want {
			t.Errorf("the waiting device sampled %v, want %v", got, want)
		}
		if fd < 0 {
			continue
		}
		signalled, own := handedOut(a, fd, since).signalled(t)
		if done, ok, err := b.FrameDone(); own && err == nil && ok && done < signalled {
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
	for range 64 {
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
	if sems > 3 || fences > 2 {
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

func flushedPass(t *testing.T, d *Device, tg *Target) {
	t.Helper()
	clearPass(t, d, tg)
	if err := d.Flush(); err != nil {
		t.Fatal(err)
	}
}

func syncFile(t *testing.T, d *Device) int {
	t.Helper()
	fd, err := d.SyncFile()
	if err != nil {
		t.Fatal(err)
	}
	if fd < 0 {
		if len(d.inflight) != 0 {
			t.Fatalf("SyncFile says all finished with %d submissions in flight", len(d.inflight))
		}
		return fd
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

// Once SyncFile is in use, a frame's fences come from the submission
// that carries its pass: none of their own, however many surfaces ask,
// and each caller's fd stands alone.
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
	if len(d.inflight) != 0 || !d.batchPass {
		t.Fatalf("%d submissions after a pass, pass batched %v", len(d.inflight), d.batchPass)
	}
	first := syncFile(t, d)
	second := syncFile(t, d)
	third, err := d.SyncFile()
	if err != nil {
		t.Fatal(err)
	}
	if third >= 0 {
		if err := unix.Close(third); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(d.inflight); n > 1 {
		t.Errorf("%d submissions for a pass and three sync_files, want the pass's only", n)
	}
	if first >= 0 && (first == second || first == third || second == third) {
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

// A flushed pass nobody asks about stops the next ones signalling,
// consuming what it signalled, and asking again starts over.
func TestPassesWithNoSyncFileStopSignalling(t *testing.T) {
	d := lavapipeSyncDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	clearPass(t, d, tg)
	syncFile(t, d)
	flushedPass(t, d, tg)
	if d.frameSignal == 0 {
		t.Fatal("a pass after SyncFile signals nothing")
	}
	flushedPass(t, d, tg)
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
	if d.batch == 0 && d.lastSyncFileSet {
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
	clearPass(t, d, tg)
	wait := syncFile(t, d)
	if wait < 0 {
		t.Logf("%s exports no sync_files: waits not checked", d.Name())
		return
	}
	dup := dupFD(t, wait)
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
	flushedPass(t, d, tg)
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

// -1 from SyncFile is a promise that nothing is left to wait for, on a
// driver that exports it or one that never does.
func TestASyncFileOfMinusOneMeansAllFinished(t *testing.T) {
	d := lavapipeSyncDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	job := heavyPassJob(t, d, tg)
	for range 8 {
		start := monotonic()
		heavyPass(t, d, tg, job.units, color.RGBA{0, 255, 0, 255})
		fd := syncFile(t, d)
		job.endsBy(t, start, fd)
		if fd < 0 {
			if got := readTarget(t, tg).RGBAAt(1, 1); got != (color.RGBA{0, 255, 0, 255}) {
				t.Fatalf("all finished, but the pass drew %v", got)
			}
		}
	}
}

func TestRenderExplicitNeverWaitsWhereImplicitFencesDoNotExist(t *testing.T) {
	d := openDevice(t)
	if !d.SyncFiles() {
		t.Skipf("%s has no sync_file semaphores", d.Name())
	}
	d.implicitFences = false
	tg, err := d.NewOffscreen(16, 16)
	if err != nil {
		t.Fatal(err)
	}
	rec := render.NewRecorder(16, 16, 1, 1)
	defer rec.Recording().End()
	rec.Clear(rec.Rect(), render.RGB(0, 255, 0))
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	waits := d.BlockingWaits()
	fd, err := d.RenderExplicit(rec.Recording(), tg)
	if err != nil {
		t.Fatal(err)
	}
	if fd < 0 {
		t.Logf("%s exported no fd for a pending signal: it had to wait", d.Name())
	} else {
		defer unix.Close(fd)
		n := d.BlockingWaits() - waits
		t.Logf("%s exported sync_file fd %d with %d blocking waits", d.Name(), fd, n)
		if n != 0 {
			t.Errorf("%d blocking waits rendering with an explicit consumer", n)
		}
		if _, err := drm.SyncFileSignalled(fd); err != nil {
			t.Errorf("the returned fd is no sync_file: %v", err)
		}
	}
	if got := readTarget(t, tg).RGBAAt(1, 1); got != (color.RGBA{0, 255, 0, 255}) {
		t.Errorf("the frame drew %v", got)
	}
}

func TestAFrameOfPassesIsOneSubmission(t *testing.T) {
	d := openDevice(t)
	chain := make([]*Target, 6)
	for i := range chain {
		tg, err := d.NewOffscreen(8>>(i/3), 8>>(i/3))
		if err != nil {
			t.Fatal(err)
		}
		defer tg.Close()
		chain[i] = tg
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	want := color.RGBA{0, 0, 0xff, 0xff}
	before := d.Submissions()
	runPass(t, d, chain[0], []image.Rectangle{{Max: chain[0].Size()}}, func(p *Pass) { p.Clear(want) })
	for i, tg := range chain[1:] {
		tx, err := chain[i].Texture()
		if err != nil {
			t.Fatal(err)
		}
		runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
			p.Kawase(tx, image.Rectangle{Max: tg.Size()}, 1, tg.Size().X > chain[i].Size().X)
		})
	}
	if n := d.Submissions() - before; n != 0 {
		t.Fatalf("%d submissions for six ended passes, want them held for the frame", n)
	}
	if d.SyncFiles() {
		waitReadable(t, syncFile(t, d))
	} else if err := d.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := d.Submissions() - before; n != 1 {
		t.Errorf("%d submissions for a frame of six passes, want one", n)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if d.calibrated {
		if _, ok, err := d.FrameDone(); err != nil || !ok {
			t.Errorf("FrameDone after the frame finished: %v, %v", ok, err)
		}
	}
	if got := readTarget(t, chain[len(chain)-1]).RGBAAt(1, 1); !within(got, want, 1) {
		t.Errorf("the last pass of the chain drew %v, want %v", got, want)
	}
}

func TestFrameDoneSubmitsThePassItAsksAbout(t *testing.T) {
	d := openDevice(t)
	if !d.calibrated {
		t.Skip("no calibrated timestamps")
	}
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	clearPass(t, d, tg)
	if _, _, err := d.FrameDone(); err != nil {
		t.Fatal(err)
	}
	if d.batchPass || d.batch != 0 {
		t.Fatal("FrameDone left the pass it reports on unsubmitted")
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := d.FrameDone(); err != nil || !ok {
		t.Errorf("FrameDone after Finish: %v, %v", ok, err)
	}
}
