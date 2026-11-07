package vorbis

import (
	"fmt"
	"math"
	"math/bits"
	"slices"

	"github.com/stubbedev/lys-lib/intconv"
)

const (
	maxFastBits    = 10
	codebookSync   = 0x564342
	maxSetupValues = 1 << 23
	maxCodewordLen = 32
)

type codebook struct {
	dims     int
	entries  int
	lengths  []uint8
	fast     []int32
	fastMask uint64
	keys     []uint32
	keyEntry []int32
	single   int32
	vq       []float32
}

func parseCodebook(r *bitReader, index int, budget *int) (*codebook, error) {
	if sync := r.read(24); sync != codebookSync {
		return nil, fmt.Errorf("vorbis: codebook %d sync %06x: %w", index, sync, ErrCorrupt)
	}
	c := &codebook{dims: int(r.read(16)), entries: int(r.read(24)), single: -1}
	if err := spend(budget, c.entries, index); err != nil {
		return nil, err
	}
	ordered := r.flag()
	sparse := !ordered && r.flag()
	minBits := 5
	if ordered || sparse {
		minBits = 1
	}
	if !ordered && c.entries*minBits > r.remaining() {
		return nil, fmt.Errorf("vorbis: codebook %d: %d entries need more than the %d bits left: %w", index, c.entries, r.remaining(), ErrCorrupt)
	}
	c.lengths = make([]uint8, c.entries)
	if !ordered {
		for i := range c.lengths {
			if sparse && !r.flag() {
				continue
			}
			c.lengths[i] = intconv.Must[uint8](r.read(5) + 1)
		}
	} else {
		length := r.read(5) + 1
		for entry := 0; entry < c.entries; length++ {
			if length > maxCodewordLen {
				return nil, fmt.Errorf("vorbis: codebook %d: ordered lengths pass %d bits: %w", index, maxCodewordLen, ErrCorrupt)
			}
			number := int(r.read(ilog(c.entries - entry)))
			if entry+number > c.entries {
				return nil, fmt.Errorf("vorbis: codebook %d: %d ordered entries past %d: %w", index, entry+number, c.entries, ErrCorrupt)
			}
			for i := entry; i < entry+number; i++ {
				c.lengths[i] = uint8(length)
			}
			entry += number
			if r.eop {
				break
			}
		}
	}
	lookup := r.read(4)
	switch lookup {
	case 0:
	case 1, 2:
		if err := c.parseLookup(r, index, lookup, budget); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("vorbis: codebook %d: lookup type %d: %w", index, lookup, ErrCorrupt)
	}
	if r.eop {
		return nil, fmt.Errorf("vorbis: codebook %d: setup header ends inside it: %w", index, ErrCorrupt)
	}
	if err := c.buildDecoder(index); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *codebook) parseLookup(r *bitReader, index int, lookup uint32, budget *int) error {
	if c.dims == 0 {
		return fmt.Errorf("vorbis: codebook %d: vector lookup with no dimensions: %w", index, ErrCorrupt)
	}
	if err := spend(budget, c.entries*c.dims, index); err != nil {
		return err
	}
	minimum := float32(float32Unpack(r.read(32)))
	delta := float32(float32Unpack(r.read(32)))
	valueBits := uint(r.read(4) + 1)
	sequence := r.flag()
	count := c.entries * c.dims
	if lookup == 1 {
		count = lookup1Values(c.entries, c.dims)
	}
	multiplicands := make([]uint32, count)
	for i := range multiplicands {
		multiplicands[i] = r.read(valueBits)
	}
	if r.eop {
		return nil
	}
	c.vq = make([]float32, c.entries*c.dims)
	for e := range c.entries {
		if c.lengths[e] == 0 {
			continue
		}
		var last float32
		divisor := 1
		row := c.vq[e*c.dims : (e+1)*c.dims]
		for i := range row {
			off := e*c.dims + i
			if lookup == 1 {
				off = e / divisor % count
				divisor *= count
			}
			v := float32(float32(multiplicands[off])*delta) + minimum
			v += last
			if sequence {
				last = v
			}
			row[i] = v
		}
	}
	return nil
}

