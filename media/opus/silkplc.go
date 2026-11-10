package opus

import (
	"math"

	"github.com/stubbedev/lys-lib/intconv"
)

// SILK's loss concealment and comfort noise (silk/PLC.c, PLC.h and
// CNG.c).
const (
	plcBWECoef           = 64881
	plcPitchGainStartMin = 11469
	plcPitchGainStartMax = 15565
	plcMaxPitchLagMs     = 18
	plcRandBufSize       = 128
	plcLog2InvGainHigh   = 3
	plcLog2InvGainLow    = 8
	plcPitchDrift        = 655
	cngBufMaskMax        = 255
	cngGainSmooth        = 4634
	cngGainSmoothThresh  = 46396
	cngNLSFSmooth        = 16348
)

var (
	plcHarmAtt         = [2]int32{32440, 31130}
	plcRandAttVoiced   = [2]int32{31130, 26214}
	plcRandAttUnvoiced = [2]int32{32440, 29491}
)

// silkPLC is silk_PLC_struct: what concealment extrapolates from.
type silkPLC struct {
	pitchLQ8        int32
	ltpCoef         [silkLTPOrder]int16
	prevLPC         [silkMaxLPCOrder]int16
	lastFrameLost   bool
	randSeed        int32
	randScale       int16
	concEnergy      int32
	concEnergyShift int
	prevLTPScale    int16
	prevGain        [2]int32
	fsKHz           int32
	nbSubfr         int
	subfrLength     int
}

// silkCNG is silk_CNG_struct: the comfort noise's spectrum, level and
// excitation.
type silkCNG struct {
	excBuf     [silkMaxFrameLength]int32
	smthNLSF   [silkMaxLPCOrder]int16
	synthState [silkMaxLPCOrder]int32
	smthGain   int32
	randSeed   int32
	fsKHz      int32
}

// resetPLC is silk_PLC_Reset.
func (ch *silkChannel) resetPLC() {
	p := &ch.plc
	p.pitchLQ8 = intconv.Must[int32](ch.frameLength) << 7
	p.prevGain = [2]int32{1 << 16, 1 << 16}
	p.subfrLength = 20
	p.nbSubfr = 2
}

// resetCNG is silk_CNG_Reset.
func (ch *silkChannel) resetCNG() {
	step := int32(math.MaxInt16) / intconv.Must[int32](ch.lpcOrder+1)
	acc := int32(0)
	for i := range ch.lpcOrder {
		acc += step
		ch.cng.smthNLSF[i] = wrap16(acc)
	}
	ch.cng.smthGain = 0
	ch.cng.randSeed = 3176576
}

// updatePLC is silk_PLC with a frame received: silk_PLC_update.
func (ch *silkChannel) updatePLC(ctl *silkControl) {
	ch.plcRate()
	p := &ch.plc
	ch.prevSignalType = ch.indices.signalType
	ltpGain := int32(0)
	if ch.indices.signalType == silkVoiced {
		for j := 0; j*ch.subfrLength < ctl.pitchL[ch.nbSubfr-1] && j != ch.nbSubfr; j++ {
			sub := ctl.ltpCoef[(ch.nbSubfr-1-j)*silkLTPOrder : (ch.nbSubfr-j)*silkLTPOrder]
			g := int32(0)
			for _, c := range sub {
				g += int32(c)
			}
			if g > ltpGain {
				ltpGain = g
				copy(p.ltpCoef[:], sub)
				p.pitchLQ8 = intconv.Must[int32](ctl.pitchL[ch.nbSubfr-1-j]) << 8
			}
		}
		p.ltpCoef = [silkLTPOrder]int16{}
		p.ltpCoef[silkLTPOrder/2] = wrap16(ltpGain)
		switch {
		case ltpGain < plcPitchGainStartMin:
			scale := (plcPitchGainStartMin << 10) / max(ltpGain, 1)
			for i := range p.ltpCoef {
				p.ltpCoef[i] = wrap16(smulbb(int32(p.ltpCoef[i]), scale) >> 10)
			}
		case ltpGain > plcPitchGainStartMax:
			scale := (plcPitchGainStartMax << 14) / max(ltpGain, 1)
			for i := range p.ltpCoef {
				p.ltpCoef[i] = wrap16(smulbb(int32(p.ltpCoef[i]), scale) >> 14)
			}
		}
	} else {
		p.pitchLQ8 = ch.fsKHz * 18 << 8
		p.ltpCoef = [silkLTPOrder]int16{}
	}
	copy(p.prevLPC[:ch.lpcOrder], ctl.predCoef[1][:ch.lpcOrder])
	p.prevLTPScale = wrap16(ctl.ltpScale)
	copy(p.prevGain[:], ctl.gains[ch.nbSubfr-2:ch.nbSubfr])
	p.subfrLength = ch.subfrLength
	p.nbSubfr = ch.nbSubfr
}

