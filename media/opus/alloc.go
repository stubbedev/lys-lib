package opus

// allocation is the outcome of the bit allocation: per band the PVQ
// budget in 1/8 bits, the fine energy bits and whether a band takes a
// leftover energy bit first, plus the coded band count, the overflow
// carried into band quantisation, and the stereo parameters.
type allocation struct {
	pulses     [nbBands]int
	fine       [nbBands]int
	priority   [nbBands]int
	codedBands int
	balance    int
	intensity  int
	dualStereo bool
}

// bandWidth is band i's width in 2.5 ms bins.
func bandWidth(i int) int { return eBands[i+1] - eBands[i] }

// cacheRow is the pulse cost row of band i split down to lm.
func cacheRow(i, lm int) []int { return cacheBits[cacheIndex[(lm+1)*nbBands+i]:] }

// getPulses maps a pseudo-pulse index to a pulse count.
func getPulses(i int) int {
	if i < 8 {
		return i
	}
	return (8 + i&7) << (i>>3 - 1)
}

// bits2pulses is the pseudo-pulse count whose cost is closest to bits.
func bits2pulses(band, lm, bits int) int {
	cache := cacheRow(band, lm)
	lo, hi := 0, cache[0]
	bits--
	for range logMaxPseudo {
		mid := (lo + hi + 1) >> 1
		if cache[mid] >= bits {
			hi = mid
		} else {
			lo = mid
		}
	}
	loBits := -1
	if lo != 0 {
		loBits = cache[lo]
	}
	if bits-loBits <= cache[hi]-bits {
		return lo
	}
	return hi
}

// pulses2bits is the cost in 1/8 bits of a pseudo-pulse count.
func pulses2bits(band, lm, pulses int) int {
	if pulses == 0 {
		return 0
	}
	return cacheRow(band, lm)[pulses] + 1
}

// bandCaps is each band's maximum useful allocation in 1/8 bits.
func bandCaps(channels int) [nbBands]int {
	var caps [nbBands]int
	for i := range nbBands {
		n := bandWidth(i) << frameLM
		caps[i] = (cacheCaps[nbBands*(2*frameLM+channels-1)+i] + 64) * channels * n >> 2
	}
	return caps
}

// allocate is compute_allocation: it interpolates the static
// allocation table to total 1/8 bits, coding the skip, intensity and
// dual stereo decisions it takes on the way.
func allocate(rc *rangeEncoder, channels int, offsets, caps *[nbBands]int, trim int, intensity int, dualStereo bool, total, prev int) allocation {
	const end = nbBands
	total = max(total, 0)
	skipRsv := 0
	if total >= 1<<bitRes {
		skipRsv = 1 << bitRes
	}
	total -= skipRsv
	intensityRsv, dualRsv := 0, 0
	if channels == 2 {
		intensityRsv = log2FracTable[end]
		if intensityRsv > total {
			intensityRsv = 0
		} else {
			total -= intensityRsv
			if total >= 1<<bitRes {
				dualRsv = 1 << bitRes
			}
			total -= dualRsv
		}
	}
	var thresh, trimOffset [nbBands]int
	for j := range end {
		n0 := bandWidth(j)
		thresh[j] = max(channels<<bitRes, (3*n0<<frameLM<<bitRes)>>4)
		trimOffset[j] = channels * n0 * (trim - 5 - frameLM) * (end - j - 1) * (1 << (frameLM + bitRes)) >> 6
		if n0<<frameLM == 1 {
			trimOffset[j] -= channels << bitRes
		}
	}
	trimmed := func(b, j int) int {
		if b > 0 {
			return max(0, b+trimOffset[j])
		}
		return b
	}
	vectorBits := func(v, j int) int {
		return trimmed(channels*bandWidth(j)*bandAllocation[v][j]<<frameLM>>2, j)
	}
	lo, hi := 1, len(bandAllocation)-1
	for lo <= hi {
		done := false
		psum := 0
		mid := (lo + hi) >> 1
		for j := end - 1; j >= 0; j-- {
			b := vectorBits(mid, j) + offsets[j]
			if b >= thresh[j] || done {
				done = true
				psum += min(b, caps[j])
			} else if b >= channels<<bitRes {
				psum += channels << bitRes
			}
		}
		if psum > total {
			hi = mid - 1
		} else {
			lo = mid + 1
		}
	}
	hi = lo
	lo--
	var bits1, bits2 [nbBands]int
	skipStart := 0
	for j := range end {
		b1 := vectorBits(lo, j)
		b2 := trimmed(caps[j], j)
		if hi < len(bandAllocation) {
			b2 = vectorBits(hi, j)
		}
		if lo > 0 {
			b1 += offsets[j]
		}
		b2 += offsets[j]
		if offsets[j] > 0 {
			skipStart = j
		}
		bits1[j] = b1
		bits2[j] = max(0, b2-b1)
	}
	return interpBits2Pulses(rc, channels, skipStart, &bits1, &bits2, &thresh, caps, total, skipRsv, intensity, intensityRsv, dualStereo, dualRsv, prev)
}

