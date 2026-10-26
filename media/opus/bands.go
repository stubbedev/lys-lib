package opus

import "math"

// bandCoder codes the normalised band shapes of one frame
// (quant_all_bands, quant_band, quant_partition and quant_band_stereo,
// encoder side). The encoder never resynthesises, so the decoder's
// folding, stereo merging and collapse masks have no counterpart here.
type bandCoder struct {
	rc              *rangeEncoder
	pvq             *pvq
	amp             *[2][nbBands]float32
	spread          int
	intensity       int
	blocks          int
	tfChange        int
	avoidSplitNoise bool
	remaining       int
	scratch         [FrameSize]float32
}

// quantAll codes every band of x (and y in stereo) given the
// allocation and each band's time-frequency change; totalBits is the
// frame size in 1/8 bits.
func (q *bandCoder) quantAll(x, y []float32, a *allocation, tfChange *[nbBands]int, totalBits int) {
	balance := a.balance
	dual := a.dualStereo
	q.avoidSplitNoise = q.blocks > 1
	for i := range nbBands {
		lo, hi := bandStart(i), bandStart(i+1)
		tell := q.rc.tellFrac()
		if i != 0 {
			balance -= tell
		}
		q.remaining = totalBits - tell - 1
		b := 0
		if i <= a.codedBands-1 {
			curr := balance / min(3, a.codedBands-i)
			b = max(0, min(16383, min(q.remaining+1, a.pulses[i]+curr)))
		}
		q.tfChange = tfChange[i]
		if dual && i == q.intensity {
			dual = false
		}
		switch {
		case y == nil:
			q.band(i, x[lo:hi], b)
		case dual:
			q.band(i, x[lo:hi], b/2)
			q.band(i, y[lo:hi], b/2)
		default:
			q.stereoBand(i, x[lo:hi], y[lo:hi], b)
		}
		balance += a.pulses[i] + tell
		q.avoidSplitNoise = false
	}
}

// band codes one mono band with b eighth bits: it trades time for
// frequency resolution by the band's TF change, orders the short
// blocks' coefficients by time, and partitions the result.
func (q *bandCoder) band(i int, x []float32, b int) {
	n := len(x)
	if n == 1 {
		q.signBit(x)
		return
	}
	blocks := q.blocks
	longBlocks := blocks == 1
	nB := n / blocks
	tfChange := q.tfChange
	recombine := max(0, tfChange)
	for k := range recombine {
		haar1(x, n>>k, 1<<k)
	}
	blocks >>= recombine
	nB <<= recombine
	for nB&1 == 0 && tfChange < 0 {
		haar1(x, nB, blocks)
		blocks <<= 1
		nB >>= 1
		tfChange++
	}
	if blocks > 1 {
		deinterleaveHadamard(x, q.scratch[:n], nB>>recombine, blocks<<recombine, longBlocks)
	}
	q.partition(i, x, b, blocks, frameLM)
}

// partition codes a mono band of blocks short blocks, splitting it in
// halves while that beats one PVQ.
func (q *bandCoder) partition(i int, x []float32, b, blocks, lm int) {
	n := len(x)
	cache := cacheRow(i, lm)
	if lm == -1 || b <= cache[cache[0]]+12 || n <= 2 {
		q.pulses(i, x, b, lm, blocks)
		return
	}
	b0 := blocks
	n >>= 1
	y := x[n:]
	x = x[:n]
	lm--
	blocks = (blocks + 1) >> 1
	itheta, qalloc := q.splitTheta(i, x, y, b, b0, lm, false)
	b -= qalloc
	delta := thetaDelta(itheta, n)
	if b0 > 1 && itheta&0x3fff != 0 {
		if itheta > 8192 {
			delta -= delta >> (4 - lm)
		} else {
			delta = min(0, delta+(n<<bitRes>>(5-lm)))
		}
	}
	q.remaining -= qalloc
	q.halves(x, y, b, delta, itheta, func(half []float32, bits int) {
		q.partition(i, half, bits, blocks, lm)
	})
}

// stereoBand codes a joint stereo band: the angle between mid and side
// (or intensity stereo), then mid and side as mono bands.
func (q *bandCoder) stereoBand(i int, x, y []float32, b int) {
	n := len(x)
	if n == 1 {
		q.signBit(x)
		q.signBit(y)
		return
	}
	itheta, qalloc := q.splitTheta(i, x, y, b, q.blocks, frameLM, true)
	b -= qalloc
	if n == 2 {
		sbits := 0
		if itheta != 0 && itheta != 16384 {
			sbits = 1 << bitRes
		}
		mbits := b - sbits
		q.remaining -= qalloc + sbits
		x2, y2 := x, y
		if itheta > 8192 {
			x2, y2 = y, x
		}
		if sbits != 0 {
			q.rc.bits(uint32(b2i(x2[0]*y2[1]-x2[1]*y2[0] < 0)), 1)
		}
		q.band(i, x2, mbits)
		return
	}
	q.remaining -= qalloc
	q.halves(x, y, b, thetaDelta(itheta, n), itheta, func(half []float32, bits int) {
		q.band(i, half, bits)
	})
}

