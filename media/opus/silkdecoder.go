package opus

import (
	"math"

	"github.com/stubbedev/lys-lib/intconv"
)

// SILK's frame geometry (silk/define.h).
const (
	silkMaxFsKHz          = 16
	silkMaxNbSubfr        = 4
	silkSubFrameMs        = 5
	silkMaxSubFrameLength = silkSubFrameMs * silkMaxFsKHz
	silkMaxFrameLength    = silkMaxSubFrameLength * silkMaxNbSubfr
	silkMaxLPCOrder       = 16
	silkMinLPCOrder       = 10
	silkLTPOrder          = 5
	silkMaxFrames         = 3
	silkShellFrame        = 16
	silkMaxPulses         = 16
	silkRateLevels        = 10
	silkLTPMemMs          = 20
	silkMaxLTPMem         = silkLTPMemMs * silkMaxFsKHz
	nlsfQuantMaxAmplitude = 4
	silkBWEAfterLoss      = 63570
	silkQuantLevelAdjust  = 80
	silkNLSFQuantLevelAdj = 102
	silkMaxStabilize      = 16
	silkMinQGainDB        = 2
	silkMaxQGainDB        = 88
	silkGainLevels        = 64
	silkMaxDeltaGain      = 36
	silkMinDeltaGain      = -4
	silkGainOffset        = (silkMinQGainDB*128)/6 + 16*128
	silkGainInvScale      = (65536 * (((silkMaxQGainDB - silkMinQGainDB) * 128) / 6)) / (silkGainLevels - 1)
)

// silkSignal is a frame's signal type.
type silkSignal int32

const (
	silkInactive silkSignal = iota
	silkUnvoiced
	silkVoiced
)

// silkCoding is how a frame's parameters depend on the frame before.
type silkCoding int

const (
	codeIndependently silkCoding = iota
	codeIndependentlyNoLTPScaling
	codeConditionally
)

// silkNLSFCodebook is silk_NLSF_CB_struct, decoder side.
type silkNLSFCodebook struct {
	order         int
	quantStepSize int32
	cb1           []uint8
	cb1Weights    []int16
	cb1ICDF       []uint8
	pred          []uint8
	ecSel         []uint8
	ecICDF        []uint8
	deltaMin      []int16
}

const silkNLSFVectors = 32

var (
	silkNLSFNBMB = silkNLSFCodebook{
		order: 10, quantStepSize: 11796,
		cb1: silkNLSFCB1NBMB[:], cb1Weights: silkNLSFCB1WghtNBMB[:], cb1ICDF: silkNLSFCB1ICDFNBMB[:],
		pred: silkNLSFPredNBMB[:], ecSel: silkNLSFCB2SelectNBMB[:], ecICDF: silkNLSFCB2ICDFNBMB[:],
		deltaMin: silkNLSFDeltaMinNBMB[:],
	}
	silkNLSFWB = silkNLSFCodebook{
		order: 16, quantStepSize: 9830,
		cb1: silkNLSFCB1WB[:], cb1Weights: silkNLSFCB1WghtWB[:], cb1ICDF: silkNLSFCB1ICDFWB[:],
		pred: silkNLSFPredWB[:], ecSel: silkNLSFCB2SelectWB[:], ecICDF: silkNLSFCB2ICDFWB[:],
		deltaMin: silkNLSFDeltaMinWB[:],
	}
	silkLTPGainICDFs = [3][]uint8{silkLTPGainICDF0[:], silkLTPGainICDF1[:], silkLTPGainICDF2[:]}
	silkLTPVQs       = [3][]int8{silkLTPGainVQ0[:], silkLTPGainVQ1[:], silkLTPGainVQ2[:]}
	silkShellTables  = [4]*[152]uint8{&silkShellCodeTable0, &silkShellCodeTable1, &silkShellCodeTable2, &silkShellCodeTable3}
)

// silkIndices is SideInfoIndices: one frame's quantisation indices.
type silkIndices struct {
	gains           [silkMaxNbSubfr]int32
	ltp             [silkMaxNbSubfr]int32
	nlsf            [silkMaxLPCOrder + 1]int32
	lagIndex        int32
	contour         int32
	signalType      silkSignal
	quantOffsetType int32
	nlsfInterp      int32
	perIndex        int32
	ltpScaleIndex   int32
	seed            int32
}

// silkControl is silk_decoder_control: one frame's dequantised
// parameters.
type silkControl struct {
	pitchL   [silkMaxNbSubfr]int
	gains    [silkMaxNbSubfr]int32
	predCoef [2][silkMaxLPCOrder]int16
	ltpCoef  [silkLTPOrder * silkMaxNbSubfr]int16
	ltpScale int32
}

