package dmabufwl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"slices"
	"syscall"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/wlr"
)

// TrancheFlags are a feedback tranche's zwp_linux_dmabuf_feedback_v1
// tranche_flags.
type TrancheFlags uint32

// TrancheScanout marks a tranche whose buffers the compositor can put on
// a display plane directly, without compositing them.
const TrancheScanout TrancheFlags = wlr.ZwpDmabufFeedbackV1TrancheFlagsScanout

// Tranche is one preference tranche of linux-dmabuf feedback: the device
// its buffers are to be allocated on (target_device, a dev_t), its
// flags, and per DRM format the explicit modifiers it takes, in the
// compositor's preference order.
type Tranche struct {
	Device    uint64
	Flags     TrancheFlags
	Modifiers map[fourcc.Format][]fourcc.Modifier
}

// Scanout reports whether the tranche carries TrancheScanout.
func (t Tranche) Scanout() bool { return t.Flags&TrancheScanout != 0 }

// Feedback is a compositor's linux-dmabuf feedback (version 4), default
// or for a surface: the device it wants buffers allocated on
// (main_device, a dev_t), its tranches in preference order, and per DRM
// format the explicit modifiers it imports across them all, tranche by
// tranche.
type Feedback struct {
	Device    uint64
	Modifiers map[fourcc.Format][]fourcc.Modifier
	Tranches  []Tranche
}

// Preferred is the tranche to allocate a buffer of format from: the
// first scanout tranche that takes format, else the first tranche that
// does. It is false when no tranche takes format in an explicit
// modifier.
func (f Feedback) Preferred(format fourcc.Format) (Tranche, bool) {
	takes := func(t Tranche) bool { return len(t.Modifiers[format]) > 0 }
	if i := slices.IndexFunc(f.Tranches, func(t Tranche) bool { return t.Scanout() && takes(t) }); i >= 0 {
		return f.Tranches[i], true
	}
	if i := slices.IndexFunc(f.Tranches, takes); i >= 0 {
		return f.Tranches[i], true
	}
	return Tranche{}, false
}

// Equal reports whether f and g name the same main device and the same
// tranches in the same order: a feedback resent unchanged needs no new
// buffers.
func (f Feedback) Equal(g Feedback) bool {
	return f.Device == g.Device && slices.EqualFunc(f.Tranches, g.Tranches, func(a, b Tranche) bool {
		return a.Device == b.Device && a.Flags == b.Flags && maps.EqualFunc(a.Modifiers, b.Modifiers, slices.Equal)
	})
}

// feedbackState parses a feedback object's events, one feedback per
// done. The format table outlives a done: a compositor resends it only
// when it changes.
type feedbackState struct {
	table    []byte
	device   uint64
	devSeen  bool
	tranche  Tranche
	tranches []Tranche
	err      error
	// done is whether a feedback finished, result and resultErr the
	// latest; update hears each one.
	done      bool
	result    Feedback
	resultErr error
	update    func(Feedback, error)
}

func (s *feedbackState) listen(fb *wlr.ZwpDmabufFeedbackV1) {
	fb.AddFormatTableHandler(s)
	fb.AddMainDeviceHandler(s)
	fb.AddTrancheTargetDeviceHandler(s)
	fb.AddTrancheFormatsHandler(s)
	fb.AddTrancheFlagsHandler(s)
	fb.AddTrancheDoneHandler(s)
	fb.AddDoneHandler(s)
}

func (s *feedbackState) fail(err error) {
	if s.err == nil {
		s.err = err
	}
}

func feedbackVersion(version uint32) error {
	if version < 4 {
		return fmt.Errorf("dmabuf: zwp_linux_dmabuf_v1 version %d has no feedback (need 4)", version)
	}
	return nil
}

// DefaultFeedback asks mgr, bound at version, for its default feedback
// and dispatches through roundtrip until the compositor has sent it
// whole. A version below 4, which has no feedback, and a compositor that
// names no main device are errors.
func DefaultFeedback(mgr *wlr.ZwpDmabufV1, version uint32, roundtrip func() error) (Feedback, error) {
	if err := feedbackVersion(version); err != nil {
		return Feedback{}, err
	}
	fb, err := mgr.GetDefaultFeedback()
	if err != nil {
		return Feedback{}, fmt.Errorf("dmabuf: get_default_feedback: %w", err)
	}
	defer func() { _ = fb.Destroy() }()
	st := &feedbackState{}
	st.listen(fb)
	for range 4 {
		if err := roundtrip(); err != nil {
			return Feedback{}, err
		}
		if st.done {
			break
		}
	}
	if !st.done {
		return Feedback{}, errors.New("dmabuf: the default feedback never finished")
	}
	return st.result, st.resultErr
}

// SurfaceFeedback follows a surface's linux-dmabuf feedback for as long
// as it lives: the first feedback and every change the compositor sends,
// such as a scanout tranche added when the surface could go on a plane
// and removed when it no longer can.
type SurfaceFeedback struct {
	fb *wlr.ZwpDmabufFeedbackV1
	st feedbackState
}

