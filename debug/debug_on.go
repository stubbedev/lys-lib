//go:build lysdebug

package debug

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Enabled is true in builds carrying the lysdebug tag.
const Enabled = true

// categories selects which trace lines pass at runtime, parsed once
// from LYS_DEBUG (comma-separated names, "*" or "" for all, "-name"
// excludes one).
var (
	parseOnce sync.Once
	cats      map[string]bool
	allCats   bool
)

// start is the process start time, so trace lines carry monotonic
// offsets instead of wall-clock noise.
var start = time.Now()

// Log emits one trace line when the build has the lysdebug tag and
// LYS_DEBUG admits the category: "<offset>s <category>: <message>"
// on stderr.
func Log(category, format string, args ...any) {
	parseOnce.Do(parseEnv)
	if !allCats && !cats[category] {
		return
	}
	fmt.Fprintf(os.Stderr, "%7.3fs %s: %s\n",
		time.Since(start).Seconds(), category, fmt.Sprintf(format, args...))
}

// parseEnv reads LYS_DEBUG once per process.
func parseEnv() {
	cats = make(map[string]bool)
	for _, name := range strings.Split(os.Getenv("LYS_DEBUG"), ",") {
		name = strings.TrimSpace(name)
		switch {
		case name == "":
		case name == "*":
			allCats = true
		case strings.HasPrefix(name, "-"):
			cats[name[1:]] = false
		default:
			cats[name] = true
		}
	}
}
