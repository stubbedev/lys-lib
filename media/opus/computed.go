package opus

import (
	"math"
	"sync"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// tables are what the codec computes rather than carries: the MDCT's
// overlap window, the transforms per LM (2.5 ms << LM long blocks; LM
// 0 is also every short block's) and the pulse codebook sizes.
type tables struct {
	window [overlap]float32
	mdcts  [frameLM + 1]*mdct
	pvqU   pvqTable
}

var shared struct {
	once   sync.Once
	tables *tables
}

// sharedTables builds the tables on the first encoder's or decoder's
// behalf, so a program that links the package and codes no Opus pays
// nothing.
func sharedTables() *tables {
	shared.once.Do(func() { shared.tables = newTables() })
	return shared.tables
}

func newTables() *tables {
	t := &tables{}
	for i := range t.window {
		s := gomath.Sin(0.5 * math.Pi * (float64(i) + 0.5) / overlap)
		t.window[i] = float32(gomath.Sin(0.5 * math.Pi * s * s))
	}
	t.mdcts = [...]*mdct{
		newMDCT(&t.window, 2*shortMdct, 4, 3, 5),
		newMDCT(&t.window, 4*shortMdct, 4, 2, 3, 5),
		newMDCT(&t.window, 8*shortMdct, 4, 4, 3, 5),
		newMDCT(&t.window, 2*FrameSize, 4, 2, 4, 3, 5),
	}
	t.pvqU.fill()
	return t
}

func (t *tables) shortMDCT() *mdct { return t.mdcts[0] }

func (t *tables) longMDCT() *mdct { return t.mdcts[frameLM] }
