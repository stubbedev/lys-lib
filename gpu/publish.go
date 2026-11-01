package gpu

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/dmabuf"
)

// nvidiaVendor is the PCI vendor of NVIDIA's GPUs.
const nvidiaVendor = 0x10de

// publishes reports whether handing t over will attach a fence to its
// dmabuf, which the submission before it must signal for.
func (d *Device) publishes(t *Target) bool {
	return !t.offscreen && d.implicitFences
}

// publish tells t's consumer when the submission just made is done: a
// fence on t's dmabuf, which linux-dmabuf's implicit synchronization has
// every consumer wait on, or else by waiting here until it is. NVIDIA's
// driver does not wait on such a fence when a compositor samples a
// buffer, so there the frame finishes first. An offscreen target has no
// consumer outside the device's own queue, which runs in order.
// signalled is what publishes said before that submission.
func (d *Device) publish(t *Target, signalled bool) error {
	if t.offscreen {
		return nil
	}
	if !signalled {
		return d.idle()
	}
	fd, err := d.exportFrameSignal()
	if err != nil {
		return err
	}
	if fd < 0 {
		return d.idle()
	}
	err = dmabuf.ImportSyncFile(t.buf.Planes[0].FD, fd)
	if cerr := unix.Close(fd); cerr != nil && err == nil {
		err = fmt.Errorf("gpu: close the frame's sync_file: %w", cerr)
	}
	if errors.Is(err, dmabuf.ErrNoSyncFileImport) {
		d.implicitFences = false
		return d.idle()
	}
	return err
}
