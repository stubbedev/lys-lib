package gpu

import (
	"errors"
	"os"
	"testing"

	"github.com/stubbedev/lys-ui/dmabuf"
)

// openDevice opens the test's device: the lavapipe CPU driver when
// LYS_UI_TEST_GPU=lavapipe (CI, no GPU needed), else the host's. With
// no Vulkan the tests skip, unless LYS_UI_TEST_GPU names a driver.
func openDevice(t *testing.T) *Device {
	t.Helper()
	if os.Getenv("LYS_UI_TEST_GPU") == "lavapipe" {
		icd := os.Getenv("LYS_UI_TEST_LAVAPIPE")
		if icd == "" {
			t.Fatal("LYS_UI_TEST_GPU=lavapipe without LYS_UI_TEST_LAVAPIPE (the dev shell sets it)")
		}
		t.Setenv("VK_DRIVER_FILES", icd)
	}
	d, err := Open()
	if errors.Is(err, ErrNoVulkan) || errors.Is(err, ErrNoDevice) {
		if os.Getenv("LYS_UI_TEST_GPU") != "" {
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

// A target's memory is a dmabuf in a modifier the device renders to,
// and what the GPU writes into it reads back.
func TestTargetClearsAndReadsBack(t *testing.T) {
	d := openDevice(t)
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
	d := openDevice(t)
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
