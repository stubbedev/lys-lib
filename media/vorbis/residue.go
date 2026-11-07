package vorbis

import (
	"fmt"

	"github.com/stubbedev/lys-lib/intconv"
)

type residue struct {
	kind            int
	begin, end      int
	partitionSize   int
	classifications int32
	classbook       int
	books           [][8]int
	classes         [][]int32
	interleaved     []float32
}

func parseResidue(r *bitReader, index int, books []*codebook) (*residue, error) {
	res := &residue{kind: int(r.read(16))}
	if res.kind > 2 {
		return nil, fmt.Errorf("vorbis: residue %d has type %d: %w", index, res.kind, ErrCorrupt)
	}
	res.begin = int(r.read(24))
	res.end = int(r.read(24))
	res.partitionSize = int(r.read(24) + 1)
	res.classifications = intconv.Must[int32](r.read(6) + 1)
	res.classbook = int(r.read(8))
	if res.classbook >= len(books) {
		return nil, fmt.Errorf("vorbis: residue %d names classbook %d of %d: %w", index, res.classbook, len(books), ErrCorrupt)
	}
	if books[res.classbook].dims == 0 {
		return nil, fmt.Errorf("vorbis: residue %d: classbook %d has no dimensions: %w", index, res.classbook, ErrCorrupt)
	}
	cascade := make([]uint32, res.classifications)
	for i := range cascade {
		low := r.read(3)
		var high uint32
		if r.flag() {
			high = r.read(5)
		}
		cascade[i] = high<<3 | low
	}
	res.books = make([][8]int, res.classifications)
	for i := range res.books {
		for j := range 8 {
			res.books[i][j] = -1
			if cascade[i]&(1<<j) == 0 {
				continue
			}
			b := int(r.read(8))
			if b >= len(books) {
				return nil, fmt.Errorf("vorbis: residue %d names codebook %d of %d: %w", index, b, len(books), ErrCorrupt)
			}
			if books[b].vq == nil {
				return nil, fmt.Errorf("vorbis: residue %d: codebook %d has no vector lookup: %w", index, b, ErrCorrupt)
			}
			if res.kind == 0 && res.partitionSize%books[b].dims != 0 {
				return nil, fmt.Errorf("vorbis: residue %d: partition size %d is not a multiple of codebook %d's %d dimensions: %w", index, res.partitionSize, b, books[b].dims, ErrCorrupt)
			}
			res.books[i][j] = b
		}
	}
	return res, nil
}

func (res *residue) allocate(channels, maxHalf int, books []*codebook) {
	size := maxHalf
	vectors := channels
	if res.kind == 2 {
		size *= channels
		vectors = 1
		res.interleaved = make([]float32, size)
	}
	partitions := size/res.partitionSize + books[res.classbook].dims
	res.classes = make([][]int32, vectors)
	for i := range res.classes {
		res.classes[i] = make([]int32, partitions)
	}
}

func (res *residue) decode(r *bitReader, books []*codebook, vectors [][]float32, skip []bool, half int) {
	if res.kind != 2 {
		res.decodeVectors(r, books, vectors, skip, half)
		return
	}
	all := true
	for _, s := range skip {
		all = all && s
	}
	if all {
		return
	}
	channels := len(vectors)
	big := res.interleaved[:half*channels]
	clear(big)
	one := [1][]float32{big}
	none := [1]bool{}
	res.decodeVectors(r, books, one[:], none[:], half*channels)
	for c, v := range vectors {
		for i := range v[:half] {
			v[i] = big[i*channels+c]
		}
	}
}

func (res *residue) decodeVectors(r *bitReader, books []*codebook, vectors [][]float32, skip []bool, size int) {
	begin := min(res.begin, size)
	end := min(res.end, size)
	if end <= begin {
		return
	}
	classbook := books[res.classbook]
	perWord := classbook.dims
	partitions := (end - begin) / res.partitionSize
	for pass := range 8 {
		for p := 0; p < partitions; {
			if pass == 0 {
				for j := range vectors {
					if skip[j] {
						continue
					}
					temp := classbook.decode(r)
					if temp < 0 {
						return
					}
					classes := res.classes[j]
					for i := perWord - 1; i >= 0; i-- {
						classes[i+p] = temp % res.classifications
						temp /= res.classifications
					}
				}
			}
			for i := 0; i < perWord && p < partitions; i++ {
				for j, v := range vectors {
					if skip[j] {
						continue
					}
					b := res.books[res.classes[j][p]][pass]
					if b < 0 {
						continue
					}
					book := books[b]
					at := begin + p*res.partitionSize
					if !res.decodePartition(r, book, v[at:at+res.partitionSize]) {
						return
					}
				}
				p++
			}
		}
	}
}

func (res *residue) decodePartition(r *bitReader, book *codebook, v []float32) bool {
	if res.kind == 0 {
		step := len(v) / book.dims
		for i := range step {
			e := book.decode(r)
			if e < 0 {
				return false
			}
			for j, x := range book.vector(e) {
				v[i+j*step] += x
			}
		}
		return true
	}
	for i := 0; i < len(v); {
		e := book.decode(r)
		if e < 0 {
			return false
		}
		for _, x := range book.vector(e) {
			if i == len(v) {
				break
			}
			v[i] += x
			i++
		}
	}
	return true
}