// halves splits b bits between the halves x and y of a split by delta,
// codes the larger share first and hands what it left unused (beyond
// three bits) to the other half unless the angle itheta silences it.
func (q *bandCoder) halves(x, y []float32, b, delta, itheta int, code func(half []float32, bits int)) {
	mbits := max(0, min(b, (b-delta)/2))
	sbits := b - mbits
	rebalance := q.remaining
	if mbits >= sbits {
		code(x, mbits)
		rebalance = mbits - (rebalance - q.remaining)
		if rebalance > 3<<bitRes && itheta != 0 {
			sbits += rebalance - 3<<bitRes
		}
		code(y, sbits)
		return
	}
	code(y, sbits)
	rebalance = sbits - (rebalance - q.remaining)
	if rebalance > 3<<bitRes && itheta != 16384 {
		mbits += rebalance - 3<<bitRes
	}
	code(x, mbits)
}

// splitTheta quantises and codes the angle between the halves of a
// band (or its mid and side in stereo, which it forms in x and y) and
// returns it in Q14 with the 1/8 bits it took. b0 is the band's block
// count before the split.
func (q *bandCoder) splitTheta(i int, x, y []float32, b, b0, lm int, stereo bool) (itheta, qalloc int) {
	n := len(x)
	pulseCap := logN[i] + lm<<bitRes
	offset := pulseCap >> 1
	if stereo && n == 2 {
		offset -= qthetaTwo
	} else {
		offset -= qthetaOffset
	}
	qn := computeQN(n, b, offset, pulseCap, stereo)
	if stereo && i >= q.intensity {
		qn = 1
	}
	itheta = stereoTheta(x, y, stereo)
	tell := q.rc.tellFrac()
	switch {
	case qn != 1:
		itheta = (itheta*qn + 8192) >> 14
		if !stereo && q.avoidSplitNoise && itheta > 0 && itheta < qn {
			unquantised := itheta * 16384 / qn
			delta := thetaDelta(unquantised, n)
			if delta > b {
				itheta = qn
			} else if delta < -b {
				itheta = 0
			}
		}
		q.codeTheta(itheta, qn, n, b0, stereo)
		itheta = itheta * 16384 / qn
		if stereo {
			if itheta == 0 {
				q.intensityStereo(i, x, y)
			} else {
				stereoSplit(x, y)
			}
		}
	case stereo:
		inv := itheta > 8192
		if inv {
			for j := range y {
				y[j] = -y[j]
			}
		}
		q.intensityStereo(i, x, y)
		if b > 2<<bitRes && q.remaining > 2<<bitRes {
			q.rc.bitLogp(inv, 2)
		}
		itheta = 0
	}
	return itheta, q.rc.tellFrac() - tell
}

// thetaDelta is how many more 1/8 bits the side than the mid of an
// n-coefficient split at angle itheta (Q14) deserves.
func thetaDelta(itheta, n int) int {
	switch itheta {
	case 0:
		return -16384
	case 16384:
		return 16384
	}
	imid := bitexactCos(itheta)
	iside := bitexactCos(16384 - itheta)
	return fracMul16((n-1)<<7, bitexactLog2Tan(iside, imid))
}

// signBit codes the sign of a one-coefficient band when a bit is left.
func (q *bandCoder) signBit(x []float32) {
	if q.remaining < 1<<bitRes {
		return
	}
	q.rc.bits(uint32(b2i(x[0] < 0)), 1)
	q.remaining -= 1 << bitRes
}

// pulses codes an unsplit band as the most pulses b affords without
// overrunning the frame.
func (q *bandCoder) pulses(i int, x []float32, b, lm, blocks int) {
	k := bits2pulses(i, lm, b)
	cost := pulses2bits(i, lm, k)
	q.remaining -= cost
	for q.remaining < 0 && k > 0 {
		q.remaining += cost
		k--
		cost = pulses2bits(i, lm, k)
		q.remaining -= cost
	}
	if k != 0 {
		q.pvq.quant(q.rc, x, getPulses(k), q.spread, blocks)
	}
}

// codeTheta codes the quantised split angle: a step PDF for stereo,
// uniform for two-coefficient stereo and splits of short blocks,
// triangular for other mono splits.
func (q *bandCoder) codeTheta(itheta, qn, n, b0 int, stereo bool) {
	switch {
	case stereo && n > 2:
		const p0 = 3
		x0 := qn / 2
		ft := p0*(x0+1) + x0
		if itheta <= x0 {
			q.rc.encode(uint32(p0*itheta), uint32(p0*(itheta+1)), uint32(ft))
		} else {
			fl := (itheta - 1 - x0) + (x0+1)*p0
			q.rc.encode(uint32(fl), uint32(fl+1), uint32(ft))
		}
	case stereo || b0 > 1:
		q.rc.uint(uint32(itheta), uint32(qn+1))
	default:
		ft := ((qn >> 1) + 1) * ((qn >> 1) + 1)
		fs := qn + 1 - itheta
		fl := ft - ((qn + 1 - itheta) * (qn + 2 - itheta) >> 1)
		if itheta <= qn>>1 {
			fs = itheta + 1
			fl = itheta * (itheta + 1) >> 1
		}
		q.rc.encode(uint32(fl), uint32(fl+fs), uint32(ft))
	}
}

