package capture_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/capture"
	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/gpu"
	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/wltest"
)

// showWindow puts a painted toplevel on screen and finds it in c's
// toplevel list.
func showWindow(t *testing.T, c *capture.Client, appID string, w, h int32) (*wltest.Window, capture.Toplevel) {
	t.Helper()
	p := wltest.NewPainter(t)
	win := p.Window(appID, "session target", w, h, fillGreen)
	p.Run()
	return win, findToplevel(t, c, appID)
}

// findToplevel waits for the window of appID in c's toplevel list.
func findToplevel(t *testing.T, c *capture.Client, appID string) capture.Toplevel {
	t.Helper()
	var target capture.Toplevel
	wltest.Eventually(t, "toplevel listed", func() error {
		tls, err := c.Toplevels()
		if err != nil {
			return err
		}
		i := slices.IndexFunc(tls, func(tl capture.Toplevel) bool { return tl.AppID == appID })
		if i < 0 {
			return errors.New("not listed yet")
		}
		target = tls[i]
		return nil
	})
	return target
}

// gpuTarget is a GPU buffer allocated in one of a session's dmabuf
// layouts and imported on its client.
type gpuTarget struct {
	buf      *gpu.Buffer
	imported *capture.DmabufBuffer
}

// allocate allocates and imports a buffer in the session's first
// format, on the device the session names; both are released at
// cleanup.
func allocate(t *testing.T, c *capture.Client, formats []capture.DmabufFormat) gpuTarget {
	t.Helper()
	f := formats[0]
	d, err := gpu.OpenNode(f.Device)
	if errors.Is(err, gpu.ErrNoVulkan) {
		t.Skipf("no Vulkan: %v", err)
	}
	if err != nil {
		t.Fatalf("open the session's device %#x: %v", f.Device, err)
	}
	t.Cleanup(d.Close)
	buf, err := d.NewBuffer(f.Width, f.Height, f.Fourcc, f.Modifiers)
	if err != nil {
		t.Fatalf("a %dx%d %s buffer in one of the session's modifiers %v: %v", f.Width, f.Height, f.Fourcc, f.Modifiers, err)
	}
	t.Cleanup(buf.Close)
	imported, err := c.ImportDmabuf(buf.Dmabuf())
	if err != nil {
		t.Fatalf("import the %s buffer: %v", buf.Dmabuf().Modifier, err)
	}
	t.Cleanup(imported.Destroy)
	return gpuTarget{buf: buf, imported: imported}
}

// openDmabufSession opens a session on src, skipping when the
// compositor offers it no dmabuf layout (a CPU renderer).
func openDmabufSession(t *testing.T, c *capture.Client, src capture.Source) (*capture.DmabufSession, []capture.DmabufFormat) {
	t.Helper()
	if !c.HasDmabuf() {
		t.Skip("compositor offers no linux-dmabuf (CPU renderer)")
	}
	s, err := c.OpenDmabufSession(src, false)
	if errors.Is(err, capture.ErrUnsupported) {
		t.Skipf("compositor offers the session no dmabuf layout: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	formats, err := s.Formats()
	if err != nil {
		t.Fatal(err)
	}
	return s, formats
}

// wantGreen captures into target until its center reads green.
func wantGreen(t *testing.T, s *capture.DmabufSession, target gpuTarget, win *wltest.Window) {
	t.Helper()
	d := target.imported.Attrs
	wltest.Eventually(t, "green window in the dmabuf", func() error {
		if err := captureRepainting(t, s, target.imported, win); err != nil {
			return err
		}
		img, err := target.buf.ReadPixels()
		if err != nil {
			return err
		}
		return wltest.WantColor(img, d.Width/2, d.Height/2, fillGreen)
	})
}

// captureRepainting runs one Capture while the window repaints at the
// buffer's size every 100ms: sway copies a toplevel's frame, a
// session's first one included, only once the window changes.
func captureRepainting(t *testing.T, s *capture.DmabufSession, buf *capture.DmabufBuffer, win *wltest.Window) error {
	t.Helper()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	return awaitCapture(t, s, buf, func(done <-chan struct{}) {
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				win.Resize(intconv.Clamp[int32](buf.Attrs.Width), intconv.Clamp[int32](buf.Attrs.Height))
			}
		}
	})
}

