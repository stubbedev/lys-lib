package mux

import (
	"testing"

	"github.com/stubbedev/lys-lib/media/internal/initcost"
)

func TestImportingCostsNothing(t *testing.T) { initcost.Check(t) }
