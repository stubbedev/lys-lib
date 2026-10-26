package dmabufwl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"syscall"

	"github.com/stubbedev/lys-ui/dmabuf"
	"github.com/stubbedev/lys-ui/wlr"
)

// Feedback is a compositor's linux-dmabuf default feedback (version 4):
// the device it wants buffers allocated on (main_device, a dev_t) and,
// per DRM format, the explicit modifiers it imports, in its preference
// order (tranche by tranche).
type Feedback struct {
	Device    uint64
	Modifiers map[uint32][]uint64
}

// feedbackState gathers the feedback events until done.
type feedbackState struct {
	table   []byte
	device  uint64
	mods    map[uint32][]uint64
	err     error
	devSeen bool
	done    bool
}

// DefaultFeedback asks mgr, bound at version, for its default feedback
// and dispatches through roundtrip until the compositor has sent it
// whole. A version below 4, which has no feedback, and a compositor that
// names no main device are errors.
func DefaultFeedback(mgr *wlr.ZwpDmabufV1, version uint32, roundtrip func() error) (Feedback, error) {
	if version < 4 {
		return Feedback{}, fmt.Errorf("dmabuf: zwp_linux_dmabuf_v1 version %d has no feedback (need 4)", version)
	}
	fb, err := mgr.GetDefaultFeedback()
	if err != nil {
		return Feedback{}, fmt.Errorf("dmabuf: get_default_feedback: %w", err)
	}
	defer func() { _ = fb.Destroy() }()
	st := &feedbackState{mods: map[uint32][]uint64{}}
	fb.AddFormatTableHandler(st)
	fb.AddMainDeviceHandler(st)
	fb.AddTrancheFormatsHandler(st)
	fb.AddDoneHandler(st)
	for range 4 {
		if err := roundtrip(); err != nil {
			return Feedback{}, err
		}
		if st.done {
			break
		}
	}
	switch {
	case st.err != nil:
		return Feedback{}, st.err
	case !st.done:
		return Feedback{}, errors.New("dmabuf: the default feedback never finished")
	case !st.devSeen:
		return Feedback{}, errors.New("dmabuf: the default feedback names no main device")
	}
	return Feedback{Device: st.device, Modifiers: st.mods}, nil
}

// HandleZwpDmabufFeedbackV1FormatTable maps the format table: 16-byte
// entries of a format, padding and a modifier.
func (s *feedbackState) HandleZwpDmabufFeedbackV1FormatTable(ev wlr.ZwpDmabufFeedbackV1FormatTableEvent) {
	if ev.FdError != nil {
		s.err = fmt.Errorf("dmabuf: format table fd: %w", ev.FdError)
		return
	}
	fd := int(ev.Fd)
	defer func() { _ = syscall.Close(fd) }()
	m, err := syscall.Mmap(fd, 0, int(ev.Size), syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		s.err = fmt.Errorf("dmabuf: map the %d-byte format table: %w", ev.Size, err)
		return
	}
	s.table = slices.Clone(m)
	_ = syscall.Munmap(m)
}

// HandleZwpDmabufFeedbackV1MainDevice takes the device dev_t.
func (s *feedbackState) HandleZwpDmabufFeedbackV1MainDevice(ev wlr.ZwpDmabufFeedbackV1MainDeviceEvent) {
	if len(ev.Device) != 2 {
		s.err = fmt.Errorf("dmabuf: main_device of %d bytes, want a dev_t's 8", 4*len(ev.Device))
		return
	}
	s.device, s.devSeen = uint64(uint32(ev.Device[0]))|uint64(uint32(ev.Device[1]))<<32, true
}

// HandleZwpDmabufFeedbackV1TrancheFormats appends a tranche's table
// entries, in order, to their formats' modifiers: implicit layouts and
// repeats left out.
func (s *feedbackState) HandleZwpDmabufFeedbackV1TrancheFormats(ev wlr.ZwpDmabufFeedbackV1TrancheFormatsEvent) {
	for _, i := range ev.Indices {
		at := int(i) * 16
		if at+16 > len(s.table) {
			s.err = fmt.Errorf("dmabuf: tranche index %d past the %d-entry format table", i, len(s.table)/16)
			return
		}
		format := binary.LittleEndian.Uint32(s.table[at:])
		mod := binary.LittleEndian.Uint64(s.table[at+8:])
		if mod != dmabuf.ModifierInvalid && !slices.Contains(s.mods[format], mod) {
			s.mods[format] = append(s.mods[format], mod)
		}
	}
}

// HandleZwpDmabufFeedbackV1Done marks the feedback whole.
func (s *feedbackState) HandleZwpDmabufFeedbackV1Done(wlr.ZwpDmabufFeedbackV1DoneEvent) {
	s.done = true
}
