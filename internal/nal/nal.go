// Package nal writes H.264 and H.265 NAL units: the bit writer with
// Exp-Golomb codes the syntax uses, the escaping of start-code-like
// byte runs, and the parameter sets an encoder that does not write its
// own (VA-API's, given packed headers) puts in the stream.
package nal

// Writer collects a NAL unit's RBSP bits, most significant first.
type Writer struct {
	buf  []byte
	cur  uint64
	bits uint
}

// U writes v's low n bits, n up to 32.
func (w *Writer) U(n uint, v uint32) {
	w.cur = w.cur<<n | uint64(v)&(1<<n-1)
	w.bits += n
	for w.bits >= 8 {
		w.bits -= 8
		w.buf = append(w.buf, byte(w.cur>>w.bits))
	}
}

// Flag writes one bit.
func (w *Writer) Flag(b bool) {
	if b {
		w.U(1, 1)
	} else {
		w.U(1, 0)
	}
}

// UE writes v as an unsigned Exp-Golomb code, ue(v).
func (w *Writer) UE(v uint32) {
	x := uint64(v) + 1
	n := uint(0)
	for x>>n > 1 {
		n++
	}
	w.U(n, 0)
	w.U(n+1, uint32(x))
}

// SE writes v as a signed Exp-Golomb code, se(v).
func (w *Writer) SE(v int32) {
	if v > 0 {
		w.UE(uint32(2*v - 1))
	} else {
		w.UE(uint32(-2 * v))
	}
}

// Trailing writes rbsp_trailing_bits: a one, then zeros to the byte.
func (w *Writer) Trailing() {
	w.U(1, 1)
	if w.bits > 0 {
		w.U(8-w.bits, 0)
	}
}

// Unit is the NAL unit of header bytes and the RBSP written so far
// (byte-aligned by Trailing), emulation prevention bytes inserted.
func (w *Writer) Unit(header ...byte) []byte {
	out := append([]byte(nil), header...)
	zeros := 0
	for _, b := range w.buf {
		if zeros == 2 && b <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// AnnexB joins NAL units, each behind a four-byte start code.
func AnnexB(units ...[]byte) []byte {
	var out []byte
	for _, u := range units {
		out = append(out, 0, 0, 0, 1)
		out = append(out, u...)
	}
	return out
}
