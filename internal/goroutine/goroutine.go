// Package goroutine identifies the calling goroutine.
package goroutine

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
)

// ID is the calling goroutine's id, read from the header line of its
// own stack trace. It panics when the runtime's header has a shape it
// does not know.
func ID() uint64 {
	var buf [64]byte
	header := string(buf[:runtime.Stack(buf[:], false)])
	field, _, _ := strings.Cut(strings.TrimPrefix(header, "goroutine "), " ")
	id, err := strconv.ParseUint(field, 10, 64)
	if err != nil {
		panic(fmt.Sprintf("goroutine id from stack header %q: %v", header, err))
	}
	return id
}
