package gpu

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/stubbedev/lys-lib/fourcc"
)

type preparedTarget struct {
	t    *Target
	next *preparedTarget
}

// ErrDeviceClosed reports a target prepared after its device's Close began.
var ErrDeviceClosed = errors.New("gpu: the device is closed")

// PrepareTarget is NewTarget from any goroutine, with the driver's allocation
// and the view and framebuffer a first draw would make done there. The target
// reaches the device's goroutine with no lock: use it there as any other. Close
// waits for the prepares running; one starting after it is ErrDeviceClosed.
func (d *Device) PrepareTarget(width, height int, format fourcc.Format, modifiers []fourcc.Modifier) (*Target, error) {
	if err := d.startPrepare(); err != nil {
		return nil, err
	}
	defer d.preparing.Done()
	if !slices.Contains(textureFormats, format) {
		return nil, fmt.Errorf("gpu: no target in DRM format %s (%v)", format, textureFormats)
	}
	t, err := d.exportedTarget(width, height, format, modifiers)
	if err != nil {
		return nil, err
	}
	return d.handOver(t)
}

// PrepareOffscreen is NewOffscreen from any goroutine, as PrepareTarget.
func (d *Device) PrepareOffscreen(width, height int) (*Target, error) {
	if err := d.startPrepare(); err != nil {
		return nil, err
	}
	defer d.preparing.Done()
	s, err := d.newSurface(width, height)
	if err != nil {
		return nil, err
	}
	return d.handOver(&Target{s: *s, offscreen: true, buf: dmabufOf(width, height)})
}

func (d *Device) handOver(t *Target) (*Target, error) {
	if t.s.vf == surfaceFormat {
		if err := t.s.attachments(d.pass); err != nil {
			t.destroy()
			return nil, err
		}
	}
	node := &preparedTarget{t: t}
	for {
		node.next = d.prepared.Load()
		if d.prepared.CompareAndSwap(node.next, node) {
			return t, nil
		}
	}
}

// adoptTargets registers what PrepareTarget finished, on the device's goroutine.
func (d *Device) adoptTargets() {
	if d.prepared.Load() == nil {
		return
	}
	for node := d.prepared.Swap(nil); node != nil; node = node.next {
		d.targets[node.t] = struct{}{}
	}
}

type preparedTargets struct {
	prepared atomic.Pointer[preparedTarget]
	// prepareMu guards closing and the start of a prepare; only prepares and
	// Close take it.
	prepareMu sync.Mutex
	closing   bool
	preparing sync.WaitGroup
}

func (d *Device) startPrepare() error {
	d.prepareMu.Lock()
	defer d.prepareMu.Unlock()
	if d.closing {
		return ErrDeviceClosed
	}
	d.preparing.Add(1)
	return nil
}

// stopPrepares makes later prepares fail and waits for those running, whose
// targets Close then takes with the rest.
func (d *Device) stopPrepares() {
	d.prepareMu.Lock()
	d.closing = true
	d.prepareMu.Unlock()
	d.preparing.Wait()
	d.adoptTargets()
}