// silkChannel is silk_decoder_state: one coded channel's history.
type silkChannel struct {
	prevGain             int32
	exc                  [silkMaxFrameLength]int32
	sLPC                 [silkMaxLPCOrder]int32
	outBuf               [silkMaxFrameLength + 2*silkMaxSubFrameLength]int16
	lagPrev              int
	lastGainIndex        int32
	fsKHz                int32
	nbSubfr              int
	frameLength          int
	subfrLength          int
	ltpMemLength         int
	lpcOrder             int
	prevNLSF             [silkMaxLPCOrder]int16
	firstFrameAfterReset bool
	pitchLagLowBitsICDF  []uint8
	pitchContourICDF     []uint8
	nFramesDecoded       int
	nFramesPerPacket     int
	ecPrevSignalType     silkSignal
	ecPrevLagIndex       int32
	vadFlags             [silkMaxFrames]bool
	lbrrFlag             bool
	lbrrFlags            [silkMaxFrames]bool
	resampler            silkResampler
	cb                   *silkNLSFCodebook
	indices              silkIndices
	cng                  silkCNG
	lossCnt              int
	prevSignalType       silkSignal
	plc                  silkPLC

	sLTP    [silkMaxLTPMem]int16
	sLTPQ15 [silkMaxLTPMem + silkMaxFrameLength]int32
	resQ14  [silkMaxSubFrameLength]int32
	sLPCQ14 [silkMaxSubFrameLength + silkMaxLPCOrder]int32
}

// reset is silk_init_decoder: a channel that has decoded nothing.
func (ch *silkChannel) reset() {
	*ch = silkChannel{firstFrameAfterReset: true, prevGain: 65536}
	ch.resetCNG()
	ch.resetPLC()
}

// setFs is silk_decoder_set_fs for an output rate of 48 kHz.
func (ch *silkChannel) setFs(fsKHz int32) {
	ch.subfrLength = silkSubFrameMs * int(fsKHz)
	frameLength := ch.nbSubfr * ch.subfrLength
	if ch.fsKHz != fsKHz {
		ch.resampler.init(fsKHz)
	}
	if ch.fsKHz == fsKHz && frameLength == ch.frameLength {
		return
	}
	switch {
	case fsKHz == 8 && ch.nbSubfr == silkMaxNbSubfr:
		ch.pitchContourICDF = silkPitchContourNBICDF[:]
	case fsKHz == 8:
		ch.pitchContourICDF = silkPitchContour10msNBICDF[:]
	case ch.nbSubfr == silkMaxNbSubfr:
		ch.pitchContourICDF = silkPitchContourICDF[:]
	default:
		ch.pitchContourICDF = silkPitchContour10msICDF[:]
	}
	if ch.fsKHz != fsKHz {
		ch.ltpMemLength = silkLTPMemMs * int(fsKHz)
		ch.lpcOrder, ch.cb = silkMinLPCOrder, &silkNLSFNBMB
		if fsKHz == 16 {
			ch.lpcOrder, ch.cb = silkMaxLPCOrder, &silkNLSFWB
		}
		switch fsKHz {
		case 16:
			ch.pitchLagLowBitsICDF = silkUniform8ICDF[:]
		case 12:
			ch.pitchLagLowBitsICDF = silkUniform6ICDF[:]
		default:
			ch.pitchLagLowBitsICDF = silkUniform4ICDF[:]
		}
		ch.firstFrameAfterReset = true
		ch.lagPrev = 100
		ch.lastGainIndex = 10
		ch.prevSignalType = silkInactive
		clear(ch.outBuf[:])
		clear(ch.sLPC[:])
	}
	ch.fsKHz = fsKHz
	ch.frameLength = frameLength
}

