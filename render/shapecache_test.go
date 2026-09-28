package render

import (
	"strings"
	"sync"
	"testing"

	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
)

// dropShapes drains the process-wide shaping cache, so a test's keys
// cannot be served by another test's entries.
func dropShapes() { shapes.dropAll() }

func monoTypeface(t *testing.T) *Typeface {
	t.Helper()
	tf, err := LoadFont(gomono.TTF)
	if err != nil {
		t.Fatalf("load mono font: %v", err)
	}
	return tf
}

func TestShapeCacheReturnsIdenticalShapedText(t *testing.T) {
	dropShapes()
	tf := testTypeface(t)

	s1 := tf.Shape("hello world", 14)
	s2 := tf.Shape("hello world", 14)
	if s1 != s2 {
		t.Fatal("second Shape of the same (face, size, string) returned a different ShapedText")
	}
	for c := range 12 {
		if s1.CaretX(c) != s2.CaretX(c) {
			t.Errorf("CaretX(%d) differs between hits: %v vs %v", c, s1.CaretX(c), s2.CaretX(c))
		}
	}
	if s1.CaretPositions() != nil && &s1.CaretPositions()[0] != &s2.CaretPositions()[0] {
		t.Error("caret table is rebuilt per read instead of shared")
	}
	if s1.Advance() != s2.Advance() {
		t.Errorf("advance differs between hits: %v vs %v", s1.Advance(), s2.Advance())
	}
	if got := s2.CaretAt(s1.CaretX(3)); got != 3 {
		t.Errorf("CaretAt(CaretX(3)) = %d, want the hit path to round-trip", got)
	}

	// The chain entry point shares the cache.
	c := NewChain(tf)
	cs1 := c.Shape("hello world", 14)
	cs2 := c.Shape("hello world", 14)
	if cs1 != cs2 {
		t.Error("second chain Shape of the same input returned a different ShapedText")
	}
}

func TestShapeCacheKeysOnExactString(t *testing.T) {
	dropShapes()
	tf := testTypeface(t)

	// An edit sequence: shape, extend, reshape. Every string gets its
	// own entry and no entry ever serves another's advances.
	ab := tf.Shape("ab", 14)
	abc := tf.Shape("abc", 14)
	abAgain := tf.Shape("ab", 14)
	if abc == ab {
		t.Fatal("different strings shared one cache entry")
	}
	if abAgain != ab {
		t.Error("reshaping the original string after an edit returned a stale entry")
	}
	if abc.Advance() <= ab.Advance() {
		t.Errorf("stale advances: %q advances %v, at most %q's %v", "abc", abc.Advance(), "ab", ab.Advance())
	}
	if abc.CaretX(3) <= abc.CaretX(2) || abc.CaretX(2) != ab.CaretX(2) {
		t.Error("the extended string's caret table disagrees with the prefix's")
	}

	// Size and face are part of the key too.
	if tf.Shape("hi", 14) == tf.Shape("hi", 28) {
		t.Error("sizes shared one cache entry")
	}
	other := monoTypeface(t)
	if tf.Shape("hi", 14) == other.Shape("hi", 14) {
		t.Error("distinct faces shared one cache entry")
	}

	// Chains key by chain identity: two chains over the same faces are
	// separate entries, one chain reuses its own.
	c1, c2 := NewChain(tf), NewChain(tf)
	first := c1.Shape("hi", 14)
	if first == c2.Shape("hi", 14) {
		t.Error("distinct chains shared one cache entry")
	}
	if first != c1.Shape("hi", 14) {
		t.Error("a chain did not reuse its own entry")
	}
}

func TestShapeCacheHitAllocatesNothing(t *testing.T) {
	dropShapes()
	tf := testTypeface(t)
	tf.Shape("steady state", 14) // warm

	hits := testing.AllocsPerRun(100, func() {
		tf.Shape("steady state", 14)
	})
	if hits != 0 {
		t.Errorf("warm Shape allocated %v times per op, want 0", hits)
	}
	s := tf.Shape("steady state", 14)
	reads := testing.AllocsPerRun(100, func() {
		s.CaretX(4)
		s.CaretAt(s.CaretX(4))
	})
	if reads != 0 {
		t.Errorf("warm CaretX/CaretAt allocated %v times per op, want 0", reads)
	}
}

