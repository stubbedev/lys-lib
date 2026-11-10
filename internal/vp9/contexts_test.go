package vp9_test

import (
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/internal/vp9"
	"github.com/stubbedev/lys-lib/internal/vp9/vp9test"
)

const contextsSrc = "testsrc2=size=192x128:rate=25:duration=1.4"

// An intra frame decodes from frame context 0 and saves to it whatever
// frame_context_idx it codes, which is the context a
// reset_frame_context of 2 resets; and the headers say which contexts
// the frames before saved to since those were reset. The stream is one
// whose hidden frames decode from context 1, its second key frame
// coding another context or made an intra-only frame.
func TestIntraFramesDecodeFromContext0(t *testing.T) {
	arf := []string{"-cpu-used", "4", "-b:v", "200k", "-g", "20", "-keyint_min", "20", "-auto-alt-ref", "1", "-lag-in-frames", "16"}
	pkts := vp9.Packets(t, contextsSrc, append([]string{"-pass", "1"}, arf...), append([]string{"-pass", "2"}, arf...))
	hdrs := vp9test.Headers(t, pkts)
	key := slices.IndexFunc(hdrs[1:], func(h []vp9.FrameHeader) bool { return h[0].FrameType == vp9.KeyFrame }) + 1
	if key == 0 {
		t.Fatal("no key frame after the first")
	}
	const both = 0b11
	if saved := hdrs[key][0].SavedContexts; saved != both {
		t.Fatalf("the frames before the second key frame saved to contexts %04b, want the shown frames' 0 and the hidden frames' 1", saved)
	}
	for _, c := range []struct {
		name  string
		frame []byte
		reset uint8
		after uint8
	}{
		{"key frame", pkts[key], 0, 0b01},
		{"key frame coding context 2", vp9test.KeyContext(t, pkts[key], 2), 2, 0b01},
		{"intra-only, resetting every context", vp9test.IntraOnly{Reset: vp9.ResetAllContexts, Refresh: 0xff, Context: 2}.Of(t, pkts[key]), 2, 0b01},
		{"intra-only, resetting context 1", vp9test.IntraOnly{Reset: vp9.ResetOneContext, Refresh: 0xff, Context: 1}.Of(t, pkts[key]), 1, 0b01},
		{"intra-only, resetting context 0", vp9test.IntraOnly{Reset: vp9.ResetOneContext, Refresh: 0xff}.Of(t, pkts[key]), 0, both},
		{"intra-only, resetting context 3", vp9test.IntraOnly{Reset: vp9.ResetOneContext, Refresh: 0xff, Context: 3}.Of(t, pkts[key]), 3, both},
		{"intra-only, resetting no context", vp9test.IntraOnly{Reset: 1, Refresh: 0xff, Context: 1}.Of(t, pkts[key]), 1, both},
	} {
		t.Run(c.name, func(t *testing.T) {
			cut := slices.Concat(pkts[:key], [][]byte{c.frame}, pkts[key+1:])
			hdrs := vp9test.Headers(t, cut)
			h, next := &hdrs[key][0], &hdrs[key+1][0]
			if h.FrameContextIdx != 0 || h.ResetContextIdx != c.reset {
				t.Errorf("the frame decodes from context %d and names context %d to reset, want 0 and %d", h.FrameContextIdx, h.ResetContextIdx, c.reset)
			}
			if h.SavedContexts != both {
				t.Errorf("the frame finds contexts %04b saved to, want %04b", h.SavedContexts, both)
			}
			if next.SavedContexts != c.after {
				t.Errorf("the frame after finds contexts %04b saved to, want %04b", next.SavedContexts, c.after)
			}
		})
	}
}

// Error resilient frames, which reset every context and save to none,
// leave none saved to.
func TestErrorResilientFramesSaveNoContext(t *testing.T) {
	pkts := vp9.Packets(t, contextsSrc, []string{"-cpu-used", "4", "-g", "12", "-error-resilient", "1"})
	for i, h := range vp9test.Headers(t, pkts) {
		if !h[0].ErrorResilientMode {
			t.Fatalf("frame %d is not error resilient", i)
		}
		if h[0].FrameContextIdx != 0 || h[0].SavedContexts != 0 {
			t.Errorf("frame %d decodes from context %d and finds contexts %04b saved to, want 0 and none", i, h[0].FrameContextIdx, h[0].SavedContexts)
		}
	}
}
