//go:build !gelmdebug

package debug

// Enabled is false without the gelmdebug build tag.
const Enabled = false

// Log is compiled out: an empty body the compiler inlines away,
// discarding every argument expression at the call site.
func Log(string, string, ...any) {}
