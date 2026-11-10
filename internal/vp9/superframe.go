package vp9

import "fmt"

// MaxFrames is how many frames a superframe holds at most.
const MaxFrames = 8

// Frames splits b, a packet, into its frames (Annex B): a superframe's
// by its index, else b whole. The frames are slices of b in frames.
func Frames(b []byte, frames *[MaxFrames][]byte) ([][]byte, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("%w: an empty packet", ErrSyntax)
	}
	marker := b[len(b)-1]
	if marker&0xe0 != 0xc0 {
		frames[0] = b
		return frames[:1], nil
	}
	n := int(marker&7) + 1
	mag := int(marker>>3&3) + 1
	size := 2 + mag*n
	if len(b) < size || b[len(b)-size] != marker {
		frames[0] = b
		return frames[:1], nil
	}
	index := b[len(b)-size+1:]
	data := b[:len(b)-size]
	at := 0
	for i := range n {
		var sz int
		for j := range mag {
			sz |= int(index[i*mag+j]) << (8 * j)
		}
		if sz == 0 || at+sz > len(data) {
			return nil, fmt.Errorf("%w: superframe frame %d of %d bytes at %d runs past the %d bytes before its index", ErrSyntax, i, sz, at, len(data))
		}
		frames[i] = data[at : at+sz]
		at += sz
	}
	return frames[:n], nil
}
