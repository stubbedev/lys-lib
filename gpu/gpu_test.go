package gpu

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-ui/dmabuf"
	"github.com/stubbedev/lys-ui/internal/gputest"
	"github.com/stubbedev/lys-ui/third_party/vk"
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
	if _, err := d.NewTarget(d.maxDim+1, 10, dmabuf.FormatARGB8888, mods); err == nil || !strings.Contains(err.Error(), strconv.Itoa(d.maxDim+1)) {
		t.Errorf("a target wider than any modifier exports: %v, want an error naming its size", err)
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

// A target takes the consumer's modifiers the device renders in, linear
// among them, and refuses a list with none it can, as an implicit-only
// one, naming both lists; XRGB8888 allocates as ARGB8888 does (#330).
func TestTargetModifiersMeetTheDevices(t *testing.T) {
	d := exportingDevice(t)
	mods, err := d.Modifiers(dmabuf.FormatXRGB8888)
	if err != nil || len(mods) == 0 {
		t.Fatalf("modifiers %v, %v", mods, err)
	}
	const unknown = 0x0abc000000000001
	tg, err := d.NewTarget(16, 8, dmabuf.FormatXRGB8888, []uint64{unknown, mods[0]})
	if err != nil {
		t.Fatalf("a list holding one supported modifier: %v", err)
	}
	if b := tg.Dmabuf(); b.Modifier != mods[0] || b.Format != dmabuf.FormatXRGB8888 || len(b.Planes) != 1 {
		t.Errorf("allocated %#x in %#08x with %d planes, want %#x in XRGB8888", b.Modifier, b.Format, len(b.Planes), mods[0])
	}
	tg.Close()
	if slices.Contains(mods, dmabuf.ModifierLinear) {
		tg, err := d.NewTarget(16, 8, dmabuf.FormatARGB8888, []uint64{dmabuf.ModifierLinear})
		if err != nil || tg.Dmabuf().Modifier != dmabuf.ModifierLinear {
			t.Fatalf("linear on offer: %v", err)
		}
		tg.Close()
	}
	if _, err := d.NewTarget(16, 8, dmabuf.FormatARGB8888, []uint64{dmabuf.ModifierInvalid}); err == nil || !strings.Contains(err.Error(), "none of the modifiers") {
		t.Errorf("an implicit-only list: %v, want an error naming the lists", err)
	}
}

// OpenNode opens the device behind a render node and behind its
// primary node, which a compositor's linux-dmabuf main device may name
// instead, and refuses one no device is; a host without DRM nodes
// (lavapipe alone) skips (#330).
func TestOpenNode(t *testing.T) {
	if _, err := gputest.SetupTest(t); err != nil {
		t.Fatal(err)
	}
	render, _ := filepath.Glob("/dev/dri/renderD*")
	primary, _ := filepath.Glob("/dev/dri/card*")
	if _, err := OpenNode(0); err == nil {
		t.Error("device number 0 opened a device")
	}
	opened := 0
	var reasons []error
	for _, kind := range []struct {
		name  string
		nodes []string
	}{{"render", render}, {"primary", primary}} {
		for _, n := range kind.nodes {
			var st unix.Stat_t
			if err := unix.Stat(n, &st); err != nil {
				t.Fatal(err)
			}
			d, err := OpenNode(st.Rdev)
			if err != nil {
				reasons = append(reasons, err)
				continue
			}
			if !d.Exports() {
				t.Errorf("%s opened %s, which does not export dmabufs", n, d.Name())
			}
			t.Logf("%s node %s: %s", kind.name, n, d.Name())
			d.Close()
			opened++
			break
		}
	}
	switch opened {
	case 0:
		t.Skipf("no DRM node opens a Vulkan device: %v", errors.Join(reasons...))
	case 1:
		t.Errorf("only one of a GPU's render and primary nodes opens it: %v", errors.Join(reasons...))
	}
}

// A Buffer is allocated for transfer and sampling only, so it is
// offered the layouts the device exports but cannot render to (linear
// on NVIDIA) besides the rest; it allocates in an offered one and
// refuses a list with none it can, closing with the device (#335).
func TestBufferExportsWithoutRendering(t *testing.T) {
	d := exportingDevice(t)
	mods, err := d.ExportModifiers(dmabuf.FormatXRGB8888)
	if err != nil || len(mods) == 0 {
		t.Fatalf("export modifiers %v, %v", mods, err)
	}
	render, _ := d.Modifiers(dmabuf.FormatXRGB8888)
	t.Logf("%s: export %#x, render %#x", d.Name(), mods, render)
	if strings.Contains(d.Name(), "NVIDIA") && !slices.Contains(mods, dmabuf.ModifierLinear) {
		t.Errorf("NVIDIA exports no linear XRGB8888 buffer: %#x", mods)
	}
	for _, m := range mods {
		b, err := d.NewBuffer(64, 16, dmabuf.FormatXRGB8888, []uint64{m})
		if err != nil {
			t.Fatalf("modifier %#x: %v", m, err)
		}
		if buf := b.Dmabuf(); buf.Modifier != m || len(buf.Planes) != 1 || buf.Format != dmabuf.FormatXRGB8888 || (m == dmabuf.ModifierLinear && buf.Planes[0].Stride < 64*4) {
			t.Errorf("modifier %#x: allocated %+v", m, buf)
		}
		b.Close()
		b.Close()
	}
	if _, err := d.NewBuffer(16, 8, dmabuf.FormatXRGB8888, []uint64{dmabuf.ModifierInvalid}); err == nil || !strings.Contains(err.Error(), "none of the modifiers") {
		t.Errorf("an implicit-only list: %v, want an error naming the lists", err)
	}
	if _, err := d.NewBuffer(16, 8, dmabuf.FormatXRGB8888, []uint64{mods[0]}); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if len(d.buffers) != 0 {
		t.Errorf("%d buffers outlived their device", len(d.buffers))
	}
}

// Buffers the CPU reads back (a frame's readbacks, ReadPixels, the
// encoder's bitstream) sit in cached memory where the device has any:
// uncached, write-combined memory reads back some forty times slower
// (418 MB/s against 16 GB/s on an RTX 3060 Ti).
func TestReadBuffersAreCached(t *testing.T) {
	d := openDevice(t)
	cached := vk.MemoryPropertyFlags(vk.MemoryPropertyHostVisibleBit | vk.MemoryPropertyHostCachedBit)
	offered := false
	for i := range d.memProps.MemoryTypeCount {
		offered = offered || d.memProps.MemoryTypes[i].PropertyFlags&cached == cached
	}
	if !offered {
		t.Skipf("%s has no cached host memory", d.Name())
	}
	if err := d.ensureScratch(4, 4); err != nil {
		t.Fatal(err)
	}
	grown, err := d.readback.fit(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	d.readback = grown
	if d.readback.flags&cached != cached {
		t.Errorf("the grown readback buffer sits in memory %#x, want cached", d.readback.flags)
	}
}

// exportProofEnv marks the child run of TestExportsIsProven.
const exportProofEnv = "LYS_UI_TEST_EXPORT_PROOF"

// A driver that offers dmabuf export but cannot do it opens as a
// device that does not export, so callers draw offscreen or on the CPU
// instead of failing every frame: lavapipe offers export with
// /dev/udmabuf hidden, then fails every export allocation. The test
// reruns itself on lavapipe in a user namespace without /dev/udmabuf.
func TestExportsIsProven(t *testing.T) {
	if os.Getenv(exportProofEnv) != "" {
		d := openDevice(t)
		if d.Exports() {
			t.Errorf("%s exports with /dev/udmabuf hidden", d.Name())
		}
		return
	}
	lavapipe := os.Getenv("LYS_UI_TEST_LAVAPIPE")
	unshare, err := exec.LookPath("unshare")
	if lavapipe == "" || err != nil {
		t.Skip("needs the dev shell's lavapipe and unshare")
	}
	if _, err := os.Stat("/dev/udmabuf"); err != nil {
		t.Skip("no /dev/udmabuf to hide")
	}
	cmd := exec.Command(unshare, "-rm", "sh", "-c", `mount --bind /dev/null /dev/udmabuf && exec "$0" -test.run '^TestExportsIsProven$' -test.v`, os.Args[0])
	cmd.Env = append(os.Environ(), exportProofEnv+"=1", "LYS_UI_TEST_GPU=lavapipe")
	out, err := cmd.CombinedOutput()
	if err != nil && strings.Contains(string(out), "unshare:") {
		t.Skipf("no user namespace: %s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		t.Fatalf("the run without /dev/udmabuf: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: TestExportsIsProven") {
		t.Fatalf("the run without /dev/udmabuf did not pass:\n%s", out)
	}
}
