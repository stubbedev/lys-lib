package gpu

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/drm"
)

const (
	jobTimeoutShare   = 10
	slowJobShare      = 8
	unknownJobTimeout = 2 * time.Second
	slowJobMaxUnits   = 1 << 16
)

type slowJob struct {
	units   int
	limit   time.Duration
	timeout time.Duration
}

func jobTimeout(t *testing.T, d *Device) time.Duration {
	t.Helper()
	node, ok := d.RenderNode()
	if !ok {
		t.Logf("%s has no render node: assuming a %v job timeout", d.Name(), unknownJobTimeout)
		return unknownJobTimeout
	}
	pattern := filepath.Join("/sys/class/drm", filepath.Base(node), "device/tile*/gt*/engines/*/job_timeout_ms")
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("globbing %s: %v", pattern, err)
	}
	if len(files) == 0 {
		t.Logf("%s states no job timeout at %s: assuming %v", d.Name(), pattern, unknownJobTimeout)
		return unknownJobTimeout
	}
	var shortest time.Duration
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading the job timeout: %v", err)
		}
		ms, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			t.Fatalf("the job timeout in %s: %v", f, err)
		}
		if timeout := time.Duration(ms) * time.Millisecond; shortest == 0 || timeout < shortest {
			shortest = timeout
		}
	}
	return shortest
}

func newSlowJob(t *testing.T, d *Device, record func(units int)) slowJob {
	t.Helper()
	timeout := jobTimeout(t, d)
	limit := timeout / jobTimeoutShare
	target := limit / slowJobShare
	timed := func(units int) time.Duration {
		t.Helper()
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
		start := monotonic()
		record(units)
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
		return monotonic() - start
	}
	timed(1)
	for units := 1; ; units *= 4 {
		took := timed(units)
		if took > limit {
			t.Fatalf("%d units of a slow job ran %v on %s, over its %v limit", units, took, d.Name(), limit)
		}
		if took >= target/slowJobShare {
			units = max(1, int(int64(units)*int64(target)/int64(took)))
			t.Logf("a slow job of %d units, sized to run %v of its %v limit on %s", units, target, limit, d.Name())
			return slowJob{units: units, limit: limit, timeout: timeout}
		}
		if units >= slowJobMaxUnits {
			t.Fatalf("%d units of a slow job ran only %v on %s: its work does not grow with its units", units, took, d.Name())
		}
	}
}

func (j slowJob) ran(t *testing.T, start, end time.Duration) {
	t.Helper()
	if took := end - start; took > j.limit {
		t.Fatalf("a slow job of %d units ran %v, over its %v limit, 1/%d of the driver's %v job timeout", j.units, took, j.limit, jobTimeoutShare, j.timeout)
	}
}

func (j slowJob) endsBy(t *testing.T, start time.Duration, fence int) {
	t.Helper()
	if fence < 0 {
		j.ran(t, start, monotonic())
		return
	}
	fd, err := unix.FcntlInt(uintptr(fence), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("duplicating the slow job's fence %d: %v", fence, err)
	}
	t.Cleanup(func() {
		defer unix.Close(fd)
		j.ran(t, start, signalTime(t, fd))
	})
}

func signalTime(t *testing.T, fd int) time.Duration {
	t.Helper()
	waitReadable(t, fd)
	at, ok, err := drm.SyncFileSignalTime(fd)
	if err != nil || !ok {
		t.Fatalf("the signal time of sync_file %d: %v, %v", fd, ok, err)
	}
	return at
}
