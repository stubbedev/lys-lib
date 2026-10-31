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
