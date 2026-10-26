// Bidirectional resolution (UAX #9) for every text path in the toolkit
// (#68): mixed Hebrew/Arabic + Latin lines resolve into directional
// runs that the shaper lays out visually correct, and the editable
// widgets map caret motion through. This package is the single source
// of truth — render shapes the runs it returns here, and the widgets
// route their Direction through it, so no consumer forks reorder
// logic.
//
// Positions stay rune indexes. BidiRuns returns the runs in visual
// left-to-right order while the spans it carries stay logical, and
// VisualOrder flattens the runs into the caret boundary sequence arrow
// keys step through. The resolver runs the full algorithm (explicit
// formatting characters and bracket pairs included), and the reorder
// here is exact for natural text — parity runs are then level runs —
// and approximate where explicit embeddings nest same-parity levels,
// which no label-level consumer relies on.
//
// Resolution is cached by (text, direction) in the same spirit as the
// render shaping caches: entries are immutable and never invalidated,
// only evicted, so a label re-resolving per frame would be the only
// cost UAX #9 could add to the paint path.
package text

import (
	"container/list"
	"slices"
	"sync"

	tsbidi "github.com/go-text/typesetting/bidi"
	"golang.org/x/text/unicode/bidi"
)

// Direction names the base paragraph direction text resolves and lays
// out with.
type Direction uint8

const (
	// DirectionAuto resolves the base from the paragraph's first strong
	// character (UAX #9 rules P2-P3). It is the zero value, so existing
	// LTR content is unchanged by default.
	DirectionAuto Direction = iota
	// DirectionLTR forces a left-to-right base.
	DirectionLTR
	// DirectionRTL forces a right-to-left base.
	DirectionRTL
)

// tsDirection maps d onto the resolver's paragraph default.
func (d Direction) tsDirection() tsbidi.Direction {
	switch d {
	case DirectionRTL:
		return tsbidi.RightToLeft
	case DirectionLTR:
		return tsbidi.LeftToRight
	}
	return tsbidi.Neutral
}

// Run is one directional run of a resolved paragraph: the [Start, End)
// rune span, the embedding level UAX #9 resolved it to, and whether it
// lays out right to left (odd level). Runs come back in visual
// left-to-right order; the spans they carry stay logical.
type Run struct {
	Start, End int
	Level      int
	RTL        bool
}

// resolution is one cached UAX #9 result: the visual-order runs and the
// base direction the paragraph resolved to.
type resolution struct {
	runs []Run
	rtl  bool
}

// RTL reports whether s lays out right to left under d: DirectionRTL
// always, DirectionLTR never, DirectionAuto from the paragraph's first
// strong character. Neutrals-only text (digits, punctuation) resolves
// left to right, so a direction never flips content without a strong
// character asking for it. Start and end alignment mirror off this.
func RTL(s string, d Direction) bool {
	return resolve(s, d).rtl
}

// BidiRuns returns the directional runs of s under base direction d, in
// visual left-to-right order — the order the shaper lays pieces out
// in. Cached by (s, d): the second resolution of the same paragraph is
// a lookup, shared with every reader of the text.
func BidiRuns(s string, d Direction) []Run {
	return resolve(s, d).runs
}

// resolve computes or serves the resolution of s under d.
func resolve(s string, d Direction) resolution {
	key := bidiKey{s: s, d: d}
	if r, ok := bidiCache.get(key); ok {
		return r
	}
	r := resolveUncached(s, d)
	bidiCache.put(key, r, bidiCost(r, len(s)))
	return r
}

// resolveUncached runs UAX #9 over s. The resolver returns runs of
// constant level in logical order; L2 reorders them into visual order
// by reversing, from the highest level down to the lowest odd one,
// each contiguous span at that level or higher. Runs are single-level,
// so reversing their order is the character-level reversal's
// run-atomic equivalent — each RTL run's glyphs come back visually
// ordered from the shaper, once.
func resolveUncached(s string, d Direction) resolution {
	if s == "" {
		return resolution{}
	}
	var p tsbidi.Paragraph
	runs := p.SegmentString(s, d.tsDirection())
	r := resolution{runs: make([]Run, 0, runs.NumRuns())}
	for i := range runs.NumRuns() {
		run := runs.Run(i)
		r.runs = append(r.runs, Run{
			Start: run.Start,
			End:   run.End,
			Level: int(run.Level),
			RTL:   run.Level%2 != 0,
		})
	}
	reorder(r.runs)
	r.rtl = firstStrongRTL(s, d)
	return r
}

// reorder is rule L2 at run granularity, in place.
func reorder(runs []Run) {
	maxLvl := 0
	for _, r := range runs {
		maxLvl = max(maxLvl, r.Level)
	}
	for lvl := maxLvl; lvl >= 1; lvl-- {
		for i := 0; i < len(runs); {
			if runs[i].Level < lvl {
				i++
				continue
			}
			j := i + 1
			for j < len(runs) && runs[j].Level >= lvl {
				j++
			}
			slices.Reverse(runs[i:j])
			i = j
		}
	}
}

// firstStrongRTL reports the base direction d resolves to: forced
// directions answer directly, auto scans for the first strong class
// (x/text's UCD tables), defaulting left to right like rule P3.
func firstStrongRTL(s string, d Direction) bool {
	switch d {
	case DirectionRTL:
		return true
	case DirectionLTR:
		return false
	}
	for _, r := range s {
		p, _ := bidi.LookupRune(r)
		switch p.Class() {
		case bidi.R, bidi.AL:
			return true
		case bidi.L:
			return false
		}
	}
	return false
}

// bidiKey identifies one resolved paragraph.
type bidiKey struct {
	s string
	d Direction
}

