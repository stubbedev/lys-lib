package aac

import "github.com/stubbedev/lys-lib/intconv"

// huffEntry is one slot of a two-level Huffman lookup: a symbol and
// its code length, or (sub > 0) the offset and width of the second
// level for codes longer than the root. A zero entry is no code.
type huffEntry struct {
	sym uint16
	len uint8
	sub uint8
}

// huffman decodes one of the standard's codebooks by table lookup.
type huffman struct {
	table []huffEntry
	root  uint
}

// newHuffman builds the lookup for codes (right-aligned, lens bits
// long), symbol i being codes[i], with root bits of first level.
func newHuffman[C uint16 | uint32](codes []C, lens []uint8, root uint) *huffman {
	h := &huffman{root: root, table: make([]huffEntry, 1<<root)}
	subBits := map[uint32]uint{}
	for i, l := range lens {
		if uint(l) > root {
			p := uint32(codes[i]) >> (uint(l) - root)
			subBits[p] = max(subBits[p], uint(l)-root)
		}
	}
	for p := range uint32(1) << root {
		b, ok := subBits[p]
		if !ok {
			continue
		}
		h.table[p] = huffEntry{sym: intconv.Must[uint16](len(h.table)), sub: intconv.Must[uint8](b)}
		h.table = append(h.table, make([]huffEntry, 1<<b)...)
	}
	for i, l := range lens {
		code, n := uint32(codes[i]), uint(l)
		if n <= root {
			first := code << (root - n)
			for j := range uint32(1) << (root - n) {
				h.table[first+j] = huffEntry{sym: intconv.Must[uint16](i), len: l}
			}
			continue
		}
		e := h.table[code>>(n-root)]
		rest := n - root
		first := uint32(e.sym) + (code&(1<<rest-1))<<(uint(e.sub)-rest)
		for j := range uint32(1) << (uint(e.sub) - rest) {
			h.table[first+j] = huffEntry{sym: intconv.Must[uint16](i), len: intconv.Must[uint8](rest)}
		}
	}
	return h
}

// decode reads one symbol, -1 for a bit pattern that is no code.
func (h *huffman) decode(r *bitReader) int32 {
	e := h.table[r.peek(h.root)]
	if e.sub != 0 {
		r.skip(h.root)
		e = h.table[uint32(e.sym)+r.peek(uint(e.sub))]
	}
	if e.len == 0 {
		return -1
	}
	r.skip(uint(e.len))
	return int32(e.sym)
}