// decodeIndices is silk_decode_indices.
func (ch *silkChannel) decodeIndices(rc *rangeDecoder, frame int, lbrr bool, coding silkCoding) {
	ix := &ch.indices
	var t int32
	if lbrr || ch.vadFlags[frame] {
		t = rc.icdf32(silkTypeOffsetVADICDF[:], 8) + 2
	} else {
		t = rc.icdf32(silkTypeOffsetNoVADICDF[:], 8)
	}
	ix.signalType = silkSignal(t >> 1)
	ix.quantOffsetType = t & 1
	if coding == codeConditionally {
		ix.gains[0] = rc.icdf32(silkDeltaGainICDF[:], 8)
	} else {
		ix.gains[0] = rc.icdf32(silkGainICDF[ix.signalType*8:], 8) << 3
		ix.gains[0] += rc.icdf32(silkUniform8ICDF[:], 8)
	}
	for i := 1; i < ch.nbSubfr; i++ {
		ix.gains[i] = rc.icdf32(silkDeltaGainICDF[:], 8)
	}
	cb := ch.cb
	ix.nlsf[0] = rc.icdf32(cb.cb1ICDF[(ix.signalType>>1)*silkNLSFVectors:], 8)
	var ecIx [silkMaxLPCOrder]int32
	var pred [silkMaxLPCOrder]uint8
	cb.unpack(&ecIx, &pred, ix.nlsf[0])
	for i := range cb.order {
		v := rc.icdf32(cb.ecICDF[ecIx[i]:], 8)
		switch v {
		case 0:
			v -= rc.icdf32(silkNLSFExtICDF[:], 8)
		case 2 * nlsfQuantMaxAmplitude:
			v += rc.icdf32(silkNLSFExtICDF[:], 8)
		}
		ix.nlsf[i+1] = v - nlsfQuantMaxAmplitude
	}
	ix.nlsfInterp = 4
	if ch.nbSubfr == silkMaxNbSubfr {
		ix.nlsfInterp = rc.icdf32(silkNLSFInterpolationFactorICDF[:], 8)
	}
	if ix.signalType == silkVoiced {
		absolute := true
		if coding == codeConditionally && ch.ecPrevSignalType == silkVoiced {
			if delta := rc.icdf32(silkPitchDeltaICDF[:], 8); delta > 0 {
				ix.lagIndex = ch.ecPrevLagIndex + delta - 9
				absolute = false
			}
		}
		if absolute {
			ix.lagIndex = rc.icdf32(silkPitchLagICDF[:], 8) * (ch.fsKHz >> 1)
			ix.lagIndex += rc.icdf32(ch.pitchLagLowBitsICDF, 8)
		}
		ch.ecPrevLagIndex = ix.lagIndex
		ix.contour = rc.icdf32(ch.pitchContourICDF, 8)
		ix.perIndex = rc.icdf32(silkLTPPerIndexICDF[:], 8)
		for k := range ch.nbSubfr {
			ix.ltp[k] = rc.icdf32(silkLTPGainICDFs[ix.perIndex], 8)
		}
		ix.ltpScaleIndex = 0
		if coding == codeIndependently {
			ix.ltpScaleIndex = rc.icdf32(silkLTPScaleICDF[:], 8)
		}
	}
	ch.ecPrevSignalType = ix.signalType
	ix.seed = rc.icdf32(silkUniform4ICDF[:], 8)
}

// unpack is silk_NLSF_unpack: the entropy table offsets and predictor
// coefficients of first-stage vector cb1.
func (cb *silkNLSFCodebook) unpack(ecIx *[silkMaxLPCOrder]int32, pred *[silkMaxLPCOrder]uint8, cb1 int32) {
	sel := cb.ecSel[int(cb1)*cb.order/2:]
	for i := 0; i < cb.order; i += 2 {
		e := sel[i/2]
		ecIx[i] = int32((e>>1)&7) * (2*nlsfQuantMaxAmplitude + 1)
		pred[i] = cb.pred[i+int(e&1)*(cb.order-1)]
		ecIx[i+1] = int32((e>>5)&7) * (2*nlsfQuantMaxAmplitude + 1)
		pred[i+1] = cb.pred[i+int((e>>4)&1)*(cb.order-1)+1]
	}
}

// decodePulses is silk_decode_pulses: the frame's quantised
// excitation.
func decodePulses(rc *rangeDecoder, pulses []int32, signalType silkSignal, quantOffsetType int32, frameLength int) {
	rateLevel := rc.icdf32(silkRateLevelsICDF[(signalType>>1)*9:], 8)
	iter := frameLength >> 4
	if iter*silkShellFrame < frameLength {
		iter++
	}
	var sum, shifts [silkMaxFrameLength / silkShellFrame]int32
	for i := range iter {
		sum[i] = rc.icdf32(silkPulsesPerBlockICDF[rateLevel*18:], 8)
		for sum[i] == silkMaxPulses+1 {
			shifts[i]++
			off := (silkRateLevels-1)*18 + b2i(shifts[i] == 10)
			sum[i] = rc.icdf32(silkPulsesPerBlockICDF[off:], 8)
		}
	}
	for i := range iter {
		block := pulses[i*silkShellFrame : (i+1)*silkShellFrame]
		if sum[i] > 0 {
			shellDecode((*[silkShellFrame]int32)(block), rc, sum[i])
		} else {
			clear(block)
		}
	}
	for i := range iter {
		if shifts[i] == 0 {
			continue
		}
		block := pulses[i*silkShellFrame : (i+1)*silkShellFrame]
		for k, v := range block {
			for range shifts[i] {
				v = v<<1 + rc.icdf32(silkLSBICDF[:], 8)
			}
			block[k] = v
		}
		sum[i] |= shifts[i] << 5
	}
	signs := silkSignICDF[7*(quantOffsetType+int32(signalType)<<1):]
	blocks := (frameLength + silkShellFrame/2) >> 4
	var icdf [2]uint8
	for i := range blocks {
		p := sum[i]
		if p <= 0 {
			continue
		}
		icdf[0] = signs[min(p&0x1f, 6)]
		for j := i * silkShellFrame; j < (i+1)*silkShellFrame; j++ {
			if pulses[j] > 0 {
				pulses[j] *= rc.icdf32(icdf[:], 8)<<1 - 1
			}
		}
	}
}

