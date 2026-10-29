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
	filter    categories
)

// start is the process start time, so trace lines carry monotonic
// offsets instead of wall-clock noise.
var start = time.Now()

// Log emits one trace line when the build has the lysdebug tag and
// LYS_DEBUG admits the category: "<offset>s <category>: <message>"
// on stderr.
func Log(category, format string, args ...any) {
	parseOnce.Do(func() { filter = parseCategories(os.Getenv("LYS_DEBUG")) })
	if !filter.admits(category) {
		return
	}
	fmt.Fprintf(os.Stderr, "%7.3fs %s: %s\n",
		time.Since(start).Seconds(), category, fmt.Sprintf(format, args...))
}

// categories is a parsed LYS_DEBUG: the named categories, or every
// category when none is named, less the excluded ones.
type categories struct {
	named    map[string]bool
	excluded map[string]bool
	all      bool
}

func parseCategories(spec string) categories {
	c := categories{named: map[string]bool{}, excluded: map[string]bool{}}
	for name := range strings.SplitSeq(spec, ",") {
		name = strings.TrimSpace(name)
		switch {
		case name == "":
		case name == "*":
			c.all = true
		case strings.HasPrefix(name, "-"):
			c.excluded[name[1:]] = true
		default:
			c.named[name] = true
		}
	}
	if len(c.named) == 0 {
		c.all = true
	}
	return c
}

func (c categories) admits(category string) bool {
	if c.excluded[category] {
		return false
	}
	return c.all || c.named[category]
}