func lookup1Values(entries, dims int) int {
	r := 0
	for pow(r+1, dims) <= entries {
		r++
	}
	return r
}

func pow(base, exp int) int {
	v := 1
	for range exp {
		v *= base
		if v > 1<<30 {
			return v
		}
	}
	return v
}

func (c *codebook) buildDecoder(index int) error {
	var available [maxCodewordLen + 1]uint32
	used := 0
	codewords := make([]uint32, c.entries)
	first := true
	for e, l := range c.lengths {
		if l == 0 {
			continue
		}
		used++
		if first {
			first = false
			for i := 1; i <= int(l); i++ {
				available[i] = 1 << (32 - i)
			}
			continue
		}
		z := int(l)
		for z > 0 && available[z] == 0 {
			z--
		}
		if z == 0 {
			return fmt.Errorf("vorbis: codebook %d: lengths overspecify the code at entry %d: %w", index, e, ErrCorrupt)
		}
		res := available[z]
		available[z] = 0
		codewords[e] = res
		for y := int(l); y > z; y-- {
			available[y] = res + 1<<(32-y)
		}
	}
	if used == 0 {
		return nil
	}
	if used == 1 {
		c.single = intconv.Must[int32](slices.IndexFunc(c.lengths, func(l uint8) bool { return l != 0 }))
		return nil
	}
	for _, a := range available[1:] {
		if a != 0 {
			return fmt.Errorf("vorbis: codebook %d: lengths leave the code incomplete: %w", index, ErrCorrupt)
		}
	}
	fastBits := min(maxFastBits, slices.Max(c.lengths))
	c.fast = make([]int32, 1<<fastBits)
	c.fastMask = intconv.Must[uint64](len(c.fast) - 1)
	for i := range c.fast {
		c.fast[i] = -1
	}
	size := intconv.Must[uint32](len(c.fast))
	var long []uint64
	for e, l := range c.lengths {
		if l == 0 {
			continue
		}
		if l > fastBits {
			long = append(long, uint64(codewords[e])<<32|uint64(e))
			continue
		}
		for j := bits.Reverse32(codewords[e]); j < size; j += 1 << l {
			c.fast[j] = int32(e) | int32(l)<<24
		}
	}
	slices.Sort(long)
	c.keys = make([]uint32, len(long))
	c.keyEntry = make([]int32, len(long))
	for i, k := range long {
		c.keys[i] = uint32(k >> 32)
		c.keyEntry[i] = int32(k & 0xffffff)
	}
	return nil
}

func (c *codebook) decode(r *bitReader) int32 {
	if r.n < maxCodewordLen {
		r.fill()
	}
	if c.single >= 0 {
		return c.consume(r, c.single, uint(c.lengths[c.single]))
	}
	if c.fast == nil {
		r.eop = true
		return -1
	}
	if f := c.fast[r.acc&c.fastMask]; f >= 0 {
		return c.consume(r, f&0xffffff, uint(f>>24))
	}
	if len(c.keys) == 0 {
		r.eop = true
		return -1
	}
	key := bits.Reverse32(uint32(r.acc & math.MaxUint32))
	lo, hi := 0, len(c.keys)
	for hi-lo > 1 {
		mid := int(uint(lo+hi) >> 1)
		if c.keys[mid] <= key {
			lo = mid
		} else {
			hi = mid
		}
	}
	e := c.keyEntry[lo]
	l := uint(c.lengths[e])
	if uint64(key)-uint64(c.keys[lo]) >= 1<<(32-l) {
		r.eop = true
		return -1
	}
	return c.consume(r, e, l)
}

func (c *codebook) consume(r *bitReader, e int32, l uint) int32 {
	if l > r.n {
		r.eop = true
		r.acc, r.n = 0, 0
		return -1
	}
	r.acc >>= l
	r.n -= l
	return e
}

func (c *codebook) vector(e int32) []float32 {
	at := int(e) * c.dims
	return c.vq[at : at+c.dims]
}

func spend(budget *int, values, index int) error {
	if values > *budget {
		return fmt.Errorf("vorbis: codebook %d declares %d values, past the %d the setup header has left of %d: %w", index, values, *budget, maxSetupValues, ErrCorrupt)
	}
	*budget -= values
	return nil
}
