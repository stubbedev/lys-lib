package capture

import (
	"bytes"
	"math"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/wltest"
)

// fakeProcessEnv tells the test binary it runs as a fake compositor for
// another, serving the listener it inherits as fd 3.
const fakeProcessEnv = "LYS_CAPTURE_FAKE_PROCESS"

// startFakeCompositorProcess serves one client from a fake compositor in a
// process of its own, whose allocations the client's process does not count,
// with every capture landing.
func startFakeCompositorProcess(t *testing.T) {
	t.Helper()
	ln, _ := wltest.LoopbackDisplay(t, "wayland-capture-fake-process")
	file, err := ln.File()
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	_ = ln.Close()
	var out bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestFakeCompositorProcess$", "-test.v")
	cmd.Env = append(os.Environ(), fakeProcessEnv+"=1")
	cmd.ExtraFiles = []*os.File{file}
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	t.Cleanup(func() {
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		select {
		case err := <-exited:
			if err != nil {
				t.Errorf("the fake compositor process: %v\n%s", err, out.String())
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			t.Errorf("the fake compositor process outlived its client (%v)\n%s", <-exited, out.String())
		}
	})
}

func TestFakeCompositorProcess(t *testing.T) {
	if os.Getenv(fakeProcessEnv) == "" {
		t.Skip("runs only as the fake compositor of startFakeCompositorProcess")
	}
	ln, err := net.FileListener(os.NewFile(3, "fake-compositor"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	f := newFakeCompositor(t)
	f.live = math.MaxInt
	f.accept(ln.(*net.UnixListener))
}

// A steady stream frame costs the client nothing to allocate, from
// create_frame to the published frame, its events and delete_id included.
func TestASteadyStreamFrameOnTheWireAllocatesNothing(t *testing.T) {
	startFakeCompositorProcess(t)
	s, err := OpenOutputStream("FAKE-1", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	var seen uint64
	var f StreamFrame
	next := func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if s.Latest(&f) && f.Seq > seen {
				seen = f.Seq
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("no frame after %d in 5s: %v", seen, s.Err())
			}
			time.Sleep(50 * time.Microsecond)
		}
	}
	for range 4 * fakeDamageCycle {
		next()
	}
	if raceEnabled {
		t.Skip("the race detector drops pooled objects at random")
	}
	if n := testing.AllocsPerRun(200, next); n != 0 {
		t.Errorf("a steady stream frame allocated %v times", n)
	}
}
