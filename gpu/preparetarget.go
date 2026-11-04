package gpu

import (
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/stubbedev/lys-lib/fourcc"
)

type preparedTarget struct {
	t    *Target
	next *preparedTarget
}

// PrepareTarget is NewTarget from any goroutine, with the driver's allocation
// and the view and framebuffer a first draw would make done there. The target
// reaches the device's goroutine with no lock: use it there as any other. No
// PrepareTarget may overlap the device's Close.
func (d *Device) PrepareTarget(width, height int, format fourcc.Format, modifiers []fourcc.Modifier) (*Target, error) {
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
}
