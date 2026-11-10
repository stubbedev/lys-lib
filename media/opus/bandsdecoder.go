package opus

import (
	"math"

	"github.com/stubbedev/lys-lib/intconv"
)

// normLen is the folding memory of one channel: every band but the
// last, at the longest frame.
const normLen = FrameSize * 78 / 100

// bandDecoder decodes and resynthesises the normalised band shapes of
// one frame (quant_all_bands, quant_band, quant_partition and
// quant_band_stereo, decoder side): the split angles and PVQ shapes,
// folding uncoded bands from lower ones, stereo merging, and each
// band's collapse mask for the anti-collapse pass.
type bandDecoder struct {
	rc         *rangeDecoder
	pvq        pvq
	lm         int
	spread     int
	intensity  int
	tfChange   int
	index      int
	remaining  int
	seed       uint32
	disableInv bool
	norm       [2 * normLen]float32
	tmp        [FrameSize]float32
}

// lcgRand is celt_lcg_rand, the decoder's noise generator.
func lcgRand(seed uint32) uint32 { return 1664525*seed + 1013904223 }

// decodeAll decodes bands [start, end) into x (and y in stereo), frames of
// 2.5 ms << lm in blocks short blocks, filling masks with each band's
// collapse mask per channel.
func (q *bandDecoder) decodeAll(x, y []float32, masks *[2 * nbBands]uint, a *allocation, start, end, blocks int, tfRes *[nbBands]int, totalBits int) {
	m := 1 << q.lm
	channels := 1
	if y != nil {
		channels = 2
	}
	normOffset := m * eBands[start]
	normEnd := m*eBands[nbBands-1] - normOffset
	norm, norm2 := q.norm[:normEnd], q.norm[normLen:normLen+normEnd]
	q.intensity = a.intensity
	dual := a.dualStereo
	balance := a.balance
	lowbandOffset := 0
	updateLowband := true
	for i := start; i < end; i++ {
		q.index = i
		lo, hi := m*eBands[i], m*eBands[i+1]
		n := hi - lo
		last := i == end-1
		bx := x[lo:hi]
		var by []float32
		if y != nil {
			by = y[lo:hi]
		}
		tell := q.rc.tellFrac()
		if i != start {
			balance -= tell
		}
		q.remaining = totalBits - tell - 1
		b := 0
		if i <= a.codedBands-1 {
			curr := balance / min(3, a.codedBands-i)
			b = max(0, min(16383, min(q.remaining+1, a.pulses[i]+curr)))
		}
		if (lo-n >= normOffset || i == start+1) && (updateLowband || lowbandOffset == 0) {
			lowbandOffset = i
		}
		if i == start+1 {
			n1, n2 := m*bandWidth(start), m*bandWidth(start+1)
			copy(norm[n1:n2], norm[2*n1-n2:n1])
			if dual {
				copy(norm2[n1:n2], norm2[2*n1-n2:n1])
			}
		}
		q.tfChange = tfRes[i]
		scratch := x[m*eBands[nbBands-1]:]
		if last {
			scratch = nil
		}
		effective := -1
		var xcm, ycm uint
		if lowbandOffset != 0 && (q.spread != spreadAggressive || blocks > 1 || q.tfChange < 0) {
			effective = max(0, m*eBands[lowbandOffset]-normOffset-n)
			foldStart := lowbandOffset
			for {
				foldStart--
				if m*eBands[foldStart] <= effective+normOffset {
					break
				}
			}
			foldEnd := lowbandOffset - 1
			for {
				foldEnd++
				if foldEnd >= i || m*eBands[foldEnd] >= effective+normOffset+n {
					break
				}
			}
			for f := foldStart; f < foldEnd; f++ {
				xcm |= masks[f*channels]
				ycm |= masks[f*channels+channels-1]
			}
		} else {
			xcm, ycm = 1<<blocks-1, 1<<blocks-1
		}
		if dual && i == q.intensity {
			dual = false
			for j := range lo - normOffset {
				norm[j] = 0.5 * (norm[j] + norm2[j])
			}
		}
		lowband := func(n []float32) []float32 {
			if effective < 0 {
				return nil
			}
			return n[effective:]
		}
		out := func(n []float32) []float32 {
			if last {
				return nil
			}
			return n[lo-normOffset:]
		}
		switch {
		case dual:
			xcm = q.band(bx, b/2, blocks, lowband(norm), out(norm), 1, scratch, xcm)
			ycm = q.band(by, b/2, blocks, lowband(norm2), out(norm2), 1, scratch, ycm)
		case y != nil:
			xcm = q.stereoBand(bx, by, b, blocks, lowband(norm), out(norm), scratch, xcm|ycm)
			ycm = xcm
		default:
			xcm = q.band(bx, b, blocks, lowband(norm), out(norm), 1, scratch, xcm|ycm)
			ycm = xcm
		}
		masks[i*channels] = xcm
		masks[i*channels+channels-1] = ycm
		balance += a.pulses[i] + tell
		updateLowband = b > n<<bitRes
	}
}

