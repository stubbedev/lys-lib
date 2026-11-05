package drm

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const ioctlCRTCGetSequence = 0x3b

// ErrNoVblankSequence reports a driver that keeps no vblank count for
// DRM_IOCTL_CRTC_GET_SEQUENCE to read: one without vblank support,
// which fails it with EOPNOTSUPP, or NVIDIA's, which answers sequence 0
// and a time that says nothing either.
var ErrNoVblankSequence = errors.New("drm: the driver keeps no vblank sequence")

// Vblank is a CRTC's most recent vertical blank: its sequence number,
// counting up from some earlier vblank, and when its first pixel went
// out, on CLOCK_MONOTONIC. Active is whether the CRTC drives a display.
type Vblank struct {
	Sequence uint64
	Time     time.Duration
	Active   bool
}

type crtcGetSequence struct {
	crtcID     uint32
	active     uint32
	sequence   uint64
	sequenceNS int64
}

// CRTCSequence reads crtc's most recent vblank with
// DRM_IOCTL_CRTC_GET_SEQUENCE, what a frame clock re-anchors on after
// idle. A CRTC that is off fails with EINVAL, and a driver with no
// vblank count with ErrNoVblankSequence.
func (d *Device) CRTCSequence(crtc uint32) (Vblank, error) {
	arg := crtcGetSequence{crtcID: crtc}
	err := d.ioctl("DRM_IOCTL_CRTC_GET_SEQUENCE", ioctlCRTCGetSequence, unsafe.Pointer(&arg), unsafe.Sizeof(arg)) //nolint:gosec // the kernel fills this struct during the call
	return arg.vblank(d.path, err)
}

func (a crtcGetSequence) vblank(path string, err error) (Vblank, error) {
	switch {
	case errors.Is(err, unix.EOPNOTSUPP):
		return Vblank{}, fmt.Errorf("%w: %w", ErrNoVblankSequence, err)
	case err != nil:
		return Vblank{}, err
	case a.sequence == 0:
		return Vblank{}, fmt.Errorf("%w: CRTC %d on %s reads sequence 0", ErrNoVblankSequence, a.crtcID, path)
	}
	return Vblank{Sequence: a.sequence, Time: time.Duration(a.sequenceNS), Active: a.active != 0}, nil
}