// shellDecode is silk_shell_decoder: the split of p pulses over a
// block of 16.
func shellDecode(out *[silkShellFrame]int32, rc *rangeDecoder, p int32) {
	var p3 [2]int32
	var p2 [4]int32
	var p1 [8]int32
	split := func(a, b *int32, p int32, table int) {
		if p <= 0 {
			*a, *b = 0, 0
			return
		}
		*a = rc.icdf32(silkShellTables[table][silkShellCodeTableOffsets[p]:], 8)
		*b = p - *a
	}
	split(&p3[0], &p3[1], p, 3)
	for i := range 2 {
		split(&p2[2*i], &p2[2*i+1], p3[i], 2)
		for j := 2 * i; j < 2*i+2; j++ {
			split(&p1[2*j], &p1[2*j+1], p2[j], 1)
			for k := 2 * j; k < 2*j+2; k++ {
				split(&out[2*k], &out[2*k+1], p1[k], 0)
			}
		}
	}
}

// decodeParameters is silk_decode_parameters.
func (ch *silkChannel) decodeParameters(ctl *silkControl, coding silkCoding) {
	ix := &ch.indices
	ch.dequantGains(ctl, coding == codeConditionally)
	var nlsf, nlsf0 [silkMaxLPCOrder]int16
	ch.cb.decode(&nlsf, &ix.nlsf)
	order := ch.lpcOrder
	nlsf2A(ctl.predCoef[1][:order], nlsf[:order])
	if ch.firstFrameAfterReset {
		ix.nlsfInterp = 4
	}
	if ix.nlsfInterp < 4 {
		for i := range order {
			nlsf0[i] = wrap16(int32(ch.prevNLSF[i]) + (ix.nlsfInterp*(int32(nlsf[i])-int32(ch.prevNLSF[i])))>>2)
		}
		nlsf2A(ctl.predCoef[0][:order], nlsf0[:order])
	} else {
		ctl.predCoef[0] = ctl.predCoef[1]
	}
	ch.prevNLSF = nlsf
	if ch.lossCnt != 0 {
		bwExpand(ctl.predCoef[0][:order], silkBWEAfterLoss)
		bwExpand(ctl.predCoef[1][:order], silkBWEAfterLoss)
	}
	if ix.signalType != silkVoiced {
		ctl.pitchL = [silkMaxNbSubfr]int{}
		ctl.ltpCoef = [silkLTPOrder * silkMaxNbSubfr]int16{}
		ix.perIndex = 0
		ctl.ltpScale = 0
		return
	}
	decodePitch(ix.lagIndex, ix.contour, &ctl.pitchL, ch.fsKHz, ch.nbSubfr)
	cbk := silkLTPVQs[ix.perIndex]
	for k := range ch.nbSubfr {
		row := int(ix.ltp[k]) * silkLTPOrder
		for i := range silkLTPOrder {
			ctl.ltpCoef[k*silkLTPOrder+i] = int16(cbk[row+i]) << 7
		}
	}
	ctl.ltpScale = int32(silkLTPScales[ix.ltpScaleIndex])
}

// dequantGains is silk_gains_dequant.
func (ch *silkChannel) dequantGains(ctl *silkControl, conditional bool) {
	prev := ch.lastGainIndex
	for k := range ch.nbSubfr {
		ind := ch.indices.gains[k]
		if k == 0 && !conditional {
			prev = max(ind, prev-16)
		} else {
			ind += silkMinDeltaGain
			threshold := 2*silkMaxDeltaGain - silkGainLevels + prev
			if ind > threshold {
				prev += ind<<1 - threshold
			} else {
				prev += ind
			}
		}
		prev = max(0, min(silkGainLevels-1, prev))
		ctl.gains[k] = log2lin(min(smulwb(silkGainInvScale, prev)+silkGainOffset, 3967))
	}
	ch.lastGainIndex = prev
}