// n1 decodes a one-coefficient band's sign (both channels' in stereo).
func (q *bandDecoder) n1(x, y, lowbandOut []float32) uint {
	for _, v := range [2][]float32{x, y} {
		if v == nil {
			continue
		}
		neg := false
		if q.remaining >= 1<<bitRes {
			neg = q.rc.bits(1) == 1
			q.remaining -= 1 << bitRes
		}
		v[0] = 1
		if neg {
			v[0] = -1
		}
	}
	if lowbandOut != nil {
		lowbandOut[0] = x[0]
	}
	return 1
}

var (
	bitInterleave   = [16]uint{0, 1, 1, 1, 2, 3, 3, 3, 2, 3, 3, 3, 2, 3, 3, 3}
	bitDeinterleave = [16]uint{0x00, 0x03, 0x0c, 0x0f, 0x30, 0x33, 0x3c, 0x3f, 0xc0, 0xc3, 0xcc, 0xcf, 0xf0, 0xf3, 0xfc, 0xff}
)

// band decodes a mono band of b 1/8 bits in blocks short blocks into
// x, scaled to norm gain: it adapts the folding source lowband to the
// band's TF change, decodes the partitions, undoes the TF change and
// writes the folding source for the bands above into lowbandOut.
func (q *bandDecoder) band(x []float32, b, blocks int, lowband, lowbandOut []float32, gain float32, scratch []float32, fill uint) uint {
	n := len(x)
	if n == 1 {
		return q.n1(x, nil, lowbandOut)
	}
	longBlocks := blocks == 1
	nb := n / blocks
	tfChange := q.tfChange
	recombine := max(0, tfChange)
	if scratch != nil && lowband != nil && (recombine != 0 || nb&1 == 0 && tfChange < 0 || blocks > 1) {
		copy(scratch[:n], lowband[:n])
		lowband = scratch[:n]
	}
	for k := range recombine {
		if lowband != nil {
			haar1(lowband, n>>k, 1<<k)
		}
		fill = bitInterleave[fill&0xf] | bitInterleave[fill>>4]<<2
	}
	blocks >>= recombine
	nb <<= recombine
	timeDivide := 0
	for nb&1 == 0 && tfChange < 0 {
		if lowband != nil {
			haar1(lowband, nb, blocks)
		}
		fill |= fill << blocks
		blocks <<= 1
		nb >>= 1
		timeDivide++
		tfChange++
	}
	b0, nb0 := blocks, nb
	if b0 > 1 && lowband != nil {
		deinterleaveHadamard(lowband, q.tmp[:n], nb>>recombine, b0<<recombine, longBlocks)
	}
	cm := q.partition(x, b, blocks, lowband, q.lm, gain, fill)
	if b0 > 1 {
		interleaveHadamard(x, q.tmp[:n], nb>>recombine, b0<<recombine, longBlocks)
	}
	nb, blocks = nb0, b0
	for range timeDivide {
		blocks >>= 1
		nb <<= 1
		cm |= cm >> blocks
		haar1(x, nb, blocks)
	}
	for k := range recombine {
		cm = bitDeinterleave[cm]
		haar1(x, n>>k, 1<<k)
	}
	blocks <<= recombine
	if lowbandOut != nil {
		s := float32(math.Sqrt(float64(n)))
		for j, v := range x {
			lowbandOut[j] = s * v
		}
	}
	return cm & (1<<blocks - 1)
}

