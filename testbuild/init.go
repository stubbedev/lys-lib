package testbuild

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

const initTraceTimeout = time.Minute

// RequireNoInit fails the test of a package that runs code when it is
// initialised: a package-level variable the compiler cannot lay out
// statically (one computed by a call, a map, a slice of another
// variable) or an init function, in the package or the test files that
// share its name. A program that imports the package pays for that
// before main, whether or not it ever uses what was built. The check
// reads the runtime's init trace of the test binary, which lists only
// the packages that have such code. It names the package by its
// caller, so a test calls it directly:
//
//	func TestImportingCostsNothing(t *testing.T) { testbuild.RequireNoInit(t) }
func RequireNoInit(t *testing.T) {
	t.Helper()
	if !AllocBudgetsHold || testing.CoverMode() != "" {
		t.Skip("lysdebug, -race and -cover add initialisation of their own")
	}
	pc, _, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("testbuild: no caller to name the package under test")
	}
	pkg := packageOf(runtime.FuncForPC(pc).Name())
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("testbuild: find the test binary: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), initTraceTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^$") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), "GODEBUG=inittrace=1")
	var trace bytes.Buffer
	cmd.Stderr = &trace
	if err := cmd.Run(); err != nil {
		t.Fatalf("testbuild: run %s for its init trace: %v\n%s", exe, err, trace.Bytes())
	}
	line, err := initOf(trace.Bytes(), pkg)
	if err != nil {
		t.Fatalf("testbuild: %s: %v", exe, err)
	}
	if line != "" {
		t.Errorf("importing %s runs its initialisation (%s), want none: build what it computes on first use", pkg, line)
	}
}

// packageOf is the import path of the package under test, given the
// name of a function of its tests: the package itself for a test in
// package p_test, whose own initialisation no importer pays for.
// A dot in the path's last element is written %2e in a function's name.
func packageOf(function string) string {
	slash := strings.LastIndexByte(function, '/')
	pkg := function[:slash+1+strings.IndexByte(function[slash+1:], '.')]
	return strings.TrimSuffix(strings.ReplaceAll(pkg, "%2e", "."), "_test")
}

// initOf is the line of an init trace that says pkg ran initialisation,
// "" when it ran none; a trace that lists no package at all is not one.
func initOf(trace []byte, pkg string) (string, error) {
	traced := false
	for line := range strings.SplitSeq(string(trace), "\n") {
		rest, isInit := strings.CutPrefix(line, "init ")
		name, _, timed := strings.Cut(rest, " @")
		if !isInit || !timed {
			continue
		}
		traced = true
		if name == pkg {
			return line, nil
		}
	}
	if !traced {
		return "", &noInitTraceError{output: string(trace)}
	}
	return "", nil
}

type noInitTraceError struct{ output string }

func (e *noInitTraceError) Error() string {
	return "the binary printed no init trace:\n" + e.output
}