// decodePitch is silk_decode_pitch: each subframe's pitch lag.
func decodePitch(lagIndex, contour int32, lags *[silkMaxNbSubfr]int, fsKHz int32, nbSubfr int) {
	var cb []int8
	var size int
	switch {
	case fsKHz == 8 && nbSubfr == silkMaxNbSubfr:
		cb, size = silkCBLagsStage2[:], 11
	case fsKHz == 8:
		cb, size = silkCBLagsStage210ms[:], 3
	case nbSubfr == silkMaxNbSubfr:
		cb, size = silkCBLagsStage3[:], 34
	default:
		cb, size = silkCBLagsStage310ms[:], 12
	}
	minLag, maxLag := 2*int(fsKHz), 18*int(fsKHz)
	lag := minLag + int(lagIndex)
	for k := range nbSubfr {
		lags[k] = max(minLag, min(maxLag, lag+int(cb[k*size+int(contour)])))
	}
}

// decode is silk_NLSF_decode: the stabilised NLSF vector of a frame's
// codebook path.
func (cb *silkNLSFCodebook) decode(nlsf *[silkMaxLPCOrder]int16, indices *[silkMaxLPCOrder + 1]int32) {
	var ecIx [silkMaxLPCOrder]int32
	var pred [silkMaxLPCOrder]uint8
	cb.unpack(&ecIx, &pred, indices[0])
	var res [silkMaxLPCOrder]int16
	out := int32(0)
	for i := cb.order - 1; i >= 0; i-- {
		p := smulbb(out, int32(pred[i])) >> 8
		out = indices[i+1] << 10
		switch {
		case out > 0:
			out -= silkNLSFQuantLevelAdj
		case out < 0:
			out += silkNLSFQuantLevelAdj
		}
		out = smlawb(p, out, cb.quantStepSize)
		res[i] = wrap16(out)
	}
	base := int(indices[0]) * cb.order
	for i := range cb.order {
		v := (int32(res[i])<<14)/int32(cb.cb1Weights[base+i]) + int32(cb.cb1[base+i])<<7
		nlsf[i] = int16(max(0, min(32767, v)))
	}
	nlsfStabilize(nlsf[:cb.order], cb.deltaMin)
}

// nlsfStabilize is silk_NLSF_stabilize: it spaces nlsf at least
// deltaMin apart.
func nlsfStabilize(nlsf []int16, deltaMin []int16) {
	l := len(nlsf)
	for range 20 {
		minDiff := int32(nlsf[0]) - int32(deltaMin[0])
		at := 0
		for i := 1; i < l; i++ {
			if d := int32(nlsf[i]) - (int32(nlsf[i-1]) + int32(deltaMin[i])); d < minDiff {
				minDiff, at = d, i
			}
		}
		if d := 1<<15 - (int32(nlsf[l-1]) + int32(deltaMin[l])); d < minDiff {
			minDiff, at = d, l
		}
		if minDiff >= 0 {
			return
		}
		switch at {
		case 0:
			nlsf[0] = deltaMin[0]
		case l:
			nlsf[l-1] = wrap16(1<<15 - int32(deltaMin[l]))
		default:
			minCenter := int32(0)
			for k := range at {
				minCenter += int32(deltaMin[k])
			}
			minCenter += int32(deltaMin[at] >> 1)
			maxCenter := int32(1 << 15)
			for k := l; k > at; k-- {
				maxCenter -= int32(deltaMin[k])
			}
			maxCenter -= int32(deltaMin[at] >> 1)
			center := wrap16(limit32(rshiftRound(int32(nlsf[at-1])+int32(nlsf[at]), 1), minCenter, maxCenter))
			nlsf[at-1] = center - deltaMin[at]>>1
			nlsf[at] = nlsf[at-1] + deltaMin[at]
		}
	}
	for i := 1; i < l; i++ {
		v := nlsf[i]
		j := i - 1
		for ; j >= 0 && v < nlsf[j]; j-- {
			nlsf[j+1] = nlsf[j]
		}
		nlsf[j+1] = v
	}
	nlsf[0] = max(nlsf[0], deltaMin[0])
	for i := 1; i < l; i++ {
		nlsf[i] = max(nlsf[i], sat16(int32(nlsf[i-1])+int32(deltaMin[i])))
	}
	nlsf[l-1] = min(nlsf[l-1], wrap16(1<<15-int32(deltaMin[l])))
	for i := l - 2; i >= 0; i-- {
		nlsf[i] = min(nlsf[i], nlsf[i+1]-deltaMin[i+1])
	}
}