// awaitCapture runs one Capture off the test goroutine, and during
// alongside it until it returns; it fails the test when the capture
// has not returned within five seconds.
func awaitCapture(t *testing.T, s *capture.DmabufSession, buf *capture.DmabufBuffer, during func(done <-chan struct{})) error {
	t.Helper()
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err = s.Capture(buf)
	}()
	if during != nil {
		during(done)
	}
	select {
	case <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Capture never returned")
		return nil
	}
}

// TestHeadlessToplevelDmabufSession runs a window share's zero-copy
// path: the toplevel session's dmabuf constraints, a GPU buffer
// allocated against them, frames copied into it, a resize reported as
// ErrReconfigured with the new constraints, and the session's end on
// Close and on the window closing. It needs a compositor rendering on
// a GPU (just gpu-host); the pixman gate skips it.
func TestHeadlessToplevelDmabufSession(t *testing.T) {
	wltest.Require(t)
	const appID = "dev.stubbe.lys.ui.dmabufsession"
	c := captureClient(t)
	win, target := showWindow(t, c, appID, 320, 200)
	s, formats := openDmabufSession(t, c, target)
	for _, f := range formats {
		if f.Width != 320 || f.Height != 200 || f.Device == 0 || len(f.Modifiers) == 0 {
			t.Fatalf("session format %+v for a 320x200 window", f)
		}
	}
	t.Logf("session formats %+v", formats)
	first := allocate(t, c, formats)
	wantGreen(t, s, first, win)

	t.Run("Close wakes a capture of a static window", func(t *testing.T) {
		other := captureClient(t)
		parked, _ := openDmabufSession(t, other, findToplevel(t, other, appID))
		buf := allocate(t, other, formats)
		if err := captureRepainting(t, parked, buf.imported, win); err != nil {
			t.Fatalf("first frame: %v", err)
		}
		err := awaitCapture(t, parked, buf.imported, func(<-chan struct{}) {
			time.Sleep(200 * time.Millisecond)
			if err := parked.Close(); err != nil {
				t.Error(err)
			}
		})
		if !errors.Is(err, capture.ErrClosed) {
			t.Fatalf("Capture during Close = %v, want ErrClosed", err)
		}
		if _, err := parked.Capture(buf.imported); !errors.Is(err, capture.ErrClosed) {
			t.Fatalf("Capture after Close = %v, want ErrClosed", err)
		}
		if _, err := parked.Formats(); !errors.Is(err, capture.ErrClosed) {
			t.Fatalf("Formats after Close = %v, want ErrClosed", err)
		}
		if err := parked.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
	})
	t.Run("a buffer that never fitted is not a resize", func(t *testing.T) {
		wrong := formats[0]
		wrong.Width, wrong.Height = 64, 64
		buf := allocate(t, c, []capture.DmabufFormat{wrong})
		_, err := s.Capture(buf.imported)
		if err == nil || errors.Is(err, capture.ErrReconfigured) {
			t.Fatalf("Capture into a 64x64 buffer = %v, want a plain misfit error", err)
		}
	})
	t.Run("a nil or foreign buffer is refused", func(t *testing.T) {
		if _, err := s.Capture(nil); err == nil {
			t.Fatal("captured into a nil buffer")
		}
		if _, err := s.Capture(&capture.DmabufBuffer{Attrs: dmabuf.Buffer{Width: 320, Height: 200}}); err == nil {
			t.Fatal("captured into a buffer of no client")
		}
	})
	t.Run("a resize is reported and the new constraints taken", func(t *testing.T) {
		win.Resize(400, 300)
		wltest.Eventually(t, "the resize reported", func() error {
			err := awaitCapture(t, s, first.imported, nil)
			if errors.Is(err, capture.ErrReconfigured) {
				return nil
			}
			return errors.Join(errors.New("the old buffer still fits"), err)
		})
		resized, err := s.Formats()
		if err != nil {
			t.Fatal(err)
		}
		if resized[0].Width != 400 || resized[0].Height != 300 {
			t.Fatalf("formats after the resize %+v, want 400x300", resized)
		}
		wantGreen(t, s, allocate(t, c, resized), win)
		if err := awaitCapture(t, s, first.imported, nil); !errors.Is(err, capture.ErrReconfigured) {
			t.Fatalf("the pre-resize buffer again = %v, want ErrReconfigured", err)
		}
	})
	t.Run("the window closing stops the session", func(t *testing.T) {
		resized, err := s.Formats()
		if err != nil {
			t.Fatal(err)
		}
		buf := allocate(t, c, resized)
		err = awaitCapture(t, s, buf.imported, func(<-chan struct{}) { win.Close() })
		for errors.Is(err, capture.ErrReconfigured) || err == nil {
			err = awaitCapture(t, s, buf.imported, nil)
		}
		if !errors.Is(err, capture.ErrSessionStopped) {
			t.Fatalf("Capture of a closed window = %v, want ErrSessionStopped", err)
		}
		if _, err := s.Formats(); !errors.Is(err, capture.ErrSessionStopped) {
			t.Fatalf("Formats of a stopped session = %v, want ErrSessionStopped", err)
		}
		if _, err := c.OpenDmabufSession(target, false); err == nil {
			t.Fatal("opened a session on a closed window")
		}
	})
}

