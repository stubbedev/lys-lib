package dmabufwl

import (
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/intconv"
)

// Device decodes a dev_t sent as a wl_array, the shape of
// linux-dmabuf feedback's main_device and ext-image-copy-capture's
// dmabuf_device.
func Device(words []int32) (uint64, error) {
	vs, err := uint64s(words)
	if err != nil {
		return 0, fmt.Errorf("dmabuf: dev_t: %w", err)
	}
	if len(vs) != 1 {
		return 0, fmt.Errorf("dmabuf: a dev_t of %d bytes, want 8", 8*len(vs))
	}
	return vs[0], nil
}

// Modifiers decodes a wl_array of DRM format modifiers, such as
// ext-image-copy-capture's dmabuf_format sends, into the explicit ones
// in their order: implicit layouts and repeats left out.
func Modifiers(words []int32) ([]fourcc.Modifier, error) {
	vs, err := uint64s(words)
	if err != nil {
		return nil, fmt.Errorf("dmabuf: modifiers: %w", err)
	}
	var mods []fourcc.Modifier
	for _, v := range vs {
		mods = appendExplicit(mods, fourcc.Modifier(v))
	}
	return mods, nil
}

// appendExplicit appends m to mods unless it is the implicit layout or
// already listed.
func appendExplicit(mods []fourcc.Modifier, m fourcc.Modifier) []fourcc.Modifier {
	if m == fourcc.Invalid || slices.Contains(mods, m) {
		return mods
	}
	return append(mods, m)
}

// uint64s reassembles the host-order 64-bit values of a wl_array the
// binding hands out as 32-bit words.
func uint64s(words []int32) ([]uint64, error) {
	if len(words)%2 != 0 {
		return nil, fmt.Errorf("an array of %d bytes holds no whole 64-bit values", 4*len(words))
	}
	raw := make([]byte, 0, 4*len(words))
	for _, w := range words {
		raw = binary.NativeEndian.AppendUint32(raw, intconv.Uint32Bits(w))
	}
	out := make([]uint64, len(words)/2)
	for i := range out {
		out[i] = binary.NativeEndian.Uint64(raw[8*i:])
	}
	return out, nil
}