// haar1 is one level of Haar transform between coefficient pairs n0/2
// apart in each of stride interleaved blocks.
func haar1(x []float32, n0, stride int) {
	const r = float32(0.70710678)
	n0 >>= 1
	for i := range stride {
		for j := range n0 {
			a, b := r*x[stride*2*j+i], r*x[stride*(2*j+1)+i]
			x[stride*2*j+i] = a + b
			x[stride*(2*j+1)+i] = a - b
		}
	}
}

// orderyTable maps natural Hadamard order to sequency order (with DC
// last) for 2, 4, 8 and 16 blocks.
var orderyTable = [...]int{
	1, 0,
	3, 0, 2, 1,
	7, 0, 4, 3, 6, 1, 5, 2,
	15, 0, 8, 7, 12, 3, 11, 4, 14, 1, 9, 6, 13, 2, 10, 5,
}

// deinterleaveHadamard regroups x from stride interleaved blocks of n0
// coefficients into consecutive blocks, in sequency order when the
// blocks came from Haar steps on a long block.
func deinterleaveHadamard(x, tmp []float32, n0, stride int, hadamard bool) {
	for i := range stride {
		dst := i
		if hadamard {
			dst = orderyTable[stride-2+i]
		}
		for j := range n0 {
			tmp[dst*n0+j] = x[j*stride+i]
		}
	}
	copy(x, tmp[:n0*stride])
}

// intensityStereo folds y into x by the bands' amplitudes; the side is
// not coded.
func (q *bandCoder) intensityStereo(i int, x, y []float32) {
	left, right := q.amp[0][i], q.amp[1][i]
	norm := 1e-15 + float32(math.Sqrt(float64(1e-15+left*left+right*right)))
	a1, a2 := left/norm, right/norm
	for j := range x {
		x[j] = a1*x[j] + a2*y[j]
	}
}

// stereoSplit turns left/right into mid/side.
func stereoSplit(x, y []float32) {
	const r = float32(0.70710678)
	for j := range x {
		l, rr := r*x[j], r*y[j]
		x[j] = l + rr
		y[j] = rr - l
	}
}

// stereoTheta is the angle between the mid and side (stereo) or the
// two halves (a split), in Q14 quarter turns.
func stereoTheta(x, y []float32, stereo bool) int {
	emid, eside := float32(1e-15), float32(1e-15)
	for j := range x {
		m, s := x[j], y[j]
		if stereo {
			m, s = x[j]+y[j], x[j]-y[j]
		}
		emid += m * m
		eside += s * s
	}
	mid := math.Sqrt(float64(emid))
	side := math.Sqrt(float64(eside))
	return int(math.Floor(float64(0.5 + 16384*0.63662*float32(math.Atan2(side, mid)))))
}

// exp2Table8 is 2^(i/8) in Q14.
var exp2Table8 = [8]int{16384, 17866, 19483, 21247, 23170, 25267, 27554, 30048}

// computeQN is the number of steps the split angle is quantised to.
func computeQN(n, b, offset, pulseCap int, stereo bool) int {
	n2 := 2*n - 1
	if stereo && n == 2 {
		n2--
	}
	qb := min(b-pulseCap-(4<<bitRes), (b+n2*offset)/n2)
	qb = min(8<<bitRes, qb)
	if qb < 1<<bitRes>>1 {
		return 1
	}
	qn := exp2Table8[qb&7] >> (14 - qb>>bitRes)
	return (qn + 1) >> 1 << 1
}

// fracMul16 is the reference's Q15 product of two 16-bit values.
func fracMul16(a, b int) int {
	return (16384 + int(int32(int16(a))*int32(int16(b)))) >> 15
}

// bitexactCos is the integer cosine both ends compute bit-exactly, as
// it steers the allocation.
func bitexactCos(x int) int {
	x2 := (4096 + x*x) >> 13
	x2 = (32767 - x2) + fracMul16(x2, -7651+fracMul16(x2, 8277+fracMul16(-626, x2)))
	return 1 + x2
}

// bitexactLog2Tan is log2(isin/icos) in Q11, bit-exactly.
func bitexactLog2Tan(isin, icos int) int {
	lc := ilog(uint32(icos))
	ls := ilog(uint32(isin))
	icos <<= 15 - lc
	isin <<= 15 - ls
	return (ls-lc)*(1<<11) + fracMul16(isin, fracMul16(isin, -2597)+7932) - fracMul16(icos, fracMul16(icos, -2597)+7932)
}
