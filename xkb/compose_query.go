package xkb

import (
	"iter"
	"slices"
)

const invalidComposeLocale = 679

// ComposeEntry is one compose sequence of a table.
type ComposeEntry struct {
	Sequence []Keysym
	Sym      Keysym
	UTF8     string
}

// Entries yields every sequence of the table, ordered by keysym at each step
// of the sequence. An entry's Sequence is reused between steps: clone it to
// keep it.
func (t *ComposeTable) Entries() iter.Seq[ComposeEntry] {
	return func(yield func(ComposeEntry) bool) {
		walkCompose(t.root, nil, yield)
	}
}

func walkCompose(n *composeNode, seq []Keysym, yield func(ComposeEntry) bool) bool {
	syms := make([]Keysym, 0, len(n.children))
	for sym := range n.children {
		syms = append(syms, sym)
	}
	slices.Sort(syms)
	for _, sym := range syms {
		child := n.children[sym]
		next := append(seq, sym)
		if child.leaf {
			if !yield(ComposeEntry{Sequence: next, Sym: child.sym, UTF8: child.text}) {
				return false
			}
			continue
		}
		if !walkCompose(child, next, yield) {
			return false
		}
	}
	return true
}

// Table is the table the state runs on.
func (s *ComposeState) Table() *ComposeTable { return s.table }
