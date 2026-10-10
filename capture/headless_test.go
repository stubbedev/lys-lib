package capture_test

import (
	"errors"
	"image"
	"testing"
	"time"

	"github.com/stubbedev/gelm/app"
	"github.com/stubbedev/gelm/capture"
	"github.com/stubbedev/gelm/internal/headlesstest/inproc"
	"github.com/stubbedev/gelm/render"
	"github.com/stubbedev/gelm/widget"
)

// The compositor-in-the-loop capture tests: they attach to the private
// headless sway the just headless recipe boots (GELM_HEADLESS set),
// paint known colors with gelm surfaces, and capture them back through
// every protocol path sway offers. Without GELM_HEADLESS they skip -
// they must never capture a developer's real session.

// Test colors, opaque, distinct in every channel.
var (
	fillRed   = render.Color(0xffd02030)
	fillGreen = render.Color(0xff20c040)
)

func TestHeadlessOutputCapture(t *testing.T) {
	inproc.Require(t)
	inproc.Show(t, func(a *app.Application, out *app.Output) error {
		_, err := a.NewLayer(app.LayerConfig{
			Output:     out,
			Layer:      app.LayerOverlay,
			Anchor:     app.AnchorTop | app.AnchorBottom | app.AnchorLeft | app.AnchorRight,
			Namespace:  "gelm-capture-test",
			Root:       widget.NewBox(widget.Row, 0, 0),
			Background: fillRed,
		})
		return err
	})
	c := inproc.Capture(t)
	out := c.Outputs()[0]
	if out.Name == "" || out.Width <= 0 || out.Height <= 0 {
		t.Fatalf("output metadata incomplete: %+v", out)
	}

	t.Run("screencopy whole output", func(t *testing.T) {
		if !c.HasScreencopy() {
			t.Fatal("sway offers wlr-screencopy")
		}
		inproc.Eventually(t, "red output", func() error {
			f, err := c.CaptureOutput(out, capture.Options{})
			if err != nil {
				return err
			}
			if f.Width != int(out.Width) || f.Height != int(out.Height) {
				t.Fatalf("frame %dx%d, output mode %dx%d", f.Width, f.Height, out.Width, out.Height)
			}
			img, err := f.Image()
			if err != nil {
				return err
			}
			return inproc.WantColor(img, f.Width/2, f.Height/2, fillRed)
		})
	})
	t.Run("screencopy region", func(t *testing.T) {
		f, err := c.CaptureOutputRegion(out, image.Rect(10, 20, 110, 70), capture.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if f.Width != 100 || f.Height != 50 {
			t.Fatalf("region frame %dx%d, want 100x50", f.Width, f.Height)
		}
		img, err := f.Image()
		if err != nil {
			t.Fatal(err)
		}
		if err := inproc.WantColor(img, 50, 25, fillRed); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("empty region is refused before the wire", func(t *testing.T) {
		if _, err := c.CaptureOutputRegion(out, image.Rectangle{}, capture.Options{}); err == nil {
			t.Fatal("empty region captured")
		}
	})
	t.Run("ext output capture", func(t *testing.T) {
		if !c.HasOutputCapture() {
			t.Fatal("sway offers ext-image-copy-capture output sources")
		}
		f, err := c.CaptureOutputOnce(out, capture.Options{})
		if err != nil {
			t.Fatal(err)
		}
		img, err := f.Image()
		if err != nil {
			t.Fatal(err)
		}
		if err := inproc.WantColor(img, f.Width/2, f.Height/2, fillRed); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("stream publishes frames and closes", func(t *testing.T) {
		s, err := capture.OpenOutputStream(out.Name, false)
		if err != nil {
			t.Fatal(err)
		}
		info := s.Info()
		if info.Width != int(out.Width) || info.Stride != info.Width*info.Format.BytesPerPixel() {
			t.Fatalf("stream info %+v", info)
		}
		inproc.Eventually(t, "first stream frame", func() error {
			if s.Latest() == nil {
				return errors.New("no frame yet")
			}
			return nil
		})
		f, err := s.Latest().Frame()
		if err != nil {
			t.Fatal(err)
		}
		img, err := f.Image()
		if err != nil {
			t.Fatal(err)
		}
		if err := inproc.WantColor(img, 5, 5, fillRed); err != nil {
			t.Fatal(err)
		}
		frame := s.Latest()
		closed := make(chan error, 1)
		go func() { closed <- s.Close() }()
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Close did not wake the parked capture")
		}
		if s.Err() != nil {
			t.Errorf("Err after Close = %v, want nil", s.Err())
		}
		if n := frame.ReadInto(make([]byte, 16)); n != 0 {
			t.Errorf("ReadInto after Close copied %d bytes, want 0", n)
		}
	})
	t.Run("an unknown output is an error", func(t *testing.T) {
		if _, err := capture.OpenOutputStream("NOPE-9", false); err == nil {
			t.Fatal("stream opened on a missing output")
		}
	})
	t.Run("dmabuf probe", func(t *testing.T) {
		// The pixman headless sway renders on the CPU and offers no
		// linux-dmabuf: the probe must say unsupported, not hang or
		// crash.
		if c.HasDmabuf() {
			t.Skip("compositor offers linux-dmabuf; the negative probe needs a CPU renderer")
		}
		if _, err := c.DmabufFormat(out, false); !errors.Is(err, capture.ErrUnsupported) {
			t.Fatalf("DmabufFormat = %v, want capture.ErrUnsupported", err)
		}
	})
	t.Run("hyprland export is unsupported on sway", func(t *testing.T) {
		if _, err := c.CaptureHyprlandWindow(1, capture.Options{}); !errors.Is(err, capture.ErrUnsupported) {
			t.Fatalf("CaptureHyprlandWindow = %v, want capture.ErrUnsupported", err)
		}
	})
}

func TestHeadlessToplevelCapture(t *testing.T) {
	inproc.Require(t)
	const appID = "dev.stubbe.gelm.capturetest"
	inproc.Show(t, func(a *app.Application, out *app.Output) error {
		_, err := a.NewWindow(app.WindowConfig{
			Title:      "capture target",
			AppID:      appID,
			Width:      320,
			Height:     200,
			Root:       widget.NewBox(widget.Row, 0, 0),
			Background: fillGreen,
		})
		return err
	})
	c := inproc.Capture(t)
	if !c.HasToplevelCapture() {
		t.Fatal("sway offers ext toplevel capture")
	}
	var target capture.Toplevel
	inproc.Eventually(t, "toplevel listed", func() error {
		tls, err := c.Toplevels()
		if err != nil {
			return err
		}
		for _, tl := range tls {
			if tl.AppID == appID {
				target = tl
				return nil
			}
		}
		return errors.New("not listed yet")
	})
	if target.Title != "capture target" || target.Identifier == "" {
		t.Fatalf("toplevel metadata %+v", target)
	}
	inproc.Eventually(t, "green window", func() error {
		f, err := c.CaptureToplevel(target, capture.Options{})
		if err != nil {
			return err
		}
		img, err := f.Image()
		if err != nil {
			return err
		}
		return inproc.WantColor(img, f.Width/2, f.Height/2, fillGreen)
	})
	t.Run("a foreign toplevel value is refused", func(t *testing.T) {
		if _, err := c.CaptureToplevel(capture.Toplevel{Title: "x"}, capture.Options{}); err == nil {
			t.Fatal("captured a toplevel without a handle")
		}
	})
}
