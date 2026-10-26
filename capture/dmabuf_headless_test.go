package capture_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stubbedev/lys-ui/app"
	"github.com/stubbedev/lys-ui/capture"
	"github.com/stubbedev/lys-ui/dmabuf"
	"github.com/stubbedev/lys-ui/gpu"
	"github.com/stubbedev/lys-ui/internal/headlesstest/inproc"
	"github.com/stubbedev/lys-ui/internal/udmabuf"
	"github.com/stubbedev/lys-ui/widget"
)

func newUdmabuf(t *testing.T, size int) (int, []byte) {
	t.Helper()
	b, err := udmabuf.New(size)
	if err != nil {
		t.Skipf("no udmabuf: %v", err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	return b.FD, b.Mem
}

// TestHeadlessDmabufCapture runs the zero-copy path end to end: probe
// the output's dmabuf format, import a udmabuf as the target, copy a
// frame into it, and read the pixels back through the memfd. It needs
// a GPU-rendering compositor (sway's gles2 renderer); the pixman gate
// skips it and covers the negative probe instead.
func TestHeadlessDmabufCapture(t *testing.T) {
	inproc.Require(t)
	c := inproc.Capture(t)
	if !c.HasDmabuf() {
		t.Skip("compositor offers no linux-dmabuf (CPU renderer)")
	}
	inproc.Show(t, func(a *app.Application, out *app.Output) error {
		_, err := a.NewLayer(app.LayerConfig{
			Output:     out,
			Layer:      app.LayerOverlay,
			Anchor:     app.AnchorTop | app.AnchorBottom | app.AnchorLeft | app.AnchorRight,
			Namespace:  "lys-ui-capture-test",
			Root:       widget.NewBox(widget.Row, 0, 0),
			Background: fillRed,
		})
		return err
	})
	out := c.Outputs()[0]
	format, err := c.DmabufFormat(out, false)
	if errors.Is(err, capture.ErrUnsupported) {
		t.Skip("compositor offers no dmabuf screencopy target")
	}
	if err != nil {
		t.Fatal(err)
	}
	if format.Width != int(out.Width) || format.Height != int(out.Height) {
		t.Fatalf("dmabuf format %+v for a %dx%d output", format, out.Width, out.Height)
	}
	t.Logf("dmabuf format %#08x %dx%d on device %#x, modifiers %#x", format.Fourcc, format.Width, format.Height, format.Device, format.Modifiers)
	if len(format.Modifiers) == 0 || format.Device == 0 {
		t.Fatalf("dmabuf format %+v names no modifiers or device; the compositor's feedback should (#339)", format)
	}
	cached, err := c.DmabufFormat(out, false)
	if err != nil || !reflect.DeepEqual(cached, format) {
		t.Fatalf("second probe = %+v, %v; want the cached %+v", cached, err, format)
	}
	stride := format.Width * 4
	size := (stride*format.Height + 4095) &^ 4095
	fd, mem := newUdmabuf(t, size)

	t.Run("a bad import reports the compositor's verdict", func(t *testing.T) {
		// A plane that claims more rows than the buffer holds is a
		// protocol error by the linux-dmabuf spec - fatal to the
		// connection - so it runs on a throwaway client. The verdict
		// must surface from the import and from every later call,
		// never as a hang.
		bad := inproc.Capture(t)
		_, err := bad.ImportDmabuf(dmabuf.Buffer{
			Width: format.Width, Height: format.Height * 4, Format: dmabuf.FormatXRGB8888,
			Planes: []dmabuf.Plane{{FD: fd, Stride: uint32(stride)}},
		})
		if err == nil {
			t.Fatal("oversized import accepted")
		}
		if err := bad.Refresh(); err == nil {
			t.Fatal("a dead connection refreshed cleanly")
		}
	})

	buf, err := c.ImportDmabuf(dmabuf.Buffer{
		Width: format.Width, Height: format.Height, Format: format.Fourcc,
		Planes: []dmabuf.Plane{{FD: fd, Stride: uint32(stride)}},
	})
	if errors.Is(err, capture.ErrDmabufRejected) {
		t.Skip("compositor cannot import udmabuf memory")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer buf.Destroy()
	// Some GPUs (NVIDIA among them) import a linear dmabuf for
	// sampling only and cannot render the copy into it; the
	// compositor then fails the copy, which is its verdict, not ours.
	if _, err := c.CaptureOutputDmabuf(out, false, buf); errors.Is(err, capture.ErrFailed) {
		t.Skip("compositor cannot render into a linear udmabuf on this GPU")
	}
	inproc.Eventually(t, "red frame in the dmabuf", func() error {
		if _, err := c.CaptureOutputDmabuf(out, false, buf); err != nil {
			return err
		}
		f := &capture.Frame{Width: format.Width, Height: format.Height, Stride: stride, Format: capture.FormatFromFourcc(format.Fourcc), Data: mem}
		img, err := f.Image()
		if err != nil {
			return err
		}
		return inproc.WantColor(img, format.Width/2, format.Height/2, fillRed)
	})
}

// TestHeadlessDmabufCopyTakesAnIdleFrame pins the plain copy: on an
// output that has not changed since the last capture, the damage copy
// waits for a change, while CopyOutputDmabuf takes the next frame and
// returns.
func TestHeadlessDmabufCopyTakesAnIdleFrame(t *testing.T) {
	inproc.Require(t)
	c := inproc.Capture(t)
	if !c.HasDmabuf() {
		t.Skip("compositor offers no linux-dmabuf (CPU renderer)")
	}
	out := c.Outputs()[0]
	format, err := c.DmabufFormat(out, false)
	if err != nil {
		t.Skipf("no dmabuf screencopy target: %v", err)
	}
	stride := format.Width * 4
	fd, _ := newUdmabuf(t, (stride*format.Height+4095)&^4095)
	buf, err := c.ImportDmabuf(dmabuf.Buffer{
		Width: format.Width, Height: format.Height, Format: format.Fourcc,
		Planes: []dmabuf.Plane{{FD: fd, Stride: uint32(stride)}},
	})
	if err != nil {
		t.Skipf("import: %v", err)
	}
	defer buf.Destroy()
	for i := range 3 {
		done := make(chan error, 1)
		go func() { done <- c.CopyOutputDmabuf(out, false, buf) }()
		select {
		case err := <-done:
			if errors.Is(err, capture.ErrFailed) {
				t.Skip("compositor cannot render into a linear udmabuf on this GPU")
			}
			if err != nil {
				t.Fatalf("copy %d: %v", i, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("copy %d of an idle output never returned", i)
		}
	}
}

// TestHeadlessDmabufCaptureIntoGPUBuffer is the recorder's path: a
// buffer the GPU allocates on the compositor's main device in a
// modifier both sides take, imported and copied into, then read back.
// It needs a compositor rendering on a GPU (just gpu-host); the pixman
// gate skips it.
func TestHeadlessDmabufCaptureIntoGPUBuffer(t *testing.T) {
	inproc.Require(t)
	c := inproc.Capture(t)
	if !c.HasDmabuf() {
		t.Skip("compositor offers no linux-dmabuf (CPU renderer)")
	}
	inproc.Show(t, func(a *app.Application, out *app.Output) error {
		_, err := a.NewLayer(app.LayerConfig{
			Output:     out,
			Layer:      app.LayerOverlay,
			Anchor:     app.AnchorTop | app.AnchorBottom | app.AnchorLeft | app.AnchorRight,
			Namespace:  "lys-ui-capture-gpu-test",
			Root:       widget.NewBox(widget.Row, 0, 0),
			Background: fillRed,
		})
		return err
	})
	out := c.Outputs()[0]
	format, err := c.DmabufFormat(out, false)
	if errors.Is(err, capture.ErrUnsupported) {
		t.Skip("compositor offers no dmabuf screencopy target")
	}
	if err != nil {
		t.Fatal(err)
	}
	d, err := gpu.OpenNode(format.Device)
	if errors.Is(err, gpu.ErrNoVulkan) {
		t.Skipf("no Vulkan: %v", err)
	}
	if err != nil {
		t.Fatalf("open the compositor's device %#x: %v", format.Device, err)
	}
	defer d.Close()
	buf, err := d.NewBuffer(format.Width, format.Height, format.Fourcc, format.Modifiers)
	if err != nil {
		t.Fatalf("a %s buffer in one of the compositor's modifiers %#x: %v", d.Name(), format.Modifiers, err)
	}
	defer buf.Close()
	imported, err := c.ImportDmabuf(buf.Dmabuf())
	if err != nil {
		t.Fatalf("import the %s buffer in modifier %#x: %v", d.Name(), buf.Dmabuf().Modifier, err)
	}
	defer imported.Destroy()
	t.Logf("%s buffer in modifier %#x", d.Name(), buf.Dmabuf().Modifier)
	inproc.Eventually(t, "red frame in the GPU buffer", func() error {
		if err := c.CopyOutputDmabuf(out, false, imported); err != nil {
			return err
		}
		img, err := buf.ReadPixels()
		if err != nil {
			return err
		}
		return inproc.WantColor(img, format.Width/2, format.Height/2, fillRed)
	})
}