var (
	nlsfOrdering16 = [16]uint8{0, 15, 8, 7, 4, 11, 12, 3, 2, 13, 10, 5, 6, 9, 14, 1}
	nlsfOrdering10 = [10]uint8{0, 9, 6, 3, 4, 5, 8, 1, 2, 7}
)

// nlsf2A is silk_NLSF2A: the stable Q12 LPC filter of NLSF vector
// nlsf, both of length 10 or 16.
func nlsf2A(a []int16, nlsf []int16) {
	const qa = 16
	d := len(nlsf)
	ordering := nlsfOrdering10[:]
	if d == 16 {
		ordering = nlsfOrdering16[:]
	}
	var cosLSF [silkMaxLPCOrder]int32
	for k, f := range nlsf {
		fInt := int32(f) >> 8
		fFrac := int32(f) - fInt<<8
		c := int32(silkLSFCosTab[fInt])
		delta := int32(silkLSFCosTab[fInt+1]) - c
		cosLSF[ordering[k]] = rshiftRound(c<<8+delta*fFrac, 20-qa)
	}
	dd := d >> 1
	var p, q [silkMaxLPCOrder/2 + 1]int32
	findPoly := func(out []int32, c []int32) {
		out[0] = 1 << qa
		out[1] = -c[0]
		for k := 1; k < dd; k++ {
			f := c[2*k]
			out[k+1] = out[k-1]<<1 - wrap32(rshiftRound64(int64(f)*int64(out[k]), qa))
			for n := k; n > 1; n-- {
				out[n] += out[n-2] - wrap32(rshiftRound64(int64(f)*int64(out[n-1]), qa))
			}
			out[1] -= f
		}
	}
	findPoly(p[:], cosLSF[0:])
	findPoly(q[:], cosLSF[1:])
	var a32 [silkMaxLPCOrder]int32
	for k := range dd {
		pt := p[k+1] + p[k]
		qt := q[k+1] - q[k]
		a32[k] = -qt - pt
		a32[d-k-1] = qt - pt
	}
	lpcFit(a, a32[:d], 12, qa+1)
	for i := 0; lpcInversePredGain(a) == 0 && i < silkMaxStabilize; i++ {
		bwExpand32(a32[:d], 65536-int32(2)<<i)
		for k := range d {
			a[k] = wrap16(rshiftRound(a32[k], qa+1-12))
		}
	}
}

// lpcFit is silk_LPC_fit: aIn (Q qin) bandwidth-expanded until it fits
// int16, into aOut (Q qout).
func lpcFit(aOut []int16, aIn []int32, qout, qin uint) {
	i := 0
	for ; i < 10; i++ {
		maxAbs, idx := int32(0), 0
		for k, v := range aIn {
			if a := abs32i(v); a > maxAbs {
				maxAbs, idx = a, k
			}
		}
		maxAbs = rshiftRound(maxAbs, qin-qout)
		if maxAbs <= math.MaxInt16 {
			break
		}
		maxAbs = min(maxAbs, 163838)
		chirp := int32(65470) - ((maxAbs-math.MaxInt16)<<14)/((maxAbs*int32(idx+1))>>2)
		bwExpand32(aIn, chirp)
	}
	if i == 10 {
		for k, v := range aIn {
			aOut[k] = sat16(rshiftRound(v, qin-qout))
			aIn[k] = int32(aOut[k]) << (qin - qout)
		}
		return
	}
	for k, v := range aIn {
		aOut[k] = wrap16(rshiftRound(v, qin-qout))
	}
}

// lpcInversePredGain is silk_LPC_inverse_pred_gain: the inverse
// prediction gain of Q12 filter a in Q30, zero if it is unstable.
func lpcInversePredGain(a []int16) int32 {
	const qa = 24
	const aLimit = 16773022
	const minInvGain = 107374
	var aQA [silkMaxLPCOrder]int32
	dc := int32(0)
	for k, v := range a {
		dc += int32(v)
		aQA[k] = int32(v) << (qa - 12)
	}
	if dc >= 4096 {
		return 0
	}
	order := len(a)
	invGain := int32(1 << 30)
	k := order - 1
	for ; k > 0; k-- {
		if aQA[k] > aLimit || aQA[k] < -aLimit {
			return 0
		}
		rc := -(aQA[k] << (31 - qa))
		mult1 := 1<<30 - smmul(rc, rc)
		invGain = smmul(invGain, mult1) << 2
		if invGain < minInvGain {
			return 0
		}
		mult2Q := 32 - clz32(abs32i(mult1))
		mult2 := inverse32VarQ(mult1, mult2Q+30)
		for n := range (k + 1) >> 1 {
			t1, t2 := aQA[n], aQA[k-n-1]
			v := rshiftRound64(int64(subSat32(t1, wrap32(rshiftRound64(int64(t2)*int64(rc), 31))))*int64(mult2), mult2Q)
			if v > math.MaxInt32 || v < math.MinInt32 {
				return 0
			}
			aQA[n] = wrap32(v)
			v = rshiftRound64(int64(subSat32(t2, wrap32(rshiftRound64(int64(t1)*int64(rc), 31))))*int64(mult2), mult2Q)
			if v > math.MaxInt32 || v < math.MinInt32 {
				return 0
			}
			aQA[k-n-1] = wrap32(v)
		}
	}
	if aQA[0] > aLimit || aQA[0] < -aLimit {
		return 0
	}
	rc := -(aQA[0] << (31 - qa))
	mult1 := 1<<30 - smmul(rc, rc)
	invGain = smmul(invGain, mult1) << 2
	if invGain < minInvGain {
		return 0
	}
	return invGain
}

