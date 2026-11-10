package va

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/stubbedev/lys-lib/testbuild"
)

// A display opens on each render node a VA driver serves and lists its
// encode entrypoints; without libva, or without a driver for a node,
// opening reports it. Where the host has a VA driver this exercises
// every call up to the encode itself.
func TestOpenListsEncoders(t *testing.T) {
	nodes, _ := filepath.Glob("/dev/dri/renderD*")
	if len(nodes) == 0 {
		t.Skip("no DRM render node")
	}
	for _, n := range nodes {
		d, err := Open(n)
		if errors.Is(err, ErrNoLibrary) {
			t.Skipf("no libva: %v", err)
		}
		if err != nil {
			t.Logf("%s: %v", n, err)
			continue
		}
		for _, p := range []struct {
			name    string
			profile int32
		}{{"H.264 High", ProfileH264High}, {"H.264 Main", ProfileH264Main}, {"HEVC Main", ProfileHEVCMain}} {
			t.Logf("%s (%s): %s entrypoints %v", n, d.Vendor(), p.name, d.Entrypoints(p.profile))
		}
		d.Close()
	}
}

// A coded buffer reads as its segments joined behind what the caller
// has, without allocating when the caller has room, and a segment the
// picture overflowed is an error that keeps what the caller had.
func TestCodedSegmentsJoin(t *testing.T) {
	first, second := []byte{1, 2, 3}, []byte{4, 5}
	tail := CodedBufferSegment{Size: 2, Buf: unsafe.Pointer(&second[0])}
	empty := CodedBufferSegment{Next: unsafe.Pointer(&tail)}
	head := CodedBufferSegment{Size: 3, Buf: unsafe.Pointer(&first[0]), Next: unsafe.Pointer(&empty)}
	dst := make([]byte, 0, 16)
	var out []byte
	if n := testing.AllocsPerRun(100, func() { out, _ = appendCoded(dst[:1], &head) }); n != 0 && !testbuild.Race {
		t.Errorf("%v allocations joining segments into a buffer with room, want 0", n)
	}
	if want := []byte{0, 1, 2, 3, 4, 5}; !bytes.Equal(out, want) {
		t.Errorf("joined %v, want %v", out, want)
	}
	tail.Status = CodedBufStatusSliceOverflowMask
	if out, err := appendCoded(dst[:1], &head); err == nil || len(out) != 1 {
		t.Errorf("a picture that overflowed its buffer read as %v, %v, want an error and the caller's byte", out, err)
	}
}

// A miscellaneous parameter buffer is its type, in the machine's byte
// order as the C struct holds it, then the parameter, appended without
// allocating into a buffer with room.
func TestMiscParameterLaysOutItsTypeThenItsParameter(t *testing.T) {
	rc := EncMiscParameterFrameRate{Framerate: 60}
	param := unsafe.Slice((*byte)(unsafe.Pointer(&rc)), unsafe.Sizeof(rc))
	dst := make([]byte, 0, 64)
	var out []byte
	if n := testing.AllocsPerRun(100, func() { out = appendMisc(dst[:0], EncMiscParameterTypeFrameRate, param) }); n != 0 && !testbuild.Race {
		t.Errorf("%v allocations laying out a parameter into a buffer with room, want 0", n)
	}
	if len(out) != 4+len(param) || *(*int32)(unsafe.Pointer(&out[0])) != EncMiscParameterTypeFrameRate || !bytes.Equal(out[4:], param) {
		t.Errorf("laid out %v for type %d and parameter %v", out, EncMiscParameterTypeFrameRate, param)
	}
}

func TestGenericValueIntegerIsTheUnionsInt(t *testing.T) {
	v := GenericValue{Type: GenericValueTypeInteger, Value: 0xdeadbeef_fffffffb}
	if got := v.integer(); got != -5 {
		t.Fatalf("integer() = %d, want -5", got)
	}
}