// plcRate resets the concealment state when the internal rate changed.
func (ch *silkChannel) plcRate() {
	if ch.fsKHz != ch.plc.fsKHz {
		ch.resetPLC()
		ch.plc.fsKHz = ch.fsKHz
	}
}

// conceal is silk_PLC with the frame lost: silk_PLC_conceal, which
// extrapolates the last frame's pitch and noise into frame.
func (ch *silkChannel) conceal(ctl *silkControl, frame []int16) {
	ch.plcRate()
	p := &ch.plc
	prevGainQ10 := [2]int32{p.prevGain[0] >> 6, p.prevGain[1] >> 6}
	if ch.firstFrameAfterReset {
		p.prevLPC = [silkMaxLPCOrder]int16{}
	}
	var excBuf [2 * silkMaxSubFrameLength]int16
	sl := ch.subfrLength
	for k := range 2 {
		for i := range sl {
			excBuf[k*sl+i] = sat16(smulww(ch.exc[i+(k+ch.nbSubfr-2)*sl], prevGainQ10[k]) >> 8)
		}
	}
	energy1, shift1 := sumSqrShift(excBuf[:sl])
	energy2, shift2 := sumSqrShift(excBuf[sl : 2*sl])
	var randBuf []int32
	if energy1>>shift2 < energy2>>shift1 {
		randBuf = ch.exc[max(0, (p.nbSubfr-1)*p.subfrLength-plcRandBufSize):]
	} else {
		randBuf = ch.exc[max(0, p.nbSubfr*p.subfrLength-plcRandBufSize):]
	}
	b := &p.ltpCoef
	randScale := int32(p.randScale)
	att := min(1, ch.lossCnt)
	harmGain := plcHarmAtt[att]
	randGain := plcRandAttUnvoiced[att]
	if ch.prevSignalType == silkVoiced {
		randGain = plcRandAttVoiced[att]
	}
	order := ch.lpcOrder
	bwExpand(p.prevLPC[:order], plcBWECoef)
	var a [silkMaxLPCOrder]int16
	copy(a[:order], p.prevLPC[:order])
	if ch.lossCnt == 0 {
		randScale = 1 << 14
		if ch.prevSignalType == silkVoiced {
			for _, c := range b {
				randScale -= int32(c)
			}
			randScale = int32(max(3277, wrap16(randScale)))
			randScale = smulbb(randScale, int32(p.prevLTPScale)) >> 14
		} else {
			invGain := lpcInversePredGain(p.prevLPC[:order])
			down := min(int32(1<<30)>>plcLog2InvGainHigh, invGain)
			down = max(int32(1<<30)>>plcLog2InvGainLow, down)
			down <<= plcLog2InvGainHigh
			randGain = smulwb(down, randGain) >> 14
		}
	}
	seed := p.randSeed
	lag := int(rshiftRound(p.pitchLQ8, 8))
	ltpMem := ch.ltpMemLength
	bufIdx := ltpMem
	idx := ltpMem - lag - order - silkLTPOrder/2
	sLTP := ch.sLTP[:]
	sLTPQ14 := ch.sLTPQ15[:ltpMem+ch.frameLength]
	lpcAnalysisFilter(sLTP[idx:], ch.outBuf[idx:], a[:order], ltpMem-idx)
	invGain := inverse32VarQ(p.prevGain[1], 46)
	invGain = min(invGain, math.MaxInt32>>1)
	for i := idx + order; i < ltpMem; i++ {
		sLTPQ14[i] = smulwb(invGain, int32(sLTP[i]))
	}
	for range ch.nbSubfr {
		pi := bufIdx - lag + silkLTPOrder/2
		for range ch.subfrLength {
			pred := int32(2)
			pred = smlawb(pred, sLTPQ14[pi], int32(b[0]))
			pred = smlawb(pred, sLTPQ14[pi-1], int32(b[1]))
			pred = smlawb(pred, sLTPQ14[pi-2], int32(b[2]))
			pred = smlawb(pred, sLTPQ14[pi-3], int32(b[3]))
			pred = smlawb(pred, sLTPQ14[pi-4], int32(b[4]))
			pi++
			seed = silkRand(seed)
			r := (seed >> 25) & (plcRandBufSize - 1)
			sLTPQ14[bufIdx] = smlawb(pred, randBuf[r], randScale) << 2
			bufIdx++
		}
		for j := range b {
			b[j] = wrap16(smulbb(harmGain, int32(b[j])) >> 15)
		}
		randScale = smulbb(randScale, randGain) >> 15
		p.pitchLQ8 = smlawb(p.pitchLQ8, p.pitchLQ8, plcPitchDrift)
		p.pitchLQ8 = min(p.pitchLQ8, plcMaxPitchLagMs*ch.fsKHz<<8)
		lag = int(rshiftRound(p.pitchLQ8, 8))
	}
	s := sLTPQ14[ltpMem-silkMaxLPCOrder:]
	copy(s, ch.sLPC[:])
	lpcSynthesis(s, a[:order], ch.frameLength)
	for i := range frame {
		frame[i] = sat16(rshiftRound(smulww(s[silkMaxLPCOrder+i], prevGainQ10[1]), 8))
	}
	copy(ch.sLPC[:], s[ch.frameLength:ch.frameLength+silkMaxLPCOrder])
	p.randSeed = seed
	p.randScale = wrap16(randScale)
	for i := range ctl.pitchL {
		ctl.pitchL[i] = lag
	}
	ch.lossCnt++
}

