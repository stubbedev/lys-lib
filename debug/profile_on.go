//go:build lysuidebug

package debug

import (
	"fmt"
	"os"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"
)

// profileEnv is LYS_UI_CPUPROFILE=path:start-end: a debug build
// profiles its CPU from start to end seconds after launch and writes
// the profile to path, so a running application is profiled, past its
// startup, without code of its own.
const profileEnv = "LYS_UI_CPUPROFILE"

func init() {
	spec := os.Getenv(profileEnv)
	if spec == "" {
		return
	}
	if err := startProfile(spec); err != nil {
		fmt.Fprintf(os.Stderr, "lys-ui: %s=%q: %v\n", profileEnv, spec, err)
	}
}

func startProfile(spec string) error {
	path, window, ok := strings.Cut(spec, ":")
	from, to, ranged := strings.Cut(window, "-")
	if !ok || !ranged {
		return fmt.Errorf("want path:start-end")
	}
	start, err1 := strconv.ParseFloat(from, 64)
	end, err2 := strconv.ParseFloat(to, 64)
	if err1 != nil || err2 != nil || start < 0 || end <= start {
		return fmt.Errorf("window %q is not start-end seconds with start before end", window)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	seconds := func(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
	time.AfterFunc(seconds(start), func() {
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintf(os.Stderr, "lys-ui: starting the CPU profile %s: %v\n", path, err)
			_ = f.Close()
			return
		}
		time.AfterFunc(seconds(end-start), func() {
			pprof.StopCPUProfile()
			if err := f.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "lys-ui: closing the CPU profile %s: %v\n", path, err)
			}
		})
	})
	return nil
}
