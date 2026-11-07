package vorbis

import (
	"fmt"
	"math"
	"slices"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/gomath"
)

type floor interface {
	decode(r *bitReader, books []*codebook, blockflag int, curve []float32) (bool, error)
}

type floor0 struct {
	order     int
	ampBits   uint
	ampOffset int
	books     []int
	bookBits  uint
	barkMap   [2][]int
	cosine    []float32
	lsp       []float32
}

func parseFloor0(r *bitReader, index int, books []*codebook, blocksizes [2]int) (*floor0, error) {
	f := &floor0{order: int(r.read(8))}
	rate := int(r.read(16))
	barkMapSize := int(r.read(16))
	f.ampBits = uint(r.read(6))
	f.ampOffset = int(r.read(8))
	f.books = make([]int, r.read(4)+1)
	maxDims := 0
	for i := range f.books {
		b := int(r.read(8))
		if b >= len(books) {
			return nil, fmt.Errorf("vorbis: floor %d names codebook %d of %d: %w", index, b, len(books), ErrCorrupt)
		}
		if books[b].vq == nil {
			return nil, fmt.Errorf("vorbis: floor %d: codebook %d has no vector lookup: %w", index, b, ErrCorrupt)
		}
		f.books[i] = b
		maxDims = max(maxDims, books[b].dims)
	}
	if f.order < 1 || rate < 1 || barkMapSize < 1 || f.ampBits < 1 {
		return nil, fmt.Errorf("vorbis: floor %d: order %d, rate %d, bark map size %d, amplitude bits %d: %w", index, f.order, rate, barkMapSize, f.ampBits, ErrCorrupt)
	}
	f.lsp = make([]float32, f.order+maxDims)
	f.bookBits = ilog(len(f.books))
	scale := float32(float64(barkMapSize) / toBark(float32(rate)/2))
	wdel := float32(math.Pi / float64(barkMapSize))
	for w, size := range blocksizes {
		n := size / 2
		m := make([]int, n+1)
		for j := range n {
			val := int(math.Floor(float64(toBark(float32(float32(rate)/2)/float32(n)*float32(j))) * float64(scale)))
			m[j] = min(val, barkMapSize-1)
		}
		m[n] = -1
		f.barkMap[w] = m
	}
	f.cosine = make([]float32, barkMapSize)
	for k := range f.cosine {
		f.cosine[k] = float32(2 * gomath.Cos(float64(wdel*float32(k))))
	}
	return f, nil
}

func toBark(n float32) float64 {
	x := float64(n)
	a := float64(float64(float32(13.1)) * gomath.Atan(float64(float32(0.00074))*x))
	b := float64(float64(float32(2.24)) * gomath.Atan(x*x*float64(float32(1.85e-8))))
	return a + b + float64(float64(float32(1e-4))*x)
}

func (f *floor0) decode(r *bitReader, books []*codebook, blockflag int, curve []float32) (bool, error) {
	amplitude := r.read(f.ampBits)
	if amplitude == 0 || r.eop {
		return false, nil
	}
	maxval := float32(int(1)<<f.ampBits - 1)
	amp := float32(amplitude) / maxval * float32(f.ampOffset)
	bookNum := int(r.read(f.bookBits))
	if r.eop {
		return false, nil
	}
	if bookNum >= len(f.books) {
		return false, fmt.Errorf("vorbis: floor 0 packet names book %d of %d: %w", bookNum, len(f.books), ErrCorrupt)
	}
	book := books[f.books[bookNum]]
	lsp := f.lsp
	for j := 0; j < f.order; j += book.dims {
		e := book.decode(r)
		if e < 0 {
			return false, nil
		}
		copy(lsp[j:], book.vector(e))
	}
	var last float32
	for j := 0; j < f.order; {
		for k := 0; j < f.order && k < book.dims; k++ {
			lsp[j] += last
			j++
		}
		last = lsp[j-1]
	}
	for i := range f.order {
		lsp[i] = float32(2 * gomath.Cos(float64(lsp[i])))
	}
	m := f.barkMap[blockflag]
	n := len(curve)
	for i := 0; i < n; {
		k := m[i]
		w := f.cosine[k]
		p, q := float32(0.5), float32(0.5)
		j := 1
		for ; j < f.order; j += 2 {
			q *= w - lsp[j-1]
			p *= w - lsp[j]
		}
		if j == f.order {
			q *= w - lsp[j-1]
			p *= p * (4 - float32(w*w))
			q *= q
		} else {
			p *= p * (2 - w)
			q *= q * (2 + w)
		}
		v := float32(gomath.Exp((float64(amp)/math.Sqrt(float64(p+q)) - float64(f.ampOffset)) * float64(float32(0.11512925))))
		curve[i] = v
		for i++; i < n && m[i] == k; i++ {
			curve[i] = v
		}
	}
	return true, nil
}

