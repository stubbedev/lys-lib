package capture_test

import (
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
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

// TestHeadlessDmabufTransformedOutput pins what a rotated or flipped
// output's capture holds, the convention gpu.EncoderConfig.Transform
// turns back: the output's pixels flipped (for the flipped
// transforms), then turned counter-clockwise by the transform. A band
// down the output's left edge lands on the buffer edge worked out by
// hand for each of sway's eight transforms, and the output reports the
// transform. It needs a compositor rendering on a GPU (just gpu-host).
func TestHeadlessDmabufTransformedOutput(t *testing.T) {
	inproc.Require(t)
	c := inproc.Capture(t)
	if !c.HasDmabuf() {
		t.Skip("compositor offers no linux-dmabuf (CPU renderer)")
	}
	socks, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "sway-ipc.*.sock"))
	swaymsg, err := exec.LookPath("swaymsg")
	if len(socks) == 0 || err != nil {
		t.Skip("needs sway's IPC socket and swaymsg to turn the output")
	}
	turn := func(transform string) {
		t.Helper()
		if out, err := exec.Command(swaymsg, "-s", socks[0], "output", "*", "transform", transform).CombinedOutput(); err != nil {
			t.Fatalf("swaymsg transform %s: %v: %s", transform, err, out)
		}
	}
	t.Cleanup(func() { turn("normal") })
	inproc.Show(t, func(a *app.Application, out *app.Output) error {
		_, err := a.NewLayer(app.LayerConfig{
			Output:     out,
			Layer:      app.LayerOverlay,
			Anchor:     app.AnchorTop | app.AnchorBottom | app.AnchorLeft,
			Namespace:  "lys-ui-capture-transform-test",
			Root:       widget.NewSpacer(48, 1),
			Background: fillRed,
		})
		return err
	})
	// sway names its transforms turning clockwise, so its 90 is
	// wl_output's 270 and the other way round.
	for _, tc := range []struct {
		name      string
		transform capture.Transform
		edge      string
	}{
		{"normal", 0, "left"},
		{"270", 1, "bottom"},
		{"180", 2, "right"},
		{"90", 3, "top"},
		{"flipped", 4, "right"},
		{"flipped-270", 5, "top"},
		{"flipped-180", 6, "left"},
		{"flipped-90", 7, "bottom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turn(tc.name)
			inproc.Eventually(t, "the band on the "+tc.edge+" edge", func() error {
				if err := c.Refresh(); err != nil {
					return err
				}
				out := c.Outputs()[0]
				if out.Transform != tc.transform {
					return fmt.Errorf("output reports transform %d, want %d", out.Transform, tc.transform)
				}
				format, err := c.DmabufFormat(out, false)
				if err != nil {
					return err
				}
				d, err := gpu.OpenNode(format.Device)
				if err != nil {
					return err
				}
				defer d.Close()
				buf, err := d.NewBuffer(format.Width, format.Height, format.Fourcc, format.Modifiers)
				if err != nil {
					return err
				}
				defer buf.Close()
				imported, err := c.ImportDmabuf(buf.Dmabuf())
				if err != nil {
					return err
				}
				defer imported.Destroy()
				if err := c.CopyOutputDmabuf(out, false, imported); err != nil {
					return err
				}
				img, err := buf.ReadPixels()
				if err != nil {
					return err
				}
				w, h := format.Width, format.Height
				edges := map[string]image.Point{"left": {10, h / 2}, "right": {w - 11, h / 2}, "top": {w / 2, 10}, "bottom": {w / 2, h - 11}}
				for name, p := range edges {
					err := inproc.WantColor(img, p.X, p.Y, fillRed)
					if (name == tc.edge) != (err == nil) {
						return fmt.Errorf("%dx%d buffer: %s edge at %v is %v, want the band only on the %s edge", w, h, name, p, img.RGBAAt(p.X, p.Y), tc.edge)
					}
				}
				return nil
			})
		})
	}
}
