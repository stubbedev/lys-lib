package capture

import (
	"errors"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/gelm/app"
	"github.com/stubbedev/gelm/widget"
)

// drmFourccXRGB8888 is DRM_FORMAT_XRGB8888 ('XR24').
const drmFourccXRGB8888 = 0x34325258

// udmabufCreate is struct udmabuf_create; udmabufIoctl is
// UDMABUF_CREATE, _IOW('u', 0x42, struct udmabuf_create).
type udmabufCreate struct {
	memfd  uint32
	flags  uint32
	offset uint64
	size   uint64
}

const udmabufIoctl = 0x40187542

// newUdmabuf allocates a linear, CPU-visible dmabuf through
// /dev/udmabuf: a sealed memfd wrapped as a dmabuf, so the test can
// read what the compositor's GPU copy wrote without any GPU library.
func newUdmabuf(t *testing.T, size int) (dmabufFd int, mem []byte) {
	t.Helper()
	dev, err := os.OpenFile("/dev/udmabuf", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no udmabuf: %v", err)
	}
	defer dev.Close()
	memfd, err := unix.MemfdCreate("gelm-capture-udmabuf", unix.MFD_ALLOW_SEALING|unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(memfd) })
	if err := unix.Ftruncate(memfd, int64(size)); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(uintptr(memfd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK); err != nil {
		t.Fatal(err)
	}
	req := udmabufCreate{memfd: uint32(memfd), flags: unix.O_CLOEXEC, size: uint64(size)}
	fd, _, errno := unix.Syscall(unix.SYS_IOCTL, dev.Fd(), udmabufIoctl, uintptr(unsafe.Pointer(&req)))
	if errno != 0 {
		t.Skipf("UDMABUF_CREATE: %v", errno)
	}
	t.Cleanup(func() { _ = unix.Close(int(fd)) })
	mem, err = unix.Mmap(memfd, 0, size, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Munmap(mem) })
	return int(fd), mem
}

// TestHeadlessDmabufCapture runs the zero-copy path end to end: probe
// the output's dmabuf format, import a udmabuf as the target, copy a
// frame into it, and read the pixels back through the memfd. It needs
// a GPU-rendering compositor (sway's gles2 renderer); the pixman gate
// skips it and covers the negative probe instead.
func TestHeadlessDmabufCapture(t *testing.T) {
	requireHeadless(t)
	c := connectCapture(t)
	if !c.HasDmabuf() {
		t.Skip("compositor offers no linux-dmabuf (CPU renderer)")
	}
	showApp(t, func(a *app.Application, out *app.Output) error {
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
	out := c.Outputs()[0]
	format, err := c.DmabufFormat(out, false)
	if errors.Is(err, ErrUnsupported) {
		t.Skip("compositor offers no dmabuf screencopy target")
	}
	if err != nil {
		t.Fatal(err)
	}
	if format.Width != int(out.Width) || format.Height != int(out.Height) {
		t.Fatalf("dmabuf format %+v for a %dx%d output", format, out.Width, out.Height)
	}
	cached, err := c.DmabufFormat(out, false)
	if err != nil || cached != format {
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
		bad := connectCapture(t)
		_, err := bad.ImportDmabuf(Dmabuf{
			Width: format.Width, Height: format.Height * 4, Fourcc: drmFourccXRGB8888,
			Planes: []DmabufPlane{{Fd: uintptr(fd), Stride: uint32(stride)}},
		})
		if err == nil {
			t.Fatal("oversized import accepted")
		}
		if err := bad.Refresh(); err == nil {
			t.Fatal("a dead connection refreshed cleanly")
		}
	})

	buf, err := c.ImportDmabuf(Dmabuf{
		Width: format.Width, Height: format.Height, Fourcc: format.Fourcc,
		Planes: []DmabufPlane{{Fd: uintptr(fd), Stride: uint32(stride)}},
	})
	if errors.Is(err, ErrDmabufRejected) {
		t.Skip("compositor cannot import udmabuf memory")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer buf.Destroy()
	// Some GPUs (NVIDIA among them) import a linear dmabuf for
	// sampling only and cannot render the copy into it; the
	// compositor then fails the copy, which is its verdict, not ours.
	if _, err := c.CaptureOutputDmabuf(out, false, buf); errors.Is(err, ErrFailed) {
		t.Skip("compositor cannot render into a linear udmabuf on this GPU")
	}
	eventually(t, "red frame in the dmabuf", func() error {
		if _, err := c.CaptureOutputDmabuf(out, false, buf); err != nil {
			return err
		}
		f := &Frame{Width: format.Width, Height: format.Height, Stride: stride, Format: FormatFromFourcc(format.Fourcc), Data: mem}
		img, err := f.Image()
		if err != nil {
			return err
		}
		return wantColor(img, format.Width/2, format.Height/2, fillRed)
	})
}
