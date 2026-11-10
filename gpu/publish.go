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

// Publish hands t to its consumer with what the passes ended so far
// drew into it: what Render does for the target it draws. The passes
// are submitted, with everything else the device holds back, and t's
// dmabuf gets a fence that signals once they are done, which
// linux-dmabuf's implicit synchronization has every reader wait on;
// Publish then returns without waiting. Where no such fence tells the
// consumer (NVIDIA's driver, which does not wait on one when a
// compositor samples a buffer; a device without sync_files; a kernel
// that attaches none to a dmabuf) Publish waits here until the passes
// are done. Either way the consumer may take t when it returns. An
// offscreen target has no consumer: its passes are submitted, no more.
func (d *Device) Publish(t *Target) error {
	switch {
	case t == nil || t.closed:
		return errors.New("gpu: Publish of a closed target")
	case t.s.d != d:
		return errors.New("gpu: Publish of another device's target")
	case d.active != nil:
		return errors.New("gpu: Publish while a pass is open")
	}
	if t.offscreen {
		return d.flushBatch()
	}
	signalled := d.publishes(t)
	if signalled {
		if err := d.signalFrame(); err != nil {
			return err
		}
	}
	return d.publish(t, signalled)
}

// signalFrame leaves the frame semaphore signalled by a submission that
// everything handed to the device so far is in or before: the one that
// already is, when nothing is held back since, else a new one carrying
// what is.
func (d *Device) signalFrame() error {
	if d.frameSignal != 0 && d.batch == 0 && len(d.waits) == 0 && len(d.timelineWaits) == 0 {
		return nil
	}
	cb, err := d.endBatch()
	if err != nil {
		return err
	}
	if err := d.queue1(cb, 0, submission{signals: true}); err != nil {
		if cb != 0 {
			d.dropBatch(cb)
		}
		return err
	}
	return nil
}