type floor1 struct {
	partitionClass []uint8
	classDims      []int
	classBits      []uint
	classMaster    []int
	subclassBooks  [][]int
	multiplier     int
	yBits          uint
	xs             []int
	sorted         []int
	low, high      []int
	y              []int
	finalY         []int
	step2          []bool
}

var floor1Ranges = [4]int{256, 128, 86, 64}

func parseFloor1(r *bitReader, index int, books []*codebook) (*floor1, error) {
	f := &floor1{partitionClass: make([]uint8, r.read(5))}
	maxClass := -1
	for i := range f.partitionClass {
		f.partitionClass[i] = intconv.Must[uint8](r.read(4))
		maxClass = max(maxClass, int(f.partitionClass[i]))
	}
	classes := maxClass + 1
	f.classDims = make([]int, classes)
	f.classBits = make([]uint, classes)
	f.classMaster = make([]int, classes)
	f.subclassBooks = make([][]int, classes)
	book := func(b int) error {
		if b >= len(books) {
			return fmt.Errorf("vorbis: floor %d names codebook %d of %d: %w", index, b, len(books), ErrCorrupt)
		}
		return nil
	}
	for c := range classes {
		f.classDims[c] = int(r.read(3) + 1)
		f.classBits[c] = uint(r.read(2))
		f.classMaster[c] = -1
		if f.classBits[c] > 0 {
			f.classMaster[c] = int(r.read(8))
			if err := book(f.classMaster[c]); err != nil {
				return nil, err
			}
		}
		f.subclassBooks[c] = make([]int, 1<<f.classBits[c])
		for j := range f.subclassBooks[c] {
			b := int(r.read(8)) - 1
			if b >= 0 {
				if err := book(b); err != nil {
					return nil, err
				}
			}
			f.subclassBooks[c][j] = b
		}
	}
	f.multiplier = int(r.read(2) + 1)
	f.yBits = ilog(floor1Ranges[f.multiplier-1] - 1)
	rangeBits := uint(r.read(4))
	f.xs = append(f.xs, 0, 1<<rangeBits)
	for _, c := range f.partitionClass {
		for range f.classDims[c] {
			f.xs = append(f.xs, int(r.read(rangeBits)))
		}
	}
	if len(f.xs) > 65 {
		return nil, fmt.Errorf("vorbis: floor %d has %d points, more than 65: %w", index, len(f.xs), ErrCorrupt)
	}
	f.sorted = make([]int, len(f.xs))
	for i := range f.sorted {
		f.sorted[i] = i
	}
	slices.SortFunc(f.sorted, func(a, b int) int { return f.xs[a] - f.xs[b] })
	for i := 1; i < len(f.sorted); i++ {
		if f.xs[f.sorted[i]] == f.xs[f.sorted[i-1]] {
			return nil, fmt.Errorf("vorbis: floor %d repeats point %d: %w", index, f.xs[f.sorted[i]], ErrCorrupt)
		}
	}
	f.low = make([]int, len(f.xs))
	f.high = make([]int, len(f.xs))
	for i := 2; i < len(f.xs); i++ {
		lo, hi := 0, 1
		for j := range i {
			if f.xs[j] < f.xs[i] && f.xs[j] > f.xs[lo] {
				lo = j
			}
			if f.xs[j] > f.xs[i] && f.xs[j] < f.xs[hi] {
				hi = j
			}
		}
		f.low[i], f.high[i] = lo, hi
	}
	f.y = make([]int, len(f.xs))
	f.finalY = make([]int, len(f.xs))
	f.step2 = make([]bool, len(f.xs))
	return f, nil
}

