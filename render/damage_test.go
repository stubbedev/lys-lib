package render

import "testing"

func TestEmpty(t *testing.T) {
	t.Run("zero and negative extents are empty", func(t *testing.T) {
		for _, r := range []Rect{
			{W: 0, H: 5},
			{W: 5, H: 0},
			{W: -1, H: 5},
			{W: 5, H: -1},
		} {
			if !r.Empty() {
				t.Errorf("%v must be empty", r)
			}
		}
	})

	t.Run("a positive rect is not empty", func(t *testing.T) {
		if (Rect{X: 1, Y: 2, W: 3, H: 4}).Empty() {
			t.Error("positive rect reported empty")
		}
	})
}

func TestContains(t *testing.T) {
	r := Rect{X: 10, Y: 10, W: 5, H: 5}

	t.Run("top-left corner is inside, exclusive far edges", func(t *testing.T) {
		if !r.Contains(10, 10) {
			t.Error("(10,10) must be inside")
		}
		if r.Contains(15, 12) {
			t.Error("x == X+W must be outside")
		}
		if r.Contains(12, 15) {
			t.Error("y == Y+H must be outside")
		}
	})

	t.Run("outside points", func(t *testing.T) {
		if r.Contains(9, 10) || r.Contains(10, 9) {
			t.Error("below/left of origin must be outside")
		}
	})

	t.Run("empty rect contains nothing", func(t *testing.T) {
		if (Rect{X: 5, Y: 5, W: 0, H: 5}).Contains(5, 5) {
			t.Error("empty rect must contain nothing")
		}
	})
}

func TestIntersect(t *testing.T) {
	t.Run("partial overlap", func(t *testing.T) {
		got := (Rect{X: 0, Y: 0, W: 10, H: 10}).Intersect(Rect{X: 5, Y: 5, W: 10, H: 10})
		want := Rect{X: 5, Y: 5, W: 5, H: 5}
		if got != want {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("disjoint yields empty", func(t *testing.T) {
		got := (Rect{X: 0, Y: 0, W: 10, H: 10}).Intersect(Rect{X: 20, Y: 20, W: 5, H: 5})
		if !got.Empty() {
			t.Errorf("disjoint intersect must be empty, got %v", got)
		}
	})

	t.Run("edge touching yields empty", func(t *testing.T) {
		got := (Rect{X: 0, Y: 0, W: 10, H: 10}).Intersect(Rect{X: 10, Y: 0, W: 5, H: 5})
		if !got.Empty() {
			t.Errorf("edge-touching intersect must be empty, got %v", got)
		}
	})

	t.Run("empty operand yields empty", func(t *testing.T) {
		got := (Rect{X: 0, Y: 0, W: 10, H: 10}).Intersect(Rect{W: -1, H: 5})
		if !got.Empty() {
			t.Errorf("intersect with empty must be empty, got %v", got)
		}
	})
}

func TestUnion(t *testing.T) {
	t.Run("bounding box", func(t *testing.T) {
		got := (Rect{X: 5, Y: 5, W: 5, H: 5}).Union(Rect{X: 0, Y: 0, W: 3, H: 3})
		want := Rect{X: 0, Y: 0, W: 10, H: 10}
		if got != want {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("empty operand drops out", func(t *testing.T) {
		r := Rect{X: 1, Y: 1, W: 2, H: 2}
		if got := r.Union(Rect{}); got != r {
			t.Errorf("union with empty must be identity, got %v", got)
		}
		if got := (Rect{}).Union(r); got != r {
			t.Errorf("union of empty with r must be r, got %v", got)
		}
	})

	t.Run("two empties yield empty", func(t *testing.T) {
		if got := (Rect{W: -1, H: 0}).Union(Rect{}); !got.Empty() {
			t.Errorf("union of empties must be empty, got %v", got)
		}
	})
}

func area(r Rect) int {
	if r.Empty() {
		return 0
	}
	return r.W * r.H
}

func overlapsAny(r Rect, others []Rect) bool {
	for _, o := range others {
		if !r.Intersect(o).Empty() {
			return true
		}
	}
	return false
}

func TestSubtract(t *testing.T) {
	bounds := Rect{X: 0, Y: 0, W: 100, H: 50}

	t.Run("disjoint cut leaves r unchanged", func(t *testing.T) {
		r := Rect{X: 10, Y: 10, W: 20, H: 20}
		got := r.Subtract(Rect{X: 50, Y: 50, W: 5, H: 5})
		if len(got) != 1 || got[0] != r {
			t.Errorf("disjoint subtract must yield [r], got %v", got)
		}
	})

	t.Run("full cover yields nothing", func(t *testing.T) {
		got := bounds.Subtract(Rect{X: -10, Y: -10, W: 200, H: 100})
		if len(got) != 0 {
			t.Errorf("covered subtract must yield no rects, got %v", got)
		}
	})

	t.Run("centered hole yields four rects that tile the remainder", func(t *testing.T) {
		hole := Rect{X: 40, Y: 20, W: 20, H: 10}
		got := bounds.Subtract(hole)
		sum := 0
		for _, r := range got {
			if r.Empty() {
				t.Errorf("subtract produced an empty rect: %v", r)
			}
			if overlapsAny(r, hole.Subtract(Rect{})) {
				t.Errorf("rect %v still overlaps the hole", r)
			}
			sum += area(r)
		}
		if want := area(bounds) - area(hole); sum != want {
			t.Errorf("remainder area %d, want %d (rects: %v)", sum, want, got)
		}
		if len(got) != 4 {
			t.Errorf("centered hole must produce 4 rects, got %d: %v", len(got), got)
		}
	})

	t.Run("empty cut leaves r unchanged", func(t *testing.T) {
		r := Rect{X: 5, Y: 5, W: 5, H: 5}
		got := r.Subtract(Rect{W: 0, H: 0})
		if len(got) != 1 || got[0] != r {
			t.Errorf("empty cut must yield [r], got %v", got)
		}
	})
}

func TestUnionAll(t *testing.T) {
	t.Run("nil yields empty", func(t *testing.T) {
		if got := UnionAll(nil); !got.Empty() {
			t.Errorf("union of nil must be empty, got %v", got)
		}
	})

	t.Run("bounding box of many", func(t *testing.T) {
		got := UnionAll([]Rect{
			{X: 10, Y: 10, W: 5, H: 5},
			{X: 0, Y: 0, W: 2, H: 2},
			{X: 20, Y: 20, W: 5, H: 5},
		})
		want := Rect{X: 0, Y: 0, W: 25, H: 25}
		if got != want {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}
