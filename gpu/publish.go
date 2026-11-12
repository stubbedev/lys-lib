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

// publish tells t's consumer when the submission just made is done
// (fenceDmabuf). An offscreen target has no consumer outside the device's
// own queue, which runs in order. signalled is what publishes said
// before that submission.
func (d *Device) publish(t *Target, signalled bool) error {
	if t.offscreen {
		return nil
	}
	return d.fenceDmabuf(t.sync, signalled)
}

// implicitSync is the dmabufs behind an image another user shares, one
// fd of each distinct dmabuf, the image's own: what linux-dmabuf's
// implicit synchronization orders the device's use of the image with
// every other user's by. The zero value is an image no one else uses.
type implicitSync struct {
	fds []int
}

// newImplicitSync is the implicit synchronization of an image over
// planes, the planes' fds staying the caller's.
func newImplicitSync(planes []dmabuf.Plane) (implicitSync, error) {
	distinct, err := distinctDmabufs(planes)
	if err != nil {
		return implicitSync{}, err
	}
	s := implicitSync{fds: make([]int, 0, len(distinct))}
	for _, fd := range distinct {
		dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			s.close()
			return implicitSync{}, fmt.Errorf("gpu: dup dmabuf %d to synchronize with its other users: %w", fd, err)
		}
		s.fds = append(s.fds, dup)
	}
	return s, nil
}

func (s *implicitSync) close() {
	for _, fd := range s.fds {
		_ = unix.Close(fd)
	}
	s.fds = nil
}

// awaitDmabuf makes the device's next submission, the one carrying the
// commands being recorded, wait on the GPU for the work on s's dmabufs
// that a use for a waits for: their writers' for a Read, every user's for
// a Write. Whoever recorded that work, in this process or another, left
// its fence on the dmabuf, as Publish and Encode do. A device without
// sync_file semaphores, or a kernel that exports no fence from a dmabuf,
// waits here instead. What fails fails that submission.
func (d *Device) awaitDmabuf(s implicitSync, a dmabuf.Access) {
	for _, fd := range s.fds {
		if err := d.awaitDmabufFences(fd, a); err != nil {
			d.waitErr = errors.Join(d.waitErr, err)
		}
	}
}

func (d *Device) awaitDmabufFences(fd int, a dmabuf.Access) error {
	if d.syncFiles {
		syncFile, err := dmabuf.ExportSyncFile(fd, a)
		if err == nil {
			if err := d.WaitSyncFile(syncFile); err != nil {
				return fmt.Errorf("gpu: wait for the fences on dmabuf %d before a %s: %w", fd, a, err)
			}
			return nil
		}
		if !errors.Is(err, dmabuf.ErrNoSyncFileExport) {
			return fmt.Errorf("gpu: %w", err)
		}
	}
	d.blockingWaits++
	err := dmabuf.Wait(fd, a, d.waitLimit)
	if errors.Is(err, dmabuf.ErrWaitTimeout) {
		return fmt.Errorf("%w: %w", ErrTimeout, err)
	}
	return err
}

// fenceDmabuf tells whoever uses s's dmabufs next, in this process or
// another, when the submission just made is done with them: a fence on
// each dmabuf, which linux-dmabuf's implicit synchronization has every
// user wait on, or else by waiting here until it is. NVIDIA's driver
// waits on no such fence, so there the submission finishes first.
// signalled is whether that submission signals the frame semaphore, as
// the device's implicitFences said before it.
func (d *Device) fenceDmabuf(s implicitSync, signalled bool) error {
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
	for _, dmabufFD := range s.fds {
		if err = dmabuf.ImportSyncFile(dmabufFD, fd); err != nil {
			break
		}
	}
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
