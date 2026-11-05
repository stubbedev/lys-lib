package drm

import (
	"errors"
	"fmt"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestAVblankCountTheDriverLacksIsItsOwnError(t *testing.T) {
	for name, c := range map[string]struct {
		arg crtcGetSequence
		err error
	}{
		"sequence 0": {arg: crtcGetSequence{active: 1, sequenceNS: 123}},
		"EOPNOTSUPP": {err: fmt.Errorf("drm: DRM_IOCTL_CRTC_GET_SEQUENCE: %w", unix.EOPNOTSUPP)},
	} {
		if _, err := c.arg.vblank("card0", c.err); !errors.Is(err, ErrNoVblankSequence) {
			t.Errorf("%s read as %v, want ErrNoVblankSequence", name, err)
		}
	}
	if _, err := (crtcGetSequence{}).vblank("card0", unix.EINVAL); !errors.Is(err, unix.EINVAL) || errors.Is(err, ErrNoVblankSequence) {
		t.Errorf("an inactive CRTC read as %v", err)
	}
	v, err := (crtcGetSequence{active: 1, sequence: 7, sequenceNS: 123}).vblank("card0", nil)
	if err != nil || v != (Vblank{Sequence: 7, Time: 123 * time.Nanosecond, Active: true}) {
		t.Errorf("sequence 7 read as %+v, %v", v, err)
	}
}

func TestCRTCGetSequenceMatchesTheKernelLayout(t *testing.T) {
	if n := unsafe.Sizeof(crtcGetSequence{}); n != 24 {
		t.Errorf("struct drm_crtc_get_sequence is 24 bytes, not %d", n)
	}
	if got, want := iowr(ioctlCRTCGetSequence, unsafe.Sizeof(crtcGetSequence{})), uintptr(0xc018643b); got != want {
		t.Errorf("DRM_IOCTL_CRTC_GET_SEQUENCE is %#x, want %#x", got, want)
	}
}
