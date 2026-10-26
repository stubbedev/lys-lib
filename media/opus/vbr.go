package opus

// vbrState is constrained VBR's memory: the bit reservoir that keeps
// the bitrate within one frame of the average, and the drift that
// steers the targets onto the average.
type vbrState struct {
	reservoir int
	drift     int
	offset    int
	count     int
}

// vbrTarget is libopus's compute_vbr without the tonality analysis: the
// frame's size in 1/8 bits from base, the average less the frame
// overhead, adjusted for the stereo saving, the dynamic allocation's
// boost, transients, the spectrum's depth above the noise floor and
// its loudness relative to the long-term average, then pulled back
// towards base as constrained VBR cannot sustain a high rate.
func (e *Encoder) vbrTarget(base, equivRate int, d *dynamics, tfEstimate, temporal float32) int {
	codedBands := e.lastCodedBands
	if codedBands == 0 {
		codedBands = nbBands
	}
	codedBins := eBands[codedBands] << frameLM
	if e.channels == 2 {
		codedBins += eBands[min(e.intensity, codedBands)] << frameLM
	}
	target := base
	if e.channels == 2 {
		stereoBands := min(e.intensity, codedBands)
		dof := eBands[stereoBands]<<frameLM - stereoBands
		maxFrac := 0.8 * float32(dof) / float32(codedBins)
		saving := min(e.stereoSaving, 1)
		target -= int(min(maxFrac*float32(target), (saving-0.1)*float32(dof<<bitRes)))
	}
	target += d.totalBoost - 19<<frameLM
	target += int(2 * (tfEstimate - 0.044) * float32(target))
	floorDepth := int(float32(e.channels*eBands[nbBands-2]<<frameLM<<bitRes) * d.maxDepth)
	target = min(target, max(floorDepth, target>>2))
	target = base + int(0.67*float32(target-base))
	if tfEstimate < 0.2 {
		amount := 0.0000031 * float32(max(0, min(32000, 96000-equivRate)))
		target += int(temporal * amount * float32(target))
	}
	return min(2*base, target)
}

// frameBytes picks a constrained VBR frame's size in bytes within
// [minBytes, maxBytes] for target 1/8 bits (counting what is coded so
// far) against the average rate, and updates the reservoir and drift.
func (v *vbrState) frameBytes(rate, target, minBytes, maxBytes int, silence bool) int {
	bytes := min(maxBytes, max(minBytes, (target+(1<<(bitRes+2)))>>(bitRes+3)))
	delta := target - rate
	used := bytes << (bitRes + 3)
	if silence {
		bytes = 2
		used = 2 * 8 << bitRes
		delta = 0
	}
	alpha := float32(0.001)
	if v.count < 970 {
		v.count++
		alpha = 1 / float32(v.count+20)
	}
	v.reservoir += used - rate
	v.drift += int(alpha * float32(delta-v.offset-v.drift))
	v.offset = -v.drift
	if v.reservoir < 0 {
		if !silence {
			bytes += -v.reservoir / (8 << bitRes)
		}
		v.reservoir = 0
	}
	return min(maxBytes, bytes)
}