func TestShapeRuneMatchesShapeOfString(t *testing.T) {
	// Font.ShapeRune is the allocation-free probe path for per-rune
	// advance math; it must produce exactly what Shape of the one-rune
	// string produces, for both Font implementations and every rune
	// class (ASCII, Latin-1, CJK, emoji fallback through the chain).
	dropShapes()
	tf := testTypeface(t)
	mono := monoTypeface(t)
	c := NewChain(mono, tf)
	runes := []rune{'a', 'W', 'é', '中', '😀', ' ', '\t'}
	for _, r := range runes {
		for _, f := range []Font{tf, mono, c} {
			want := f.Shape(string(r), 14)
			got := f.ShapeRune(r, 14)
			if got.Advance() != want.Advance() || got.LineHeight() != want.LineHeight() {
				t.Fatalf("ShapeRune(%q) disagrees with Shape for %T: advance %v vs %v", r, f, got.Advance(), want.Advance())
			}
			if got.CaretX(1) != want.CaretX(1) {
				t.Errorf("ShapeRune(%q) caret table disagrees with Shape for %T", r, f)
			}
			if f.ShapeRune(r, 14) != got {
				t.Errorf("warm ShapeRune(%q) for %T missed the rune cache", r, f)
			}
		}
	}
	// Size is part of the key, like Shape.
	if tf.ShapeRune('a', 14) == tf.ShapeRune('a', 28) {
		t.Error("sizes shared one rune-cache entry")
	}
	// And the probe stays allocation-free warm.
	tf.ShapeRune('a', 14) // warm
	if n := testing.AllocsPerRun(100, func() { tf.ShapeRune('a', 14) }); n != 0 {
		t.Errorf("warm ShapeRune allocated %v times per op, want 0", n)
	}
}

func TestLRUEvictionKeepsHotEntries(t *testing.T) {
	// The eviction discipline, pinned on a small cache: entries keep
	// their heat across hits, the budget pressure evicts the coldest
	// first, and the entry that triggered the put always survives.
	c := newLRU[int, int](100)
	c.put(1, 1, 30)
	c.put(2, 2, 30)
	c.put(3, 3, 30)
	for range 5 {
		if _, ok := c.get(1); !ok { // entry 1 goes hot
			t.Fatal("live entry vanished without eviction pressure")
		}
	}
	c.put(4, 4, 30) // 60 live + 30: the coldest entry (2) must go
	if _, ok := c.get(2); ok {
		t.Error("the coldest entry survived eviction")
	}
	for _, k := range []int{1, 3, 4} {
		if _, ok := c.get(k); !ok {
			t.Errorf("entry %d was evicted though it fit and stayed warm", k)
		}
	}
	c.put(5, 5, 1000) // alone bigger than the budget: it must survive
	if _, ok := c.get(5); !ok {
		t.Error("the freshly inserted entry was evicted")
	}
	if c.len() != 1 {
		t.Errorf("cache holds %d entries, want only the oversized one", c.len())
	}
}

func TestShapedCacheEvictionUnderProductionBudget(t *testing.T) {
	// The production cache evicts: stuff it with heavy shapes until
	// the 16 MiB budget forces eviction, touching the hot entry every
	// round, then check the hot entry survived and the cache is
	// bounded (fillers churn out despite their head start).
	dropShapes()
	tf := testTypeface(t)
	hot := tf.Shape("the hot line", 14)
	filler := strings.Repeat("filler text padding the cache ", 7) // ~8 KiB shaped
	firstLen := -1
	hotKept := true
	for range 3000 {
		tf.Shape(filler, 14)
		if _, ok := shapes.get(shapeKey{font: tf, px: 14, text: "the hot line"}); !ok {
			hotKept = false
		}
		if firstLen < 0 && shapes.len() < 3000 {
			firstLen = shapes.len() // eviction has begun
		}
	}
	if !hotKept {
		t.Error("the hot entry left the cache mid-churn")
	}
	if firstLen < 0 {
		t.Error("no eviction under budget pressure after 3000 heavy inserts")
	}
	if tf.Shape("the hot line", 14) != hot {
		t.Error("the repeatedly-hit entry was evicted while cold fillers churned")
	}
	dropShapes()
}

func TestShapeCacheConcurrentAccess(t *testing.T) {
	// The cache is process-wide and mutex-guarded; distinct fonts
	// shaping concurrently through it must not race. (One Typeface
	// from several goroutines is still unsupported, as documented.)
	dropShapes()
	fonts := make([]*Typeface, 4)
	for i := range fonts {
		tf, err := LoadFont(goregular.TTF)
		if err != nil {
			t.Fatalf("load font: %v", err)
		}
		fonts[i] = tf
	}
	var wg sync.WaitGroup
	for _, tf := range fonts {
		wg.Add(1)
		go func(tf *Typeface) {
			defer wg.Done()
			for j := range 50 {
				tf.Shape("concurrent shaping", 12+float64(j%3))
				tf.Shape("another line", 14)
			}
		}(tf)
	}
	wg.Wait()
	dropShapes()
}