// Cache budgets. A resolution costs one small struct per directional
// run over the paragraph's runes; a megabyte bounds the worst
// text-heavy window with room to spare.
const (
	bidiCacheBudget = 1 << 20
	bidiCostBase    = 64
	bidiCostPerRune = 4
)

// bidiCache is the process-wide resolution cache, hit on the loop
// goroutine and mutex-guarded regardless, like the render caches.
var bidiCache = &bidiLRU{entries: map[bidiKey]*list.Element{}, budget: bidiCacheBudget}

func bidiCost(r resolution, runes int) int {
	return bidiCostBase + bidiCostPerRune*runes + 24*len(r.runs)
}

// bidiLRU is a mutex-guarded LRU over resolutions with a byte budget,
// the internal/text counterpart of the render shaping cache: entries
// move to the front on hit, eviction walks the back, and the freshly
// inserted entry always survives.
type bidiLRU struct {
	mu      sync.Mutex
	entries map[bidiKey]*list.Element
	order   *list.List
	live    int
	budget  int
}

type bidiEntry struct {
	key  bidiKey
	val  resolution
	cost int
}

// get returns the cached resolution, marking it most recently used.
func (c *bidiLRU) get(key bidiKey) (resolution, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		if e, ok := el.Value.(*bidiEntry); ok {
			return e.val, true
		}
	}
	return resolution{}, false
}

// put inserts or refreshes an entry and evicts least-recently-used
// entries while the total cost exceeds the budget. The new entry
// always survives, like every cache in the toolkit.
func (c *bidiLRU) put(key bidiKey, val resolution, cost int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.order == nil {
		c.order = list.New()
	}
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		if e, ok := el.Value.(*bidiEntry); ok {
			c.live += cost - e.cost
			e.val, e.cost = val, cost
		}
		return
	}
	c.entries[key] = c.order.PushFront(&bidiEntry{key: key, val: val, cost: cost})
	c.live += cost
	for c.live > c.budget && c.order.Len() > 1 {
		back := c.order.Back()
		if e, ok := back.Value.(*bidiEntry); ok {
			c.order.Remove(back)
			delete(c.entries, e.key)
			c.live -= e.cost
		}
	}
}

// VisualOrder returns the caret boundaries of rs in visual left-to-right
// order under runs — the sequence the arrow keys step through. Elements
// are cluster-snapped rune indexes in [0, len(rs)], each appearing once,
// leftmost first; the logical order of the text never changes. Editing
// stays logical: callers move within this sequence and keep the rune
// indexes it hands back.
func VisualOrder(rs []rune, runs []Run) []int {
	starts := clusterStarts(rs)
	order := make([]int, 0, len(starts))
	emitted := make([]bool, len(starts))
	emit := func(k int) {
		if !emitted[k] {
			emitted[k] = true
			order = append(order, starts[k])
		}
	}
	for _, r := range runs {
		if r.End <= r.Start {
			continue
		}
		// Runs never split a grapheme cluster, but the clip keeps the
		// mapping total if a run boundary ever lands mid-cluster.
		k0 := clusterIndex(starts, r.Start)
		k1 := clusterIndex(starts, r.End-1)
		if r.RTL {
			for k := k1 + 1; k >= k0; k-- {
				emit(k)
			}
			continue
		}
		for k := k0; k <= k1+1; k++ {
			emit(k)
		}
	}
	for k := range starts {
		emit(k)
	}
	return order
}

// VisualStep returns the boundary one visual step from at: visual left
// for negative delta, visual right for positive, over the visual
// boundary order. at snaps to its cluster first, so a caret parked
// mid-cluster joins the sequence; the ends come back unchanged, so a
// caret at an edge stays put.
func VisualStep(rs []rune, order []int, at, delta int) int {
	i := orderAt(rs, order, at)
	if i < 0 {
		return at
	}
	step := max(min(delta, 1), -1)
	j := i + step
	if j < 0 || j >= len(order) {
		return order[i] // off the sequence: stay at the snapped boundary
	}
	return order[j]
}

// VisualWordStep returns the boundary one visual word away from at:
// over the shared word segmentation (words.go) walked in visual order,
// so ctrl+arrows land on the word edge the eye sees — word ends moving
// visually right, word starts moving visually left, gaps skipped
// whole, exactly the edges WordEnd and WordStart land on in logical
// order. Word classes come from rs; order carries the visual sequence.
// Like VisualStep the ends come back unchanged.
func VisualWordStep(rs []rune, order []int, at, delta int) int {
	i := orderAt(rs, order, at)
	if i < 0 {
		return at
	}
	starts := clusterStarts(rs)
	word := func(b int) bool { return IsWordRune(rs[starts[clusterIndex(starts, b)]]) }
	// A word ends at b when the cluster before it is a word and the
	// one at it is not (or b is the slice end); starts mirrors it.
	isEnd := func(b int) bool {
		return b > 0 && word(b-1) && (b == len(rs) || !word(b))
	}
	isStart := func(b int) bool {
		return b < len(rs) && word(b) && (b == 0 || !word(b-1))
	}
	step := max(min(delta, 1), -1)
	for j := i + step; j >= 0 && j < len(order); j += step {
		if b := order[j]; step > 0 && isEnd(b) || step < 0 && isStart(b) {
			return b
		}
	}
	return order[i] // no word edge in that direction: the snapped boundary
}

// orderAt returns the position of at's cluster in the visual order, -1
// when the order cannot place it.
func orderAt(rs []rune, order []int, at int) int {
	if len(order) == 0 {
		return -1
	}
	b := SnapCluster(rs, min(max(at, 0), len(rs)))
	for i, o := range order {
		if o == b {
			return i
		}
	}
	return -1
}
