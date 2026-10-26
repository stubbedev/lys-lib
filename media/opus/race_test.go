//go:build race

package opus

// raceEnabled reports a -race build, whose instrumentation allocates:
// allocation budgets skip under it.
const raceEnabled = true