func interpBits2Pulses(rc *rangeEncoder, channels, skipStart int, bits1, bits2, thresh, caps *[nbBands]int, total, skipRsv, intensity, intensityRsv int, dualStereo bool, dualRsv, prev int) allocation {
	const end = nbBands
	allocFloor := channels << bitRes
	stereoShift := 0
	if channels > 1 {
		stereoShift = 1
	}
	logM := frameLM << bitRes
	lo, hi := 0, 1<<allocSteps
	for range allocSteps {
		mid := (lo + hi) >> 1
		psum := 0
		done := false
		for j := end - 1; j >= 0; j-- {
			tmp := bits1[j] + (mid * bits2[j] >> allocSteps)
			if tmp >= thresh[j] || done {
				done = true
				psum += min(tmp, caps[j])
			} else if tmp >= allocFloor {
				psum += allocFloor
			}
		}
		if psum > total {
			hi = mid
		} else {
			lo = mid
		}
	}
	var a allocation
	bits := &a.pulses
	psum := 0
	done := false
	for j := end - 1; j >= 0; j-- {
		tmp := bits1[j] + (lo * bits2[j] >> allocSteps)
		if tmp < thresh[j] && !done {
			if tmp >= allocFloor {
				tmp = allocFloor
			} else {
				tmp = 0
			}
		} else {
			done = true
		}
		tmp = min(tmp, caps[j])
		bits[j] = tmp
		psum += tmp
	}
	codedBands := end
	for ; ; codedBands-- {
		j := codedBands - 1
		if j <= skipStart {
			total += skipRsv
			break
		}
		left := total - psum
		percoeff := left / (eBands[codedBands] - eBands[0])
		left -= (eBands[codedBands] - eBands[0]) * percoeff
		rem := max(left-(eBands[j]-eBands[0]), 0)
		bandW := eBands[codedBands] - eBands[j]
		bandBits := bits[j] + percoeff*bandW + rem
		if bandBits >= max(thresh[j], allocFloor+(1<<bitRes)) {
			hyst := 9
			if j < prev {
				hyst = 7
			}
			if bandBits > (hyst*bandW<<frameLM<<bitRes)>>4 {
				rc.bitLogp(true, 1)
				break
			}
			rc.bitLogp(false, 1)
			psum += 1 << bitRes
			bandBits -= 1 << bitRes
		}
		psum -= bits[j] + intensityRsv
		if intensityRsv > 0 {
			intensityRsv = log2FracTable[j]
		}
		psum += intensityRsv
		if bandBits >= allocFloor {
			psum += allocFloor
			bits[j] = allocFloor
		} else {
			bits[j] = 0
		}
	}
	a.codedBands = codedBands
	if intensityRsv > 0 {
		a.intensity = min(intensity, codedBands)
		rc.uint(uint32(a.intensity), uint32(codedBands+1))
	}
	if a.intensity <= 0 {
		total += dualRsv
		dualRsv = 0
	}
	if dualRsv > 0 {
		a.dualStereo = dualStereo
		rc.bitLogp(dualStereo, 1)
	}
	left := total - psum
	percoeff := left / (eBands[codedBands] - eBands[0])
	left -= (eBands[codedBands] - eBands[0]) * percoeff
	for j := range codedBands {
		bits[j] += percoeff * bandWidth(j)
	}
	for j := range codedBands {
		tmp := min(left, bandWidth(j))
		bits[j] += tmp
		left -= tmp
	}
	balance := 0
	j := 0
	for ; j < codedBands; j++ {
		n := bandWidth(j) << frameLM
		bits[j] += balance
		var excess int
		if n > 1 {
			excess = max(bits[j]-caps[j], 0)
			bits[j] -= excess
			den := channels * n
			if channels == 2 && n > 2 && !a.dualStereo && j < a.intensity {
				den++
			}
			nclogn := den * (logN[j] + logM)
			offset := (nclogn >> 1) - den*fineOffset
			if n == 2 {
				offset += den << bitRes >> 2
			}
			if bits[j]+offset < den*2<<bitRes {
				offset += nclogn >> 2
			} else if bits[j]+offset < den*3<<bitRes {
				offset += nclogn >> 3
			}
			a.fine[j] = max(0, (bits[j]+offset+(den<<(bitRes-1)))/(den<<bitRes))
			if channels*a.fine[j] > bits[j]>>bitRes {
				a.fine[j] = bits[j] >> stereoShift >> bitRes
			}
			a.fine[j] = min(a.fine[j], maxFineBits)
			a.priority[j] = b2i(a.fine[j]*(den<<bitRes) >= bits[j]+offset)
			bits[j] -= channels * a.fine[j] << bitRes
		} else {
			excess = max(0, bits[j]-(channels<<bitRes))
			bits[j] -= excess
			a.fine[j] = 0
			a.priority[j] = 1
		}
		if excess > 0 {
			extraFine := min(excess>>(stereoShift+bitRes), maxFineBits-a.fine[j])
			a.fine[j] += extraFine
			extraBits := extraFine * channels << bitRes
			a.priority[j] = b2i(extraBits >= excess-balance)
			excess -= extraBits
		}
		balance = excess
	}
	a.balance = balance
	for ; j < end; j++ {
		a.fine[j] = bits[j] >> stereoShift >> bitRes
		bits[j] = 0
		a.priority[j] = b2i(a.fine[j] < 1)
	}
	return a
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
