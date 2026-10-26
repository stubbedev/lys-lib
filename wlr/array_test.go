package wlr

import (
	"encoding/binary"
	"slices"
	"testing"

	"github.com/neurlang/wayland/wl"
)

// A wl_array of 16-bit values decodes whole, an odd count's last value
// included, and consumes its padding, so the next argument reads
// right (#339).
func TestUint16ArrayKeepsAnOddCount(t *testing.T) {
	data := binary.LittleEndian.AppendUint32(nil, 6)
	data = append(data, 1, 0, 2, 0, 3, 0, 0, 0)
	data = binary.LittleEndian.AppendUint32(data, 0xfeedface)
	ev := &wl.Event{Data: data}
	if got := uint16Array(ev); !slices.Equal(got, []uint16{1, 2, 3}) {
		t.Errorf("decoded %v, want [1 2 3]", got)
	}
	if next := ev.Uint32(); next != 0xfeedface {
		t.Errorf("the argument after the array reads %#x: the padding was not consumed", next)
	}
}