// WatchSurfaceFeedback asks mgr, bound at version, for surface's
// feedback and calls update, on the goroutine that dispatches the
// connection, with each feedback the compositor finishes, or with why it
// is malformed: a bad format table or device, or no main device. A
// version below 4, which has no feedback, is an error. The feedback is
// followed until Destroy.
func WatchSurfaceFeedback(mgr *wlr.ZwpDmabufV1, version uint32, surface *wlr.Surface, update func(Feedback, error)) (*SurfaceFeedback, error) {
	if err := feedbackVersion(version); err != nil {
		return nil, err
	}
	if surface == nil || update == nil {
		return nil, errors.New("dmabuf: surface feedback needs a surface and an update")
	}
	fb, err := mgr.GetSurfaceFeedback(surface)
	if err != nil {
		return nil, fmt.Errorf("dmabuf: get_surface_feedback: %w", err)
	}
	s := &SurfaceFeedback{fb: fb, st: feedbackState{update: update}}
	s.st.listen(fb)
	return s, nil
}

// Destroy stops following the feedback and releases its object.
func (s *SurfaceFeedback) Destroy() error {
	if err := s.fb.Destroy(); err != nil {
		return fmt.Errorf("dmabuf: destroy the surface feedback: %w", err)
	}
	return nil
}

// HandleZwpDmabufFeedbackV1FormatTable maps the format table: 16-byte
// entries of a format, padding and a modifier.
func (s *feedbackState) HandleZwpDmabufFeedbackV1FormatTable(ev wlr.ZwpDmabufFeedbackV1FormatTableEvent) {
	if ev.FdError != nil {
		s.fail(fmt.Errorf("dmabuf: format table fd: %w", ev.FdError))
		return
	}
	fd := int(ev.Fd)
	defer func() { _ = syscall.Close(fd) }()
	m, err := syscall.Mmap(fd, 0, int(ev.Size), syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		s.fail(fmt.Errorf("dmabuf: map the %d-byte format table: %w", ev.Size, err))
		return
	}
	s.table = slices.Clone(m)
	_ = syscall.Munmap(m)
}

// HandleZwpDmabufFeedbackV1MainDevice takes the device dev_t.
func (s *feedbackState) HandleZwpDmabufFeedbackV1MainDevice(ev wlr.ZwpDmabufFeedbackV1MainDeviceEvent) {
	dev, err := Device(ev.Device)
	if err != nil {
		s.fail(fmt.Errorf("%w in main_device", err))
		return
	}
	s.device, s.devSeen = dev, true
}

// HandleZwpDmabufFeedbackV1TrancheTargetDevice takes the tranche's
// device dev_t.
func (s *feedbackState) HandleZwpDmabufFeedbackV1TrancheTargetDevice(ev wlr.ZwpDmabufFeedbackV1TrancheTargetDeviceEvent) {
	dev, err := Device(ev.Device)
	if err != nil {
		s.fail(fmt.Errorf("%w in tranche_target_device", err))
		return
	}
	s.tranche.Device = dev
}

// HandleZwpDmabufFeedbackV1TrancheFormats appends a tranche's table
// entries, in order, to their formats' modifiers: implicit layouts and
// repeats left out.
func (s *feedbackState) HandleZwpDmabufFeedbackV1TrancheFormats(ev wlr.ZwpDmabufFeedbackV1TrancheFormatsEvent) {
	if s.tranche.Modifiers == nil {
		s.tranche.Modifiers = map[fourcc.Format][]fourcc.Modifier{}
	}
	for _, i := range ev.Indices {
		at := int(i) * 16
		if at+16 > len(s.table) {
			s.fail(fmt.Errorf("dmabuf: tranche index %d past the %d-entry format table", i, len(s.table)/16))
			return
		}
		format := fourcc.Format(binary.LittleEndian.Uint32(s.table[at:]))
		mod := fourcc.Modifier(binary.LittleEndian.Uint64(s.table[at+8:]))
		s.tranche.Modifiers[format] = appendExplicit(s.tranche.Modifiers[format], mod)
	}
}

// HandleZwpDmabufFeedbackV1TrancheFlags takes the tranche's flags.
func (s *feedbackState) HandleZwpDmabufFeedbackV1TrancheFlags(ev wlr.ZwpDmabufFeedbackV1TrancheFlagsEvent) {
	s.tranche.Flags = TrancheFlags(ev.Flags)
}

// HandleZwpDmabufFeedbackV1TrancheDone ends a tranche.
func (s *feedbackState) HandleZwpDmabufFeedbackV1TrancheDone(wlr.ZwpDmabufFeedbackV1TrancheDoneEvent) {
	s.tranches = append(s.tranches, s.tranche)
	s.tranche = Tranche{}
}

// HandleZwpDmabufFeedbackV1Done finishes a feedback, hands it to update
// and starts gathering the next.
func (s *feedbackState) HandleZwpDmabufFeedbackV1Done(wlr.ZwpDmabufFeedbackV1DoneEvent) {
	if len(s.tranche.Modifiers) > 0 {
		s.HandleZwpDmabufFeedbackV1TrancheDone(wlr.ZwpDmabufFeedbackV1TrancheDoneEvent{})
	}
	s.result, s.resultErr = Feedback{}, s.err
	switch {
	case s.err != nil:
	case !s.devSeen:
		s.resultErr = errors.New("dmabuf: the feedback names no main device")
	default:
		s.result = Feedback{Device: s.device, Modifiers: map[fourcc.Format][]fourcc.Modifier{}, Tranches: s.tranches}
		for _, t := range s.tranches {
			for format, mods := range t.Modifiers {
				for _, m := range mods {
					s.result.Modifiers[format] = appendExplicit(s.result.Modifiers[format], m)
				}
			}
		}
	}
	s.done = true
	s.device, s.devSeen, s.tranche, s.tranches, s.err = 0, false, Tranche{}, nil, nil
	if s.update != nil {
		s.update(s.result, s.resultErr)
	}
}