func (f *floor1) decode(r *bitReader, books []*codebook, _ int, curve []float32) (bool, error) {
	if !r.flag() {
		return false, nil
	}
	rng := floor1Ranges[f.multiplier-1]
	f.y[0] = int(r.read(f.yBits))
	f.y[1] = int(r.read(f.yBits))
	offset := 2
	for _, c := range f.partitionClass {
		dims := f.classDims[c]
		cbits := f.classBits[c]
		csub := 1<<cbits - 1
		cval := 0
		if cbits > 0 {
			cval = int(books[f.classMaster[c]].decode(r))
			if cval < 0 {
				return false, nil
			}
		}
		for j := range dims {
			b := f.subclassBooks[c][cval&csub]
			cval >>= cbits
			f.y[offset+j] = 0
			if b >= 0 {
				v := books[b].decode(r)
				if v < 0 {
					return false, nil
				}
				f.y[offset+j] = int(v)
			}
		}
		offset += dims
	}
	if r.eop {
		return false, nil
	}
	f.finalY[0], f.finalY[1] = f.y[0], f.y[1]
	f.step2[0], f.step2[1] = true, true
	for i := 2; i < len(f.xs); i++ {
		lo, hi := f.low[i], f.high[i]
		predicted := renderPoint(f.xs[lo], f.finalY[lo], f.xs[hi], f.finalY[hi], f.xs[i])
		val := f.y[i]
		highroom := rng - predicted
		lowroom := predicted
		room := min(highroom, lowroom) * 2
		if val == 0 {
			f.step2[i] = false
			f.finalY[i] = predicted
			continue
		}
		f.step2[lo], f.step2[hi], f.step2[i] = true, true, true
		switch {
		case val >= room && highroom > lowroom:
			val -= lowroom
		case val >= room:
			val = highroom - val - 1
		case val&1 == 1:
			val = -(val + 1) / 2
		default:
			val /= 2
		}
		f.finalY[i] = (val + predicted) & 0x7fff
	}
	lx := 0
	ly := f.amplitude(f.sorted[0])
	for _, i := range f.sorted[1:] {
		if !f.step2[i] {
			continue
		}
		hy := f.amplitude(i)
		renderLine(lx, ly, f.xs[i], hy, curve)
		lx, ly = f.xs[i], hy
	}
	for x := lx; x < len(curve); x++ {
		curve[x] = inverseDB[ly]
	}
	return true, nil
}

func (f *floor1) amplitude(i int) int {
	return min(max(f.finalY[i]*f.multiplier, 0), 255)
}

func renderPoint(x0, y0, x1, y1, x int) int {
	dy := y1 - y0
	adx := x1 - x0
	off := abs(dy) * (x - x0) / adx
	if dy < 0 {
		return y0 - off
	}
	return y0 + off
}

func renderLine(x0, y0, x1, y1 int, out []float32) {
	end := min(x1, len(out))
	if x0 >= end {
		return
	}
	dy := y1 - y0
	adx := x1 - x0
	base := dy / adx
	ady := abs(dy) - abs(base)*adx
	sy := base + 1
	if dy < 0 {
		sy = base - 1
	}
	y := y0
	errAcc := 0
	out[x0] = inverseDB[y]
	for x := x0 + 1; x < end; x++ {
		errAcc += ady
		if errAcc >= adx {
			errAcc -= adx
			y += sy
		} else {
			y += base
		}
		out[x] = inverseDB[y]
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
