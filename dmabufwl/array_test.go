package dmabufwl

import (
	"encoding/binary"
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/intconv"
)

// words lays 64-bit values out as the host-order 32-bit words a
// wl_array decodes to.
func words(vs ...uint64) []int32 {
	out := make([]int32, 0, 2*len(vs))
	for _, v := range vs {
		b := binary.NativeEndian.AppendUint64(nil, v)
		out = append(out, intconv.Int32Bits(binary.NativeEndian.Uint32(b[:4])), intconv.Int32Bits(binary.NativeEndian.Uint32(b[4:])))
	}
	return out
}

func TestDevice(t *testing.T) {
	const dev = 0x0000_0001_0000_e280
	got, err := Device(words(dev))
	if err != nil || got != dev {
		t.Fatalf("Device = %#x, %v; want %#x", got, err, uint64(dev))
	}
	for name, w := range map[string][]int32{
		"empty":      nil,
		"half":       {1},
		"two dev_ts": words(1, 2),
		"odd words":  {1, 2, 3},
	} {
		if _, err := Device(w); err == nil {
			t.Errorf("Device of %s decoded", name)
		}
	}
}

func TestModifiers(t *testing.T) {
	const tiled = fourcc.Modifier(0x0100000000000001)
	got, err := Modifiers(words(uint64(tiled), uint64(fourcc.Invalid), uint64(fourcc.Linear), uint64(tiled)))
	if err != nil || !slices.Equal(got, []fourcc.Modifier{tiled, fourcc.Linear}) {
		t.Fatalf("Modifiers = %v, %v; want the explicit ones once each, in order", got, err)
	}
	if got, err := Modifiers(nil); err != nil || len(got) != 0 {
		t.Fatalf("Modifiers of an empty array = %v, %v", got, err)
	}
	if _, err := Modifiers([]int32{1}); err == nil {
		t.Fatal("Modifiers of half a value decoded")
	}
}