// bwExpand is silk_bwexpander: ar's coefficients scaled by powers of
// chirp (Q16).
func bwExpand(ar []int16, chirp int32) {
	step := chirp - 65536
	last := len(ar) - 1
	for i := range last {
		ar[i] = wrap16(rshiftRound(chirp*int32(ar[i]), 16))
		chirp += rshiftRound(chirp*step, 16)
	}
	ar[last] = wrap16(rshiftRound(chirp*int32(ar[last]), 16))
}

// bwExpand32 is silk_bwexpander_32.
func bwExpand32(ar []int32, chirp int32) {
	step := chirp - 65536
	last := len(ar) - 1
	for i := range last {
		ar[i] = smulww(chirp, ar[i])
		chirp += rshiftRound(chirp*step, 16)
	}
	ar[last] = smulww(chirp, ar[last])
}

// lpcAnalysisFilter is silk_LPC_analysis_filter: out[d:n] the residual
// of in[:n] under Q12 predictor b, out[:d] zero.
func lpcAnalysisFilter(out, in []int16, b []int16, n int) {
	d := len(b)
	for ix := d; ix < n; ix++ {
		acc := int32(0)
		for j, c := range b {
			acc += int32(in[ix-1-j]) * int32(c)
		}
		out[ix] = sat16(rshiftRound(int32(in[ix])<<12-acc, 12))
	}
	clear(out[:d])
}

// lpcSynthesis runs the Q12 short-term predictor a over n samples of
// the Q14 state s (whose first silkMaxLPCOrder entries are history),
// adding each prediction to s, which is silk_decode_core's, PLC's and
// CNG's inner loop.
func lpcSynthesis(s []int32, a []int16, n int) {
	half := intconv.Must[int32](len(a) >> 1)
	for i := range n {
		pred := half
		hist := s[i : i+silkMaxLPCOrder]
		for j, c := range a {
			pred = smlawb(pred, hist[silkMaxLPCOrder-1-j], int32(c))
		}
		s[silkMaxLPCOrder+i] = addSat32(s[silkMaxLPCOrder+i], lshiftSat32(pred, 4))
	}
}