// TestHeadlessToplevelSessionOverlap pins that a window takes one
// capture session at a time on a Client (#45): while a DmabufSession
// is open on a static window, CaptureToplevel and a second session on
// it fail at once with ErrToplevelBusy instead of waiting for a change
// of the window that never comes, and the window captures again once
// the session closed.
func TestHeadlessToplevelSessionOverlap(t *testing.T) {
	wltest.Require(t)
	c := captureClient(t)
	win, target := showWindow(t, c, "dev.stubbe.lys.ui.sessionoverlap", 320, 200)
	s, _ := openDmabufSession(t, c, target)
	var during [2]error
	for i := range during {
		during[i] = captureToplevelPromptly(t, c, target, win)
	}
	for i, err := range during {
		if !errors.Is(err, capture.ErrToplevelBusy) {
			t.Fatalf("CaptureToplevel %d during the session = %v, want ErrToplevelBusy", i+1, err)
		}
	}
	if _, err := c.OpenDmabufSession(target, false); !errors.Is(err, capture.ErrToplevelBusy) {
		t.Fatalf("a second session on the window = %v, want ErrToplevelBusy", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := captureToplevelPromptly(t, c, target, win); err != nil {
			t.Fatalf("CaptureToplevel %d after the session closed: %v", i+1, err)
		}
	}
}

// captureToplevelPromptly runs one CaptureToplevel off the test
// goroutine and fails the test when it has not returned within five
// seconds, repainting the window then so the parked capture returns
// and the client is free for cleanup.
func captureToplevelPromptly(t *testing.T, c *capture.Client, target capture.Toplevel, win *wltest.Window) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := c.CaptureToplevel(target, capture.Options{})
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		win.Repaint()
		<-done
		t.Fatal("CaptureToplevel blocked")
		return nil
	}
}

// TestHeadlessOutputDmabufSession pins that a session opens on any
// Source: an output's frames land in a GPU buffer as a window's do.
func TestHeadlessOutputDmabufSession(t *testing.T) {
	wltest.Require(t)
	c := captureClient(t)
	p := wltest.NewPainter(t)
	p.Layer(wltest.AnchorTop|wltest.AnchorBottom|wltest.AnchorLeft|wltest.AnchorRight, 0, 0, fillRed)
	p.Run()
	out := c.Outputs()[0]
	s, formats := openDmabufSession(t, c, out)
	if formats[0].Width != int(out.Width) || formats[0].Height != int(out.Height) {
		t.Fatalf("session formats %+v for a %dx%d output", formats, out.Width, out.Height)
	}
	target := allocate(t, c, formats)
	wltest.Eventually(t, "red output in the dmabuf", func() error {
		if _, err := s.Capture(target.imported); err != nil {
			return err
		}
		img, err := target.buf.ReadPixels()
		if err != nil {
			return err
		}
		return wltest.WantColor(img, formats[0].Width/2, formats[0].Height/2, fillRed)
	})
}

// TestHeadlessDmabufSessionUnsupported pins the CPU renderer's answer:
// the pixman headless sway offers no linux-dmabuf, so a session is
// ErrUnsupported, not a hang or a session without layouts.
func TestHeadlessDmabufSessionUnsupported(t *testing.T) {
	wltest.Require(t)
	c := captureClient(t)
	if c.HasDmabuf() {
		t.Skip("compositor offers linux-dmabuf; the negative open needs a CPU renderer")
	}
	_, target := showWindow(t, c, "dev.stubbe.lys.ui.dmabufsession.cpu", 320, 200)
	if _, err := c.OpenDmabufSession(target, false); !errors.Is(err, capture.ErrUnsupported) {
		t.Fatalf("OpenDmabufSession = %v, want capture.ErrUnsupported", err)
	}
}
