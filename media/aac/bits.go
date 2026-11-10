package aac

// bitReader reads a frame's bits most significant first. Reading past
// the end yields zeros and sets overrun, which the decoder turns into
// an error at the next element boundary.
type bitReader struct {
	data    []byte
	pos     int
	overrun bool
}

// peek is the next n (at most 25) bits without consuming them.
func (r *bitReader) peek(n uint) uint32 {
	i := r.pos >> 3
	var w uint32
	if i+4 <= len(r.data) {
		w = uint32(r.data[i])<<24 | uint32(r.data[i+1])<<16 | uint32(r.data[i+2])<<8 | uint32(r.data[i+3])
	} else {
		for k := range 4 {
			w <<= 8
			if i+k < len(r.data) {
				w |= uint32(r.data[i+k])
			}
		}
	}
	return w << (r.pos & 7) >> (32 - n)
}

// skip consumes n bits.
func (r *bitReader) skip(n uint) {
	r.pos += int(n)
	if r.pos > len(r.data)*8 {
		r.overrun = true
	}
}

// read consumes and returns the next n (at most 25) bits.
func (r *bitReader) read(n uint) uint32 {
	if n == 0 {
		return 0
	}
	v := r.peek(n)
	r.skip(n)
	return v
}

// readInt is read as an int.
func (r *bitReader) readInt(n uint) int { return int(r.read(n)) }

// bit consumes one bit.
func (r *bitReader) bit() bool { return r.read(1) == 1 }

// align skips to the next byte boundary.
func (r *bitReader) align() { r.skip(uint(-r.pos & 7)) }

// left is the number of bits not yet read.
func (r *bitReader) left() int { return len(r.data)*8 - r.pos }
