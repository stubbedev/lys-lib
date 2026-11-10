package opus

// codeExtra is the bits of the first byte the decoder's register takes
// beyond whole symbols.
const codeExtra = (codeBits-2)%symBits + 1

// rangeDecoder is RFC 6716's ec_dec: it reads range-coded symbols from
// the front of a frame and raw bits from its back.
type rangeDecoder struct {
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
	// failed is set when a uniform symbol decodes out of range, which
	// only a corrupt frame does.
	failed bool
}

func (d *rangeDecoder) readByte() int {
	if d.offs < len(d.buf) {
		b := d.buf[d.offs]
		d.offs++
		return int(b)
	}
	return 0
}

func (d *rangeDecoder) readByteFromEnd() uint32 {
	if d.endOffs < len(d.buf) {
		d.endOffs++
		return uint32(d.buf[len(d.buf)-d.endOffs])
	}
	return 0
}

func (d *rangeDecoder) normalize() {
	for d.rng <= codeBot {
		d.nbitsTotal += symBits
		d.rng <<= symBits
		sym := d.rem
		d.rem = d.readByte()
		sym = (sym<<symBits | d.rem) >> (symBits - codeExtra)
		d.val = ((d.val << symBits) + sym32(symMax&^sym)) & (codeTop - 1)
	}
}

// reset starts decoding frame.
func (d *rangeDecoder) reset(frame []byte) {
	*d = rangeDecoder{buf: frame, rng: 1 << codeExtra}
	d.nbitsTotal = codeBits + 1 - ((codeBits-codeExtra)/symBits)*symBits
	d.rem = d.readByte()
	d.val = d.rng - 1 - sym32(d.rem>>(symBits-codeExtra))
	d.normalize()
}

// decode is the cumulative frequency of the next symbol out of ft;
// update must follow.
func (d *rangeDecoder) decode(ft uint32) uint32 {
	d.ext = d.rng / ft
	s := d.val / d.ext
	return ft - min(s+1, ft)
}

// decodeBin is decode with ft = 1<<ftb.
func (d *rangeDecoder) decodeBin(ftb uint) uint32 {
	d.ext = d.rng >> ftb
	s := d.val / d.ext
	return 1<<ftb - min(s+1, 1<<ftb)
}

// update consumes the symbol [fl, fh) out of ft that decode found.
func (d *rangeDecoder) update(fl, fh, ft uint32) {
	s := d.ext * (ft - fh)
	d.val -= s
	if fl > 0 {
		d.rng = d.ext * (fh - fl)
	} else {
		d.rng -= s
	}
	d.normalize()
}

// bitLogp reads a bit that is set with probability 1/(1<<logp).
func (d *rangeDecoder) bitLogp(logp uint) bool {
	r := d.rng
	s := r >> logp
	set := d.val < s
	if set {
		d.rng = s
	} else {
		d.val -= s
		d.rng = r - s
	}
	d.normalize()
	return set
}

// icdf reads a symbol with the inverse CDF icdf over 1<<ftb.
func (d *rangeDecoder) icdf(icdf []uint8, ftb uint) int { return int(d.icdf32(icdf, ftb)) }

// icdf32 is icdf as the int32 SILK's fixed-point arithmetic takes.
func (d *rangeDecoder) icdf32(icdf []uint8, ftb uint) int32 {
	s := d.rng
	r := s >> ftb
	ret := int32(-1)
	var t uint32
	for {
		t = s
		ret++
		s = r * uint32(icdf[ret])
		if d.val >= s {
			break
		}
	}
	d.val -= s
	d.rng = t - s
	d.normalize()
	return ret
}

// uint reads a value uniform in [0, ft), ft > 1.
func (d *rangeDecoder) uint(ft uint32) uint32 {
	ft--
	ftb := ilog(ft)
	if ftb <= uintBits {
		s := d.decode(ft + 1)
		d.update(s, s+1, ft+1)
		return s
	}
	ftb -= uintBits
	top := ft>>ftb + 1
	s := d.decode(top)
	d.update(s, s+1, top)
	v := s<<ftb | d.bits(uint(ftb))
	if v > ft {
		d.failed = true
		return ft
	}
	return v
}

// bits reads n raw bits (n <= 25) from the end of the frame.
func (d *rangeDecoder) bits(n uint) uint32 {
	window := d.endWindow
	avail := d.nendBits
	if avail < int(n) {
		for avail <= windowLen-symBits {
			window |= d.readByteFromEnd() << avail
			avail += symBits
		}
	}
	v := window & (1<<n - 1)
	d.endWindow = window >> n
	d.nendBits = avail - int(n)
	d.nbitsTotal += int(n)
	return v
}

func (d *rangeDecoder) tell() int { return tellBits(d.nbitsTotal, d.rng) }

func (d *rangeDecoder) tellFrac() int { return tellFracBits(d.nbitsTotal, d.rng) }

// sym32 is a byte-sized symbol value, which fits uint32.
func sym32(v int) uint32 { return uint32(v & symMax) }
