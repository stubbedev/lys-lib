package gpu

import (
	"errors"
	"os"
	"testing"

	"github.com/stubbedev/lys-ui/dmabuf"
	"github.com/stubbedev/lys-ui/internal/gputest"
)

// openDevice opens the test's device (internal/gputest picks the
// driver); with none the test skips, unless LYS_UI_TEST_GPU asks for
// one.
func openDevice(t testing.TB) *Device {
	t.Helper()
	required, err := gputest.SetupTest(t)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Open()
	if errors.Is(err, ErrNoVulkan) || errors.Is(err, ErrNoDevice) {
		if required {
			t.Fatalf("no GPU device for LYS_UI_TEST_GPU=%s: %v", os.Getenv("LYS_UI_TEST_GPU"), err)
		}
		t.Skipf("no Vulkan device: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	t.Logf("device: %s", d.Name())
	return d
}

// exportingDevice is openDevice for a test of dmabuf targets: it
// skips on a device that renders only offscreen (lavapipe without a
// DRM node, as in CI).
func exportingDevice(t testing.TB) *Device {
	t.Helper()
	d := openDevice(t)
	if !d.Exports() {
		if _, err := d.NewTarget(4, 4, dmabuf.FormatARGB8888, []uint64{0}); !errors.Is(err, ErrNoDmabuf) {
			t.Fatalf("NewTarget on a device without dmabuf export: %v, want ErrNoDmabuf", err)
		}
		t.Skipf("%s does not export dmabufs", d.Name())
	}
	return d
}

// A target's memory is a dmabuf in a modifier the device renders to,
// and what the GPU writes into it reads back.
func TestTargetClearsAndReadsBack(t *testing.T) {
	d := exportingDevice(t)
	mods, err := d.Modifiers(dmabuf.FormatARGB8888)
	if err != nil || len(mods) == 0 {
		t.Fatalf("modifiers %v, %v", mods, err)
	}
	tg, err := d.NewTarget(64, 32, dmabuf.FormatARGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	buf := tg.Dmabuf()
	if err := buf.Validate(); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range mods {
		found = found || m == buf.Modifier
	}
	if !found || buf.Planes[0].Stride < 64*4 {
		t.Errorf("dmabuf %+v, want one of %v and a stride of at least a row", buf, mods)
	}
	if err := tg.clear([4]float32{0.2, 0.4, 0.6, 1}); err != nil {
		t.Fatal(err)
	}
	img, err := tg.ReadPixels()
	if err != nil {
		t.Fatal(err)
	}
	if c := img.RGBAAt(40, 20); c.R != 51 || c.G != 102 || c.B != 153 || c.A != 255 {
		t.Errorf("read back %v, want the cleared {51 102 153 255}", c)
	}
}

// A size or format the device cannot target fails at once.
func TestNewTargetRejectsBadRequests(t *testing.T) {
	d := exportingDevice(t)
	mods, _ := d.Modifiers(dmabuf.FormatARGB8888)
	if _, err := d.NewTarget(0, 10, dmabuf.FormatARGB8888, mods); err == nil {
		t.Error("a zero-width target was made")
	}
	if _, err := d.NewTarget(10, 10, 0x3231564e, mods); err == nil {
		t.Error("an NV12 target was made")
	}
	if _, err := d.NewTarget(10, 10, dmabuf.FormatARGB8888, nil); err == nil {
		t.Error("a target with no modifier was made")
	}
}

// Closing the device closes its targets, and a target closed after
// its device does nothing.
func TestDeviceCloseClosesTargets(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if !tg.closed {
		t.Fatal("the device closed and left its target open")
	}
	tg.Close()
}
