package render

// The text caches: a process-wide LRU for shaping results, keyed by
// (font, pixel size, string), and the machinery both it and the glyph
// atlas share. Shaping is the most expensive text operation -
// itemization plus glyph positioning, allocating as it goes - and
// widgets re-shape the same string many times per frame (Entry's paint
// alone shapes three times). Text is immutable per key, so entries are
// never invalidated, only evicted: a string that edits reshapes under a
// new key and the stale one ages out.

import (
	"container/list"
	"sync"
	"unicode/utf8"

	"github.com/stubbedev/gelm/internal/text"
)

// shapeKey identifies one shaped line. The font field compares by
// pointer identity - a *Typeface or a *Chain - so a chain held by the
// app reuses its entries across frames and two chains over the same
// faces stay separate keys; neither deep face equality nor per-chain
// rune picks leak into the key. The direction is part of the key: the
// same text under an explicit left-to-right vs right-to-left base
// resolves and reorders differently.
type shapeKey struct {
	font Font
	px   float64
	text string
	dir  text.Direction
}

// Cache budgets. Shaped lines are small (a few hundred bytes each) and
// rasterized glyphs are 8bpp masks, so tens of megabytes bound the
// worst text-heavy window with room to spare.
const (
	shapeCacheBudget = 16 << 20 // shaped lines
	glyphCacheBudget = 8 << 20  // glyph masks and scaled bitmap strikes
	// runeShapeCacheBudget bounds the rune-keyed cache the per-rune
	// probes share. Entries cost exactly what a one-rune shape costs
	// (tens of bytes plus its glyphs), and a text-heavy window touches
	// a few hundred distinct runes, so a megabyte is far above use and
	// the LRU covers the rest.
	runeShapeCacheBudget = 1 << 20
	// subpixelBuckets quantizes a glyph's fractional pen offset: four
	// buckets per axis bound the cache blowup a fractional device
	// scale (150/120) would otherwise cause while keeping positional
	// error at a quarter pixel, under the AA ramp's own resolution.
	subpixelBuckets = 4
	// shapeOverhead is the per-entry cost model constant: ShapedText
	// itself, the run slice, and map bookkeeping.
	shapeOverhead = 128
	// glyphBytesPerRun is the per-glyph cost of a shaping.Glyph: nine
	// fixed.Int26_6 fields plus cluster, counts, and ID.
	glyphBytesPerRun = 96
)

// shapeCache is the process-wide shaping cache. Hit from widgets on the
// loop goroutine today, mutex-guarded regardless; the fill work (the
// shaper itself) runs outside the lock, so concurrent misses on one
// Typeface remain as unsafe as ever - the same contract Typeface has
// always had.
var shapes = newLRU[shapeKey, *ShapedText](shapeCacheBudget)

// cachedShape returns the shape of text at px for font under base
// direction d, filling the cache on a miss. The returned pointer is
// shared: ShapedText is immutable once built, so every caller may keep
// and read it freely.
func cachedShape(font Font, px float64, s string, d text.Direction, fill func() *ShapedText) *ShapedText {
	key := shapeKey{font: font, px: px, text: s, dir: d}
	if sh, ok := shapes.get(key); ok {
		return sh
	}
	sh := fill()
	shapes.put(key, sh, shapedCost(sh))
	return sh
}

// runeKey identifies one shaped rune. The same font identity and size
// semantics as shapeKey, minus the string: a probe's cost was the
// one-rune string each lookup built, so the key stores the rune.
type runeKey struct {
	font Font
	px   float64
	r    rune
}

// runeShapes is the rune-keyed half of the shaping cache, serving the
// per-rune probes (Font.ShapeRune). Same LRU, same cost model.
var runeShapes = newLRU[runeKey, *ShapedText](runeShapeCacheBudget)

// cachedShapeRune returns the shape of one rune at px for font,
// filling the cache on a miss; the fill must produce what Shape of the
// one-rune string would, so both caches agree entry for entry.
func cachedShapeRune(font Font, px float64, r rune, fill func() *ShapedText) *ShapedText {
	key := runeKey{font: font, px: px, r: r}
	if sh, ok := runeShapes.get(key); ok {
		return sh
	}
	sh := fill()
	runeShapes.put(key, sh, shapedCost(sh))
	return sh
}

// shapedCost estimates an entry's memory: the string, the caret table,
// and one row per shaped glyph.
func shapedCost(s *ShapedText) int {
	glyphs := 0
	for i := range s.runs {
		glyphs += len(s.runs[i].out.Glyphs)
	}
	return shapeOverhead + len(s.text) + glyphBytesPerRun*glyphs +
		8*(utf8.RuneCountInString(s.text)+1)
}

// lru is a mutex-guarded LRU map with a byte budget. Entries move to
// the front on hit; eviction walks the back when a put pushes the total
// over budget, always keeping the freshly inserted entry. K must not
// retain more than the caller intends; V's cost is computed by callers
// and passed to put.
type lru[K comparable, V any] struct {
	mu      sync.Mutex
	entries map[K]*list.Element
	order   *list.List // front = most recently used
	live    int
	budget  int
}

type lruEntry[K comparable, V any] struct {
	key  K
	val  V
	cost int
}

func newLRU[K comparable, V any](budget int) *lru[K, V] {
	return &lru[K, V]{
		entries: make(map[K]*list.Element),
		order:   list.New(),
		budget:  budget,
	}
}

// get returns the cached value, marking it most recently used.
func (c *lru[K, V]) get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		if e, ok := el.Value.(*lruEntry[K, V]); ok {
			return e.val, true
		}
	}
	var zero V
	return zero, false
}

// put inserts or refreshes an entry and evicts least-recently-used
// entries while the total cost exceeds the budget. The new entry always
// survives: a cache that evicts what it was just asked for is useless.
func (c *lru[K, V]) put(key K, val V, cost int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		if e, ok := el.Value.(*lruEntry[K, V]); ok {
			c.live += cost - e.cost
			e.val, e.cost = val, cost
		}
	} else {
		c.entries[key] = c.order.PushFront(&lruEntry[K, V]{key: key, val: val, cost: cost})
		c.live += cost
	}
	for c.live > c.budget && c.order.Len() > 1 {
		back := c.order.Back()
		if e, ok := back.Value.(*lruEntry[K, V]); ok {
			c.order.Remove(back)
			delete(c.entries, e.key)
			c.live -= e.cost
		}
	}
}

// len returns the number of cached entries.
func (c *lru[K, V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// dropAll empties the cache. Tests use it to pin miss-path costs and
// to keep caches fixture-local; no production caller evicts wholesale
// (the budget LRU is the only production eviction).
func (c *lru[K, V]) dropAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[K]*list.Element)
	c.order.Init()
	c.live = 0
}
