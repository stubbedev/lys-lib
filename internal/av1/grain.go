package av1

// The sizes of the film grain templates (7.18.3.3): the luma one, and
// the chroma ones of a 4:2:0 picture.
const (
	GrainLumaWidth    = 82
	GrainLumaHeight   = 73
	GrainChromaWidth  = 44
	GrainChromaHeight = 38
)

// Grain is what synthesizing a frame's film grain (7.18.3) reads beside
// the frame's samples: the grain templates the noise is cut from, the
// scaling function of each plane, and where in the templates each 32x32
// block of the picture starts. It is laid out as words for a shader to
// read; a Grain kept between frames makes one without allocating.
type Grain struct {
	Luma    [GrainLumaHeight * GrainLumaWidth]int32
	Cb, Cr  [GrainChromaHeight * GrainChromaWidth]int32
	Scaling [3][256]int32
	// Offsets holds, for each row of blocks and each block in it, the
	// eight random bits that place the block in the templates; Stride
	// is the blocks in a row.
	Offsets []int32
	Stride  int
}

// grainRandom is the film grain pseudo-random register (7.18.3.2).
type grainRandom uint16

// next is get_random_number(bits).
func (r *grainRandom) next(bits uint) int32 {
	v := uint16(*r)
	bit := (v ^ v>>1 ^ v>>3 ^ v>>12) & 1
	v = v>>1 | bit<<15
	*r = grainRandom(v)
	return int32(v >> (16 - bits) & (1<<bits - 1))
}

// round2 is Round2 of a signed value.
func round2(x int32, n uint) int32 {
	if n == 0 {
		return x
	}
	return (x + 1<<(n-1)) >> n
}

// Synthesize fills t for a w x h 4:2:0 frame of bitDepth-bit samples
// that applies g: the generate grain process (7.18.3.3), the scaling
// lookup initialization (7.18.3.4) and the random offsets of the add
// noise synthesis process (7.18.3.5).
func (g *FilmGrain) Synthesize(t *Grain, bitDepth, w, h int) {
	shift := uint(12 - bitDepth + int(g.GrainScaleShift))
	center := int32(128) << (bitDepth - 8)
	lo, hi := -center, int32(256)<<(bitDepth-8)-1-center
	draw := func(dst []int32, seed uint16, on bool) {
		if !on {
			clear(dst)
			return
		}
		r := grainRandom(seed)
		for i := range dst {
			dst[i] = round2(int32(gaussianSequence[r.next(11)]), shift)
		}
	}
	chroma := func(points uint8) bool { return points > 0 || g.ChromaScalingFromLuma }
	draw(t.Luma[:], g.GrainSeed, g.NumYPoints > 0)
	draw(t.Cb[:], g.GrainSeed^0xb524, chroma(g.NumCbPoints))
	draw(t.Cr[:], g.GrainSeed^0x49d8, chroma(g.NumCrPoints))
	lag := int(g.ArCoeffLag)
	arShift := uint(g.ArCoeffShiftMinus6) + 6
	clip := func(v int32) int32 { return max(lo, min(hi, v)) }
	if g.NumYPoints > 0 {
		for y := 3; y < GrainLumaHeight; y++ {
			for x := 3; x < GrainLumaWidth-3; x++ {
				var sum int32
				pos := 0
				for dy := -lag; dy <= 0; dy++ {
					for dx := -lag; dx <= lag && (dy != 0 || dx != 0); dx++ {
						sum += (int32(g.ArCoeffsYPlus128[pos]) - 128) * t.Luma[(y+dy)*GrainLumaWidth+x+dx]
						pos++
					}
				}
				at := y*GrainLumaWidth + x
				t.Luma[at] = clip(t.Luma[at] + round2(sum, arShift))
			}
		}
	}
	for y := 3; y < GrainChromaHeight; y++ {
		for x := 3; x < GrainChromaWidth-3; x++ {
			var sum [2]int32
			pos := 0
			for dy := -lag; dy <= 0; dy++ {
				for dx := -lag; dx <= lag && (dy != 0 || dx != 0); dx++ {
					at := (y+dy)*GrainChromaWidth + x + dx
					sum[0] += (int32(g.ArCoeffsCbPlus128[pos]) - 128) * t.Cb[at]
					sum[1] += (int32(g.ArCoeffsCrPlus128[pos]) - 128) * t.Cr[at]
					pos++
				}
			}
			if g.NumYPoints > 0 {
				lx, ly := (x-3)<<1+3, (y-3)<<1+3
				luma := t.Luma[ly*GrainLumaWidth+lx] + t.Luma[ly*GrainLumaWidth+lx+1] + t.Luma[(ly+1)*GrainLumaWidth+lx] + t.Luma[(ly+1)*GrainLumaWidth+lx+1]
				luma = round2(luma, 2)
				sum[0] += luma * (int32(g.ArCoeffsCbPlus128[pos]) - 128)
				sum[1] += luma * (int32(g.ArCoeffsCrPlus128[pos]) - 128)
			}
			at := y*GrainChromaWidth + x
			if chroma(g.NumCbPoints) {
				t.Cb[at] = clip(t.Cb[at] + round2(sum[0], arShift))
			}
			if chroma(g.NumCrPoints) {
				t.Cr[at] = clip(t.Cr[at] + round2(sum[1], arShift))
			}
		}
	}
	grainScaling(&t.Scaling[0], g.PointYValue[:g.NumYPoints], g.PointYScaling[:g.NumYPoints])
	if g.ChromaScalingFromLuma {
		t.Scaling[1], t.Scaling[2] = t.Scaling[0], t.Scaling[0]
	} else {
		grainScaling(&t.Scaling[1], g.PointCbValue[:g.NumCbPoints], g.PointCbScaling[:g.NumCbPoints])
		grainScaling(&t.Scaling[2], g.PointCrValue[:g.NumCrPoints], g.PointCrScaling[:g.NumCrPoints])
	}
	t.Stride = ((w+1)/2 + 15) / 16
	rows := ((h+1)/2 + 15) / 16
	t.Offsets = t.Offsets[:0]
	for row := range rows {
		r := grainRandom(g.GrainSeed)
		r ^= grainRandom((row*37 + 178) & 255 << 8)
		r ^= grainRandom((row*173 + 105) & 255)
		for range t.Stride {
			t.Offsets = append(t.Offsets, r.next(8))
		}
	}
}

// grainScaling fills lut with the piecewise linear scaling function
// through the points (7.18.3.4): zero with no points.
func grainScaling(lut *[256]int32, values, scaling []uint8) {
	*lut = [256]int32{}
	n := len(values)
	if n == 0 {
		return
	}
	for i := range int(values[0]) {
		lut[i] = int32(scaling[0])
	}
	for i := range n - 1 {
		dy := int32(scaling[i+1]) - int32(scaling[i])
		dx := int32(values[i+1]) - int32(values[i])
		delta := dy * ((65536 + dx>>1) / dx)
		for x := range dx {
			lut[int32(values[i])+x] = int32(scaling[i]) + (x*delta+32768)>>16
		}
	}
	for i := int(values[n-1]); i < 256; i++ {
		lut[i] = int32(scaling[n-1])
	}
}
