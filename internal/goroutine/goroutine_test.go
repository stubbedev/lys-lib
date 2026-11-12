package goroutine_test

import (
	"testing"

	"github.com/stubbedev/lys-lib/internal/goroutine"
)

func TestIDIsStableOnAGoroutineAndDiffersAcrossThem(t *testing.T) {
	self := goroutine.ID()
	if again := goroutine.ID(); again != self {
		t.Fatalf("ID() = %d then %d on one goroutine", self, again)
	}
	other := make(chan uint64)
	go func() { other <- goroutine.ID() }()
	if id := <-other; id == self || id == 0 {
		t.Fatalf("another goroutine's ID() = %d, this one's %d", id, self)
	}
}
