package nal

// Reader reads an RBSP's bits, most significant first, with the
// Exp-Golomb codes the syntax uses. Reading past the end yields zeros
// and marks the reader Short, which a parser checks once at the end.
type Reader struct {
	b     []byte
	i     int
	short bool
}

// NewReader reads rbsp, whose emulation prevention bytes Unescape has
// removed.
func NewReader(rbsp []byte) Reader { return Reader{b: rbsp} }

// U reads an n-bit unsigned value, u(n), n up to 32.
func (r *Reader) U(n int) uint32 {
	var v uint32
	for range n {
		if r.i >= 8*len(r.b) {
			r.short = true
			return 0
		}
		v = v<<1 | uint32(r.b[r.i>>3]>>(7-r.i&7)&1)
		r.i++
	}
	return v
}

// Flag reads one bit as a flag, u(1).
func (r *Reader) Flag() bool { return r.U(1) == 1 }

// Skip passes over n bits.
func (r *Reader) Skip(n int) {
	r.i += n
	if r.i > 8*len(r.b) {
		r.short = true
	}
}

// UE reads an unsigned Exp-Golomb code, ue(v). A code past 32 bits
// marks the reader Short.
func (r *Reader) UE() uint32 {
	zeros := 0
	for r.U(1) == 0 {
		if zeros++; zeros > 31 || r.short {
			r.short = true
			return 0
		}
	}
	return 1<<zeros - 1 + r.U(zeros)
}

// SE reads a signed Exp-Golomb code, se(v).
func (r *Reader) SE() int32 {
	k := r.UE()
	half := int32(k >> 1)
	if k&1 == 1 {
		return half + 1
	}
	return -half
}

// Short reports whether a read ran past the end of the RBSP.
func (r *Reader) Short() bool { return r.short }

// Pos is how many bits have been read.
func (r *Reader) Pos() int { return r.i }

// MoreRBSPData is more_rbsp_data(): whether syntax remains before the
// RBSP's trailing bits, the last one bit and the zeros after it.
func (r *Reader) MoreRBSPData() bool {
	last := len(r.b) - 1
	for last >= 0 && r.b[last] == 0 {
		last--
	}
	if last < 0 {
		return false
	}
	stop := 8*last + 7
	for b := r.b[last]; b&1 == 0; b >>= 1 {
		stop--
	}
	return r.i < stop
}

// Unescape appends nal to dst with its emulation prevention bytes, the
// 3 after each pair of zero bytes, removed: the RBSP Reader reads.
func Unescape(dst, nal []byte) []byte {
	zeros := 0
	for _, c := range nal {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		dst = append(dst, c)
	}
	return dst
}

// U8 reads an n-bit value, n up to 8.
func (r *Reader) U8(n int) uint8 { return uint8(r.U(min(n, 8)) & 0xff) }

// U16 reads an n-bit value, n up to 16.
func (r *Reader) U16(n int) uint16 { return uint16(r.U(min(n, 16)) & 0xffff) }
