package opus

import (
	"testing"

	"github.com/stubbedev/lys-lib/media/internal/initcost"
)

func TestImportingCostsNothing(t *testing.T) { initcost.Check(t) }

func TestCodersShareTheTables(t *testing.T) {
	e, err := NewEncoder(2, 96000, CBR)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDecoder(e.AppendHead(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, when := range []string{"a new decoder", "a reset decoder"} {
		celt := d.streams[0].celt
		if e.tables == nil || celt.tables != e.tables {
			t.Errorf("%s holds the tables %p, the encoder %p, want the one set built once", when, celt.tables, e.tables)
		}
		if e.pvq.u != &e.tables.pvqU || celt.bands.pvq.u != &e.tables.pvqU {
			t.Errorf("%s codes pulses by the table %p, the encoder by %p, want %p", when, celt.bands.pvq.u, e.pvq.u, &e.tables.pvqU)
		}
		d.Reset()
	}
}