// glueFrames is silk_PLC_glue_frames: it fades the first frame after a
// loss in from the concealment's energy.
func (ch *silkChannel) glueFrames(frame []int16) {
	p := &ch.plc
	if ch.lossCnt != 0 {
		p.concEnergy, p.concEnergyShift = sumSqrShift(frame)
		p.lastFrameLost = true
		return
	}
	if p.lastFrameLost {
		energy, shift := sumSqrShift(frame)
		if shift > p.concEnergyShift {
			p.concEnergy >>= shift - p.concEnergyShift
		} else if shift < p.concEnergyShift {
			energy >>= p.concEnergyShift - shift
		}
		if energy > p.concEnergy {
			lz := clz32(p.concEnergy) - 1
			p.concEnergy <<= lz
			energy >>= max(24-lz, 0)
			frac := p.concEnergy / max(energy, 1)
			gain := sqrtApprox(frac) << 4
			slope := ((1 << 16) - gain) / intconv.Must[int32](len(frame))
			slope <<= 2
			for i, v := range frame {
				frame[i] = wrap16(smulwb(gain, int32(v)))
				gain += slope
				if gain > 1<<16 {
					break
				}
			}
		}
	}
	p.lastFrameLost = false
}

// comfortNoise is silk_CNG: it tracks the background of inactive
// frames and adds it to concealed ones.
func (ch *silkChannel) comfortNoise(ctl *silkControl, frame []int16) {
	c := &ch.cng
	if ch.fsKHz != c.fsKHz {
		ch.resetCNG()
		c.fsKHz = ch.fsKHz
	}
	order := ch.lpcOrder
	if ch.lossCnt == 0 && ch.prevSignalType == silkInactive {
		for i := range order {
			c.smthNLSF[i] += wrap16(smulwb(int32(ch.prevNLSF[i])-int32(c.smthNLSF[i]), cngNLSFSmooth))
		}
		maxGain, sub := int32(0), 0
		for i := range ch.nbSubfr {
			if ctl.gains[i] > maxGain {
				maxGain, sub = ctl.gains[i], i
			}
		}
		sl := ch.subfrLength
		copy(c.excBuf[sl:ch.nbSubfr*sl], c.excBuf[:(ch.nbSubfr-1)*sl])
		copy(c.excBuf[:sl], ch.exc[sub*sl:(sub+1)*sl])
		for i := range ch.nbSubfr {
			c.smthGain += smulwb(ctl.gains[i]-c.smthGain, cngGainSmooth)
			if smulww(c.smthGain, cngGainSmoothThresh) > ctl.gains[i] {
				c.smthGain = ctl.gains[i]
			}
		}
	}
	if ch.lossCnt == 0 {
		clear(c.synthState[:order])
		return
	}
	gain := smulww(int32(ch.plc.randScale), ch.plc.prevGain[1])
	if gain >= 1<<21 || c.smthGain > 1<<23 {
		gain = smultt(gain, gain)
		gain = smultt(c.smthGain, c.smthGain) - gain<<5
		gain = sqrtApprox(gain) << 16
	} else {
		gain = smulww(gain, gain)
		gain = smulww(c.smthGain, c.smthGain) - gain<<5
		gain = sqrtApprox(gain) << 8
	}
	gainQ10 := gain >> 6
	var sig [silkMaxFrameLength + silkMaxLPCOrder]int32
	mask := cngBufMaskMax
	for mask > len(frame) {
		mask >>= 1
	}
	seed := c.randSeed
	noise := sig[silkMaxLPCOrder : silkMaxLPCOrder+len(frame)]
	for i := range noise {
		seed = silkRand(seed)
		noise[i] = c.excBuf[int(seed>>24)&mask]
	}
	c.randSeed = seed
	var a [silkMaxLPCOrder]int16
	nlsf2A(a[:order], c.smthNLSF[:order])
	copy(sig[:silkMaxLPCOrder], c.synthState[:])
	lpcSynthesis(sig[:], a[:order], len(frame))
	for i, v := range noise {
		frame[i] = sat16(int32(frame[i]) + int32(sat16(rshiftRound(smulww(v, gainQ10), 8))))
	}
	copy(c.synthState[:], sig[len(frame):len(frame)+silkMaxLPCOrder])
}