// interleaveHadamard undoes deinterleaveHadamard.
func interleaveHadamard(x, tmp []float32, n0, stride int, hadamard bool) {
	for i := range stride {
		src := i
		if hadamard {
			src = orderyTable[stride-2+i]
		}
		for j := range n0 {
			tmp[j*stride+i] = x[src*n0+j]
		}
	}
	copy(x, tmp[:n0*stride])
}

// partition decodes a mono band of blocks short blocks, split in
// halves as the encoder did while one PVQ cannot take its bits.
func (q *bandDecoder) partition(x []float32, b, blocks int, lowband []float32, lm int, gain float32, fill uint) uint {
	n := len(x)
	cache := cacheRow(q.index, lm)
	if lm == -1 || b <= cache[cache[0]]+12 || n <= 2 {
		return q.pulses(x, b, blocks, lowband, lm, gain, fill)
	}
	b0 := blocks
	n >>= 1
	y := x[n:]
	x = x[:n]
	lm--
	if blocks == 1 {
		fill = fill&1 | fill<<1
	}
	blocks = (blocks + 1) >> 1
	t := q.theta(n, &b, blocks, b0, lm, false, &fill)
	mid, side := float32(t.imid)/32768, float32(t.iside)/32768
	delta := t.delta
	if b0 > 1 && t.itheta&0x3fff != 0 {
		if t.itheta > 8192 {
			delta -= delta >> (4 - lm)
		} else {
			delta = min(0, delta+(n<<bitRes>>(5-lm)))
		}
	}
	mbits := max(0, min(b, (b-delta)/2))
	sbits := b - mbits
	q.remaining -= t.qalloc
	var lowband2 []float32
	if lowband != nil {
		lowband2 = lowband[n:]
		lowband = lowband[:n]
	}
	rebalance := q.remaining
	var cm uint
	if mbits >= sbits {
		cm = q.partition(x, mbits, blocks, lowband, lm, gain*mid, fill)
		rebalance = mbits - (rebalance - q.remaining)
		if rebalance > 3<<bitRes && t.itheta != 0 {
			sbits += rebalance - 3<<bitRes
		}
		cm |= q.partition(y, sbits, blocks, lowband2, lm, gain*side, fill>>blocks) << (b0 >> 1)
		return cm
	}
	cm = q.partition(y, sbits, blocks, lowband2, lm, gain*side, fill>>blocks) << (b0 >> 1)
	rebalance = sbits - (rebalance - q.remaining)
	if rebalance > 3<<bitRes && t.itheta != 16384 {
		mbits += rebalance - 3<<bitRes
	}
	return cm | q.partition(x, mbits, blocks, lowband, lm, gain*mid, fill)
}

// pulses decodes an unsplit band: its PVQ shape, or without pulses the
// folded lowband or noise where fill allows.
func (q *bandDecoder) pulses(x []float32, b, blocks int, lowband []float32, lm int, gain float32, fill uint) uint {
	k := bits2pulses(q.index, lm, b)
	cost := pulses2bits(q.index, lm, k)
	q.remaining -= cost
	for q.remaining < 0 && k > 0 {
		q.remaining += cost
		k--
		cost = pulses2bits(q.index, lm, k)
		q.remaining -= cost
	}
	if k != 0 {
		return q.pvq.unquant(q.rc, x, getPulses(k), q.spread, blocks, gain)
	}
	mask := uint(1)<<blocks - 1
	fill &= mask
	if fill == 0 {
		clear(x)
		return 0
	}
	cm := fill
	if lowband == nil {
		for j := range x {
			q.seed = lcgRand(q.seed)
			x[j] = float32(intconv.Int32Bits(q.seed) >> 20)
		}
		cm = mask
	} else {
		for j := range x {
			q.seed = lcgRand(q.seed)
			tmp := float32(1.0 / 256)
			if q.seed&0x8000 == 0 {
				tmp = -tmp
			}
			x[j] = lowband[j] + tmp
		}
	}
	renormalise(x, gain)
	return cm
}