// decodeCore is silk_decode_core: the frame's excitation through the
// long- and short-term predictors into xq.
func (ch *silkChannel) decodeCore(ctl *silkControl, xq []int16, pulses []int32) {
	ix := &ch.indices
	offset := int32(silkQuantizationOffsets[int(ix.signalType>>1)*2+int(ix.quantOffsetType)])
	interp := ix.nlsfInterp < 4
	seed := ix.seed
	for i := range ch.frameLength {
		seed = silkRand(seed)
		e := pulses[i] << 14
		switch {
		case e > 0:
			e -= silkQuantLevelAdjust << 4
		case e < 0:
			e += silkQuantLevelAdjust << 4
		}
		e += offset << 4
		if seed < 0 {
			e = -e
		}
		ch.exc[i] = e
		seed += pulses[i]
	}
	sLPC := ch.sLPCQ14[:ch.subfrLength+silkMaxLPCOrder]
	copy(sLPC, ch.sLPC[:])
	order := ch.lpcOrder
	ltpMem := ch.ltpMemLength
	sLTP := ch.sLTP[:]
	sLTPQ15 := ch.sLTPQ15[:]
	bufIdx := ltpMem
	lag := 0
	for k := range ch.nbSubfr {
		var a [silkMaxLPCOrder]int16
		copy(a[:order], ctl.predCoef[k>>1][:order])
		b := ctl.ltpCoef[k*silkLTPOrder : (k+1)*silkLTPOrder]
		signalType := ix.signalType
		gain := ctl.gains[k]
		gainQ10 := gain >> 6
		invGain := inverse32VarQ(gain, 47)
		gainAdj := int32(1 << 16)
		if gain != ch.prevGain {
			gainAdj = div32VarQ(ch.prevGain, gain, 16)
			for i := range silkMaxLPCOrder {
				sLPC[i] = smulww(gainAdj, sLPC[i])
			}
		}
		ch.prevGain = gain
		if ch.lossCnt != 0 && ch.prevSignalType == silkVoiced && ix.signalType != silkVoiced && k < silkMaxNbSubfr/2 {
			clear(b)
			b[silkLTPOrder/2] = 4096
			signalType = silkVoiced
			ctl.pitchL[k] = ch.lagPrev
		}
		exc := ch.exc[k*ch.subfrLength : (k+1)*ch.subfrLength]
		res := exc
		if signalType == silkVoiced {
			lag = ctl.pitchL[k]
			if k == 0 || (k == 2 && interp) {
				start := ltpMem - lag - order - silkLTPOrder/2
				if k == 2 {
					copy(ch.outBuf[ltpMem:], xq[:2*ch.subfrLength])
				}
				lpcAnalysisFilter(sLTP[start:], ch.outBuf[start+k*ch.subfrLength:], a[:order], ltpMem-start)
				if k == 0 {
					invGain = smulwb(invGain, ctl.ltpScale) << 2
				}
				for i := range lag + silkLTPOrder/2 {
					sLTPQ15[bufIdx-i-1] = smulwb(invGain, int32(sLTP[ltpMem-i-1]))
				}
			} else if gainAdj != 1<<16 {
				for i := range lag + silkLTPOrder/2 {
					sLTPQ15[bufIdx-i-1] = smulww(gainAdj, sLTPQ15[bufIdx-i-1])
				}
			}
			res = ch.resQ14[:ch.subfrLength]
			p := bufIdx - lag + silkLTPOrder/2
			for i := range res {
				pred := int32(2)
				pred = smlawb(pred, sLTPQ15[p], int32(b[0]))
				pred = smlawb(pred, sLTPQ15[p-1], int32(b[1]))
				pred = smlawb(pred, sLTPQ15[p-2], int32(b[2]))
				pred = smlawb(pred, sLTPQ15[p-3], int32(b[3]))
				pred = smlawb(pred, sLTPQ15[p-4], int32(b[4]))
				p++
				res[i] = exc[i] + pred<<1
				sLTPQ15[bufIdx] = res[i] << 1
				bufIdx++
			}
		}
		copy(sLPC[silkMaxLPCOrder:], res)
		lpcSynthesis(sLPC, a[:order], ch.subfrLength)
		out := xq[k*ch.subfrLength : (k+1)*ch.subfrLength]
		for i := range out {
			out[i] = sat16(rshiftRound(smulww(sLPC[silkMaxLPCOrder+i], gainQ10), 8))
		}
		copy(sLPC, sLPC[ch.subfrLength:ch.subfrLength+silkMaxLPCOrder])
	}
	copy(ch.sLPC[:], sLPC[:silkMaxLPCOrder])
}

// decodeFrame is silk_decode_frame: one frame of xq, decoded or, when
// lost, concealed.
func (ch *silkChannel) decodeFrame(rc *rangeDecoder, xq []int16, pulses []int32, lost bool, coding silkCoding) {
	var ctl silkControl
	n := ch.frameLength
	xq = xq[:n]
	if !lost {
		ch.decodeIndices(rc, ch.nFramesDecoded, false, coding)
		decodePulses(rc, pulses, ch.indices.signalType, ch.indices.quantOffsetType, n)
		ch.decodeParameters(&ctl, coding)
		ch.decodeCore(&ctl, xq, pulses)
		ch.pushOutput(xq)
		ch.updatePLC(&ctl)
		ch.lossCnt = 0
		ch.prevSignalType = ch.indices.signalType
		ch.firstFrameAfterReset = false
	} else {
		ch.conceal(&ctl, xq)
		ch.pushOutput(xq)
	}
	ch.comfortNoise(&ctl, xq)
	ch.glueFrames(xq)
	ch.lagPrev = ctl.pitchL[ch.nbSubfr-1]
}

// pushOutput appends xq to the LTP history.
func (ch *silkChannel) pushOutput(xq []int16) {
	mv := ch.ltpMemLength - ch.frameLength
	copy(ch.outBuf[:mv], ch.outBuf[ch.frameLength:ch.frameLength+mv])
	copy(ch.outBuf[mv:], xq)
}
