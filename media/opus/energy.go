package opus

import "math"

// bandEnergies sets each band's amplitude (the root of its energy),
// its log2 relative to the band mean, and the unit-norm shape.
func bandEnergies(freq, norm []float32, amp, logE *[nbBands]float32) {
	for i := range nbBands {
		band := freq[bandStart(i):bandStart(i+1)]
		sum := float32(1e-27)
		for _, v := range band {
			sum += v * v
		}
		a := float32(math.Sqrt(float64(sum)))
		amp[i] = a
		logE[i] = float32(math.Log2(float64(a))) - eMeans[i]
		g := 1 / (1e-27 + a)
		for j, v := range band {
			norm[bandStart(i)+j] = v * g
		}
	}
}

// bandStart is the first MDCT coefficient of band i in a 20 ms frame.
func bandStart(i int) int { return eBands[i] << frameLM }

// laplaceEncode codes the coarse energy residual *value with the
// Laplace-like model of probability fs for zero and decay; it clamps
// *value when the model runs out of range.
func laplaceEncode(rc *rangeEncoder, value *int, fs uint32, decay uint32) {
	const minP = 1
	const nMin = 16
	var fl uint32
	val := *value
	if val != 0 {
		neg := val < 0
		if neg {
			val = -val
		}
		fl = fs
		fs = (32768 - minP*2*nMin - fs) * (16384 - decay) >> 15
		i := 1
		for ; fs > 0 && i < val; i++ {
			fs *= 2
			fl += fs + 2*minP
			fs = fs * decay >> 15
		}
		if fs == 0 {
			ndiMax := int(32768 - fl + minP - 1)
			sign := 0
			if neg {
				sign = 1
			}
			ndiMax = (ndiMax + sign) >> 1
			di := min(val-i, ndiMax-1)
			fl += uint32(2*di + 1 - sign)
			fs = min(minP, 32768-fl)
			*value = i + di
			if neg {
				*value = -*value
			}
		} else {
			fs += minP
			if !neg {
				fl += fs
			}
		}
	}
	rc.encodeBin(fl, fl+fs, 15)
}

// coarseEnergy is quant_coarse_energy_impl for one prediction mode: it
// codes the integer part of each band's log energy against old (which
// it updates), stores the leftover in errs, and returns how far budget
// pressure pushed the codes from the ideal ones.
func coarseEnergy(rc *rangeEncoder, channels int, logE, old, errs *[2][nbBands]float32, budget int, intra bool, maxDecay float32) int {
	if rc.tell()+3 <= budget {
		rc.bitLogp(intra, 3)
	}
	coef, beta, model := predCoef, betaInter, &eProbModel[0]
	if intra {
		coef, beta, model = 0, betaIntra, &eProbModel[1]
	}
	var prev [2]float32
	badness := 0
	for i := range nbBands {
		for c := range prev[:channels] {
			x := logE[c][i]
			oldE := max(-9, old[c][i])
			f := x - coef*oldE - prev[c]
			qi := int(math.Floor(float64(0.5 + f)))
			decayBound := max(-28, old[c][i]) - maxDecay
			if qi < 0 && x < decayBound {
				qi += int(decayBound - x)
				qi = min(qi, 0)
			}
			qi0 := qi
			tell := rc.tell()
			bitsLeft := budget - tell - 3*channels*(nbBands-i)
			if i != 0 && bitsLeft < 30 {
				if bitsLeft < 24 {
					qi = min(1, qi)
				}
				if bitsLeft < 16 {
					qi = max(-1, qi)
				}
			}
			switch {
			case budget-tell >= 15:
				pi := 2 * min(i, 20)
				laplaceEncode(rc, &qi, uint32(model[pi])<<7, uint32(model[pi+1])<<6)
			case budget-tell >= 2:
				qi = max(-1, min(qi, 1))
				s := 2 * qi
				if qi < 0 {
					s = -s - 1
				}
				rc.icdf(s, smallEnergyICDF, 2)
			case budget-tell >= 1:
				qi = min(0, qi)
				rc.bitLogp(qi != 0, 1)
			default:
				qi = -1
			}
			errs[c][i] = f - float32(qi)
			badness += abs(qi0 - qi)
			q := float32(qi)
			old[c][i] = coef*oldE + prev[c] + q
			prev[c] += q - beta*q
		}
	}
	return badness
}

// fineEnergy codes fine[i] more bits of each band's energy residual.
func fineEnergy(rc *rangeEncoder, channels int, old, errs *[2][nbBands]float32, fine *[nbBands]int) {
	for i := range nbBands {
		if fine[i] <= 0 {
			continue
		}
		frac := 1 << fine[i]
		for c := range channels {
			q2 := int(math.Floor(float64((errs[c][i] + 0.5) * float32(frac))))
			q2 = max(0, min(q2, frac-1))
			rc.bits(uint32(q2), uint(fine[i]))
			offset := (float32(q2)+0.5)*float32(int(1)<<(14-fine[i]))/16384 - 0.5
			old[c][i] += offset
			errs[c][i] -= offset
		}
	}
}

// finaliseEnergy spends the bits PVQ left over on one more energy bit
// per band, the bands flagged by priority 0 first.
func finaliseEnergy(rc *rangeEncoder, channels int, old, errs *[2][nbBands]float32, fine, priority *[nbBands]int, bitsLeft int) {
	for prio := range 2 {
		for i := 0; i < nbBands && bitsLeft >= channels; i++ {
			if fine[i] >= maxFineBits || priority[i] != prio {
				continue
			}
			for c := range channels {
				q2 := 0
				if errs[c][i] >= 0 {
					q2 = 1
				}
				rc.bits(uint32(q2), 1)
				old[c][i] += (float32(q2) - 0.5) * float32(int(1)<<(14-fine[i]-1)) / 16384
				bitsLeft--
			}
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