// split is a decoded split angle: the mid and side gains in Q15, the
// bit share it implies, the angle in Q14, the 1/8 bits it took and,
// for intensity stereo, whether the side is inverted.
type split struct {
	imid, iside, delta, itheta, qalloc int
	inv                                bool
}

// theta decodes the angle of a split of n-coefficient halves (or of a
// stereo band's mid and side) with *b bits, taking the bits it used
// from *b and the halves it silences from *fill.
func (q *bandDecoder) theta(n int, b *int, blocks, b0, lm int, stereo bool, fill *uint) split {
	i := q.index
	pulseCap := logN[i] + lm<<bitRes
	offset := pulseCap >> 1
	if stereo && n == 2 {
		offset -= qthetaTwo
	} else {
		offset -= qthetaOffset
	}
	qn := computeQN(n, *b, offset, pulseCap, stereo)
	if stereo && i >= q.intensity {
		qn = 1
	}
	tell := q.rc.tellFrac()
	var t split
	switch {
	case qn != 1:
		t.itheta = q.decodeTheta(qn, n, b0, stereo) * 16384 / qn
	case stereo:
		if *b > 2<<bitRes && q.remaining > 2<<bitRes {
			t.inv = q.rc.bitLogp(2)
		}
		if q.disableInv {
			t.inv = false
		}
	}
	t.qalloc = q.rc.tellFrac() - tell
	*b -= t.qalloc
	switch t.itheta {
	case 0:
		t.imid, t.iside, t.delta = 32767, 0, -16384
		*fill &= 1<<blocks - 1
	case 16384:
		t.imid, t.iside, t.delta = 0, 32767, 16384
		*fill &= (1<<blocks - 1) << blocks
	default:
		t.imid = bitexactCos(t.itheta)
		t.iside = bitexactCos(16384 - t.itheta)
		t.delta = fracMul16((n-1)<<7, bitexactLog2Tan(t.iside, t.imid))
	}
	return t
}

// decodeTheta reads the quantised split angle, the counterpart of
// bandCoder.codeTheta.
func (q *bandDecoder) decodeTheta(qn, n, b0 int, stereo bool) int {
	rc := q.rc
	switch {
	case stereo && n > 2:
		const p0 = 3
		x0 := qn / 2
		ft := sym(p0*(x0+1) + x0)
		fs := int(rc.decode(ft))
		x := x0 + 1 + (fs - (x0+1)*p0)
		if fs < (x0+1)*p0 {
			x = fs / p0
		}
		if x <= x0 {
			rc.update(sym(p0*x), sym(p0*(x+1)), ft)
		} else {
			fl := (x - 1 - x0) + (x0+1)*p0
			rc.update(sym(fl), sym(fl+1), ft)
		}
		return x
	case stereo || b0 > 1:
		return int(rc.uint(sym(qn + 1)))
	}
	ft := ((qn >> 1) + 1) * ((qn >> 1) + 1)
	fm := int(rc.decode(sym(ft)))
	var itheta, fs, fl int
	if fm < (qn>>1)*((qn>>1)+1)>>1 {
		itheta = (isqrt32(sym(8*fm+1)) - 1) >> 1
		fs = itheta + 1
		fl = itheta * (itheta + 1) >> 1
	} else {
		itheta = (2*(qn+1) - isqrt32(sym(8*(ft-fm-1)+1))) >> 1
		fs = qn + 1 - itheta
		fl = ft - ((qn + 1 - itheta) * (qn + 2 - itheta) >> 1)
	}
	rc.update(sym(fl), sym(fl+fs), sym(ft))
	return itheta
}

