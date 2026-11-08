// Package testbuild reports how the test binary was built.
package testbuild

import (
	"testing"

	"github.com/stubbedev/lys-lib/debug"
)

// AllocBudgetsHold reports a build whose instrumentation does not allocate:
// neither lysdebug, whose traces allocate, nor -race.
const AllocBudgetsHold = !debug.Enabled && !Race

// SkipAllocBudget skips an allocation budget in a build whose instrumentation allocates.
func SkipAllocBudget(t testing.TB) {
	t.Helper()
	if !AllocBudgetsHold {
		t.Skip("allocation budgets hold for the default build, not lysdebug or -race, whose instrumentation allocates")
	}
}
