// Package testbuild reports how the test binary was built.
package testbuild

import (
	"runtime"
	"strconv"
	"strings"
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

// GoroutineID is the calling goroutine's id, parsed from its stack header.
func GoroutineID() uint64 {
	buf := make([]byte, 64)
	n := runtime.Stack(buf, false)
	id, _, _ := strings.Cut(strings.TrimPrefix(string(buf[:n]), "goroutine "), " ")
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		panic(err)
	}
	return v
}
