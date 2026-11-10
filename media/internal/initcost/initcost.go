// Package initcost pins what importing a package costs a program that
// never uses it.
package initcost

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/testbuild"
)

const reexecTimeout = time.Minute

// Check fails the test of a package that runs code when it is
// initialised: a package-level variable the compiler cannot lay out
// statically (one computed by a call, a map, a slice of another
// variable) or an init function, in the package or its test files. It
// reads the runtime's init trace of the test binary, which lists only
// the packages that have such code.
func Check(t *testing.T) {
	t.Helper()
	if !testbuild.AllocBudgetsHold || testing.CoverMode() != "" {
		t.Skip("lysdebug, -race and -cover add initialisation of their own")
	}
	pc, _, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("initcost: no caller to name the package under test")
	}
	name := runtime.FuncForPC(pc).Name()
	slash := strings.LastIndexByte(name, '/')
	pkg := name[:slash+1+strings.IndexByte(name[slash+1:], '.')]
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("initcost: find the test binary: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), reexecTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^$") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), "GODEBUG=inittrace=1")
	var trace bytes.Buffer
	cmd.Stderr = &trace
	if err := cmd.Run(); err != nil {
		t.Fatalf("initcost: run %s for its init trace: %v\n%s", exe, err, trace.Bytes())
	}
	if !regexp.MustCompile(`(?m)^init \S+ @`).Match(trace.Bytes()) {
		t.Fatalf("initcost: %s printed no init trace:\n%s", exe, trace.Bytes())
	}
	line := regexp.MustCompile(`(?m)^init ` + regexp.QuoteMeta(pkg) + ` @.*$`).Find(trace.Bytes())
	if line != nil {
		t.Errorf("importing %s runs its initialisation (%s), want none: build what it computes on first use", pkg, line)
	}
}
