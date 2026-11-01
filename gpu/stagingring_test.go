package gpu

import (
	"math/rand/v2"
	"testing"
)

func TestStagingRingNeverHandsOutHeldBytes(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	r := stagingRing{size: 1024}
	type upload struct{ at, end int }
	var held []upload
	overlaps := func(a, b upload) bool { return a.at < b.end && b.at < a.end }
	for range 200_000 {
		if len(held) > 0 && rng.IntN(2) == 0 {
			r.release(held[0].end)
			held = held[1:]
			continue
		}
		n := 1 + rng.IntN(r.size)
		at, ok := r.reserve(n)
		if !ok {
			if len(held) == 0 {
				t.Fatalf("%d bytes refused with nothing held", n)
			}
			continue
		}
		u := upload{at, at + n}
		if u.end > r.size || at%stagingAlign != 0 {
			t.Fatalf("reserved [%d, %d) in %d bytes", u.at, u.end, r.size)
		}
		for _, h := range held {
			if overlaps(u, h) {
				t.Fatalf("reserved [%d, %d) over the held [%d, %d)", u.at, u.end, h.at, h.end)
			}
		}
		r.pending++
		held = append(held, u)
	}
}
