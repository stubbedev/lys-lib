package opus

import "math/bits"

// The range coder's register geometry (RFC 6716 section 4.1).
const (
	symBits   = 8
	codeBits  = 32
	symMax    = 1<<symBits - 1
	codeShift = codeBits - symBits - 1
	codeTop   = uint32(1) << (codeBits - 1)
	codeBot   = codeTop >> symBits
	uintBits  = 8
	windowLen = 32
)

// rangeEncoder is RFC 6716's ec_enc: range-coded symbols grow from the
// front of buf and raw bits from its back, and done joins them so a
// decoder reads exactly the bytes the reference encoder writes.
type rangeEncoder struct {
	buf        []byte
	endOffs    int
	endWindow  uint32
	nendBits   int
	nbitsTotal int
	offs       int
	rng        uint32
	val        uint32
	ext        uint32
	rem        int
	failed     bool
}

// ilog is the number of bits needed to represent v (zero for zero).
func ilog(v uint32) int { return bits.Len32(v) }

// reset starts encoding into buf, whose length is the frame size.
func (e *rangeEncoder) reset(buf []byte) {
	*e = rangeEncoder{buf: buf, nbitsTotal: codeBits + 1, rng: codeTop, rem: -1}
}

// shrink cuts the frame to size bytes, moving the raw bits already
// written at the end of buf back with it (ec_enc_shrink).
func (e *rangeEncoder) shrink(size int) {
	copy(e.buf[size-e.endOffs:size], e.buf[len(e.buf)-e.endOffs:])
	e.buf = e.buf[:size]
}

func (e *rangeEncoder) writeByte(v int) {
	if e.offs+e.endOffs >= len(e.buf) {
		e.failed = true
		return
	}
	e.buf[e.offs] = byte(v)
	e.offs++
}

func (e *rangeEncoder) writeByteAtEnd(v uint32) {
	if e.offs+e.endOffs >= len(e.buf) {
		e.failed = true
		return
	}
	e.endOffs++
	e.buf[len(e.buf)-e.endOffs] = byte(v)
}

// carryOut emits a symbol, holding back runs of 0xFF until a carry
// into them is settled.
func (e *rangeEncoder) carryOut(c int) {
	if c == symMax {
		e.ext++
		return
	}
	carry := c >> symBits
	if e.rem >= 0 {
		e.writeByte(e.rem + carry)
	}
	for ; e.ext > 0; e.ext-- {
		e.writeByte((symMax + carry) & symMax)
	}
	e.rem = c & symMax
}

func (e *rangeEncoder) normalize() {
	for e.rng <= codeBot {
		e.carryOut(int(e.val >> codeShift))
		e.val = (e.val << symBits) & (codeTop - 1)
		e.rng <<= symBits
		e.nbitsTotal += symBits
	}
}

// encode codes the symbol [fl, fh) out of ft.
func (e *rangeEncoder) encode(fl, fh, ft uint32) {
	r := e.rng / ft
	if fl > 0 {
		e.val += e.rng - r*(ft-fl)
		e.rng = r * (fh - fl)
	} else {
		e.rng -= r * (ft - fh)
	}
	e.normalize()
}

// encodeBin is encode with ft = 1<<ftb.
func (e *rangeEncoder) encodeBin(fl, fh uint32, ftb uint) {
	r := e.rng >> ftb
	if fl > 0 {
		e.val += e.rng - r*((1<<ftb)-fl)
		e.rng = r * (fh - fl)
	} else {
		e.rng -= r * ((1 << ftb) - fh)
	}
	e.normalize()
}

// bitLogp codes a bit that is set with probability 1/(1<<logp).
func (e *rangeEncoder) bitLogp(set bool, logp uint) {
	s := e.rng >> logp
	r := e.rng - s
	if set {
		e.val += r
		e.rng = s
	} else {
		e.rng = r
	}
	e.normalize()
}

// icdf codes symbol s with the inverse CDF icdf over 1<<ftb.
func (e *rangeEncoder) icdf(s int, icdf []uint8, ftb uint) {
	r := e.rng >> ftb
	if s > 0 {
		e.val += e.rng - r*uint32(icdf[s-1])
		e.rng = r * uint32(icdf[s-1]-icdf[s])
	} else {
		e.rng -= r * uint32(icdf[s])
	}
	e.normalize()
}

// uint codes fl uniformly out of ft (ft > 1): the top eight bits range
// coded, the rest raw.
func (e *rangeEncoder) uint(fl, ft uint32) {
	ft--
	ftb := ilog(ft)
	if ftb <= uintBits {
		e.encode(fl, fl+1, ft+1)
		return
	}
	ftb -= uintBits
	top := (ft >> ftb) + 1
	hi := fl >> ftb
	e.encode(hi, hi+1, top)
	e.bits(fl&(1<<ftb-1), uint(ftb))
}

// bits writes n raw bits (0 < n <= 25) at the end of the frame.
func (e *rangeEncoder) bits(fl uint32, n uint) {
	window := e.endWindow
	used := e.nendBits
	if used+int(n) > windowLen {
		for used >= symBits {
			e.writeByteAtEnd(window & symMax)
			window >>= symBits
			used -= symBits
		}
	}
	window |= fl << used
	used += int(n)
	e.endWindow = window
	e.nendBits = used
	e.nbitsTotal += int(n)
}

// tell is the number of whole bits used so far, rounded up.
func (e *rangeEncoder) tell() int { return e.nbitsTotal - ilog(e.rng) }

// tellFrac is tell in 1/8 bits.
func (e *rangeEncoder) tellFrac() int {
	nbits := e.nbitsTotal << bitRes
	l := ilog(e.rng)
	r := e.rng >> (l - 16)
	for range bitRes {
		r = r * r >> 15
		b := int(r >> 16)
		l = l<<1 | b
		r >>= b
	}
	return nbits - l
}

// done flushes the coder with the fewest bits that pin every symbol,
// zeroes the unused middle and merges the trailing raw bits.
func (e *rangeEncoder) done() {
	l := codeBits - ilog(e.rng)
	msk := (codeTop - 1) >> l
	end := (e.val + msk) &^ msk
	if end|msk >= e.val+e.rng {
		l++
		msk >>= 1
		end = (e.val + msk) &^ msk
	}
	for l > 0 {
		e.carryOut(int(end >> codeShift))
		end = (end << symBits) & (codeTop - 1)
		l -= symBits
	}
	if e.rem >= 0 || e.ext > 0 {
		e.carryOut(0)
	}
	window := e.endWindow
	used := e.nendBits
	for used >= symBits {
		e.writeByteAtEnd(window & symMax)
		window >>= symBits
		used -= symBits
	}
	if e.failed {
		return
	}
	clear(e.buf[e.offs : len(e.buf)-e.endOffs])
	if used <= 0 {
		return
	}
	if e.endOffs >= len(e.buf) {
		e.failed = true
		return
	}
	l = -l
	if e.offs+e.endOffs >= len(e.buf) && l < used {
		window &= 1<<l - 1
		e.failed = true
	}
	e.buf[len(e.buf)-e.endOffs-1] |= byte(window)
}
