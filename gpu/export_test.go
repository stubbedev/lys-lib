package gpu

import (
	"errors"
	"fmt"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/fourcc"
)

func survivesExec(fd int) error {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return fmt.Errorf("descriptor %d is not open: %w", fd, err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		return fmt.Errorf("descriptor %d survives an exec", fd)
	}
	return nil
}

// A target's dmabuf, the sync_file handed out and the one the device
// keeps to answer the next call with are all gone from a child.
func TestTargetAndSyncFileDescriptorsCloseOnExec(t *testing.T) {
	d := exportingDevice(t)
	mods, err := d.Modifiers(fourcc.ARGB8888)
	if err != nil || len(mods) == 0 {
		t.Fatalf("modifiers %v, %v", mods, err)
	}
	tg, err := d.NewTarget(16, 16, fourcc.ARGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	for i, p := range tg.Dmabuf().Planes {
		if err := survivesExec(p.FD); err != nil {
			t.Errorf("target plane %d: %v", i, err)
		}
	}
	if !d.SyncFiles() {
		t.Skipf("%s has no sync_file semaphores", d.Name())
	}
	clearPass(t, d, tg)
	fd := syncFile(t, d)
	if fd < 0 {
		t.Skipf("%s exports no sync_file for a pending pass", d.Name())
	}
	if err := survivesExec(fd); err != nil {
		t.Errorf("the sync_file handed out: %v", err)
	}
	if !d.lastSyncFileSet || d.lastSyncFile < 0 {
		t.Fatalf("the device keeps sync_file %d, set %v, after handing one out", d.lastSyncFile, d.lastSyncFileSet)
	}
	if err := survivesExec(d.lastSyncFile); err != nil {
		t.Errorf("the sync_file the device keeps: %v", err)
	}
}

// Every plane of an exported picture has a descriptor of its own, and
// none of them outlives an exec: a child the process starts holds no
// dmabuf it was never handed.
func TestExportedPlanesCloseOnExec(t *testing.T) {
	d := exportingDevice(t)
	mods, err := d.modifiers(fourcc.NV12, encodeUse)
	if err != nil {
		t.Fatal(err)
	}
	exported := 0
	for _, mod := range mods {
		e, err := d.export(330, 240, fourcc.NV12, []fourcc.Modifier{mod}, encodeUse)
		if errors.Is(err, errPlanesOverlap) {
			continue
		}
		if err != nil {
			t.Fatalf("modifier %s: %v", mod, err)
		}
		exported++
		seen := map[int]bool{}
		for i, p := range e.buf.Planes {
			if err := survivesExec(p.FD); err != nil {
				t.Errorf("modifier %s: plane %d: %v", mod, i, err)
			}
			if seen[p.FD] {
				t.Errorf("modifier %s: plane %d shares descriptor %d with a plane before it", mod, i, p.FD)
			}
			seen[p.FD] = true
		}
		e.close(d)
	}
	if exported == 0 {
		t.Skipf("%s exports no NV12 pictures", d.Name())
	}
}