// isqrt32 is the integer square root, rounded down.
func isqrt32(v uint32) int {
	g := uint32(0)
	shift := (ilog(v) - 1) >> 1
	b := uint32(1) << shift
	for ; shift >= 0; shift-- {
		t := (g<<1 + b) << shift
		if t <= v {
			g += b
			v -= t
		}
		b >>= 1
	}
	return int(g)
}

// stereoBand decodes a joint stereo band: the angle between mid and
// side, then both as mono bands, merged back to left and right.
func (q *bandDecoder) stereoBand(x, y []float32, b, blocks int, lowband, lowbandOut, scratch []float32, fill uint) uint {
	n := len(x)
	if n == 1 {
		return q.n1(x, y, lowbandOut)
	}
	origFill := fill
	t := q.theta(n, &b, blocks, blocks, q.lm, true, &fill)
	mid, side := float32(t.imid)/32768, float32(t.iside)/32768
	var cm uint
	if n == 2 {
		sbits := 0
		if t.itheta != 0 && t.itheta != 16384 {
			sbits = 1 << bitRes
		}
		mbits := b - sbits
		q.remaining -= t.qalloc + sbits
		x2, y2 := x, y
		if t.itheta > 8192 {
			x2, y2 = y, x
		}
		sign := float32(1)
		if sbits != 0 && q.rc.bits(1) == 1 {
			sign = -1
		}
		cm = q.band(x2, mbits, blocks, lowband, lowbandOut, 1, scratch, origFill)
		y2[0] = -sign * x2[1]
		y2[1] = sign * x2[0]
		x[0], x[1] = float32(mid*x[0]), float32(mid*x[1])
		y[0], y[1] = float32(side*y[0]), float32(side*y[1])
		x[0], y[0] = x[0]-y[0], x[0]+y[0]
		x[1], y[1] = x[1]-y[1], x[1]+y[1]
	} else {
		mbits := max(0, min(b, (b-t.delta)/2))
		sbits := b - mbits
		q.remaining -= t.qalloc
		rebalance := q.remaining
		if mbits >= sbits {
			cm = q.band(x, mbits, blocks, lowband, lowbandOut, 1, scratch, fill)
			rebalance = mbits - (rebalance - q.remaining)
			if rebalance > 3<<bitRes && t.itheta != 0 {
				sbits += rebalance - 3<<bitRes
			}
			cm |= q.band(y, sbits, blocks, nil, nil, side, nil, fill>>blocks)
		} else {
			cm = q.band(y, sbits, blocks, nil, nil, side, nil, fill>>blocks)
			rebalance = sbits - (rebalance - q.remaining)
			if rebalance > 3<<bitRes && t.itheta != 16384 {
				mbits += rebalance - 3<<bitRes
			}
			cm |= q.band(x, mbits, blocks, lowband, lowbandOut, 1, scratch, fill)
		}
		stereoMerge(x, y, mid)
	}
	if t.inv {
		for j := range y {
			y[j] = -y[j]
		}
	}
	return cm
}

// stereoMerge turns the decoded mid (scaled by mid) and side back into
// left and right of unit norm.
func stereoMerge(x, y []float32, mid float32) {
	var xp, side float32
	for j := range x {
		xp += float32(y[j] * x[j])
		side += float32(y[j] * y[j])
	}
	xp = float32(xp * mid)
	mm := float32(mid * mid)
	el := mm + side - (xp + xp)
	er := mm + side + (xp + xp)
	if er < 6e-4 || el < 6e-4 {
		copy(y, x)
		return
	}
	lgain := 1 / float32(math.Sqrt(float64(el)))
	rgain := 1 / float32(math.Sqrt(float64(er)))
	for j := range x {
		l := float32(mid * x[j])
		r := y[j]
		x[j] = lgain * (l - r)
		y[j] = rgain * (l + r)
	}
}
