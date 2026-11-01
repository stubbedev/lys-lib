package gpu

import (
	"fmt"

	"github.com/stubbedev/lys-lib/third_party/vk"
)

// maxFrameBuffers bounds the segments of frames recorded or in flight:
// the one recording, and those of the two frames a window can have
// ahead of the GPU at two segments each. A device that outruns the GPU
// waits for the oldest rather than queueing frames without end.
const maxFrameBuffers = 5

// frameBuffers is the memory one recorded segment of a frame reads on
// the GPU, which belongs to that segment until its submission finishes.
type frameBuffers struct {
	prims, words, staging *buffer
	set                   vk.DescriptorSet
	// staged is how many staging bytes the segment filled.
	staged int
}

// newFrameBuffers allocates a segment's buffers at their smallest.
func (d *Device) newFrameBuffers() (*frameBuffers, error) {
	set, err := d.allocateSet(d.primLayout)
	if err != nil {
		return nil, err
	}
	fb := &frameBuffers{set: set}
	if fb.prims, err = d.newBuffer(stagingMin, vk.BufferUsageStorageBufferBit); err == nil {
		if fb.words, err = d.newBuffer(stagingMin, vk.BufferUsageStorageBufferBit); err == nil {
			fb.staging, err = d.newBuffer(stagingMin, vk.BufferUsageTransferSrcBit)
		}
	}
	if err != nil {
		fb.close(d)
		return nil, fmt.Errorf("gpu: a frame segment's buffers: %w", err)
	}
	d.bindPrims(fb)
	d.frameBufferCount++
	return fb, nil
}

func (fb *frameBuffers) close(d *Device) {
	for _, b := range [3]*buffer{fb.prims, fb.words, fb.staging} {
		if b != nil {
			b.close()
		}
	}
	d.cmds.FreeDescriptorSets(d.device, d.descPool, 1, &fb.set)
}

// takeFrameBuffers makes a segment's buffers current: its own after a
// failed frame, a finished segment's, new ones while the bound allows,
// or else the oldest submission's once it finishes.
func (d *Device) takeFrameBuffers() error {
	if d.current != nil {
		return nil
	}
	d.reap(false)
	for len(d.spareFrameBuffers) == 0 && d.frameBufferCount >= maxFrameBuffers {
		if err := d.waitOldest(); err != nil {
			return err
		}
	}
	if n := len(d.spareFrameBuffers); n > 0 {
		fb := d.spareFrameBuffers[n-1]
		d.spareFrameBuffers = d.spareFrameBuffers[:n-1]
		if fb.staging == nil {
			staging, err := d.newBuffer(stagingMin, vk.BufferUsageTransferSrcBit)
			if err != nil {
				d.spareFrameBuffers = append(d.spareFrameBuffers, fb)
				return fmt.Errorf("gpu: staging buffer of %d bytes: %w", stagingMin, err)
			}
			fb.staging = staging
		}
		d.current = fb
		return nil
	}
	fb, err := d.newFrameBuffers()
	if err != nil {
		return err
	}
	d.current = fb
	return nil
}

// retireFrameBuffers takes back a finished segment's buffers: one large
// upload (a wallpaper, a 4K sprite) must not keep its staging buffer for
// good, while a steady stream of them keeps one. It drops a buffer more
// than four times what the segment needed, and the next use of these
// buffers allocates it again at its smallest.
func (d *Device) retireFrameBuffers(fb *frameBuffers) {
	if keep := max(2*fb.staged, stagingMin); len(fb.staging.mapped) > 2*keep {
		fb.staging.close()
		fb.staging = nil
	}
	d.spareFrameBuffers = append(d.spareFrameBuffers, fb)
}

// closeFrameBuffers frees every spare segment's buffers; nothing holds
// them, and the next frame allocates what it needs.
func (d *Device) closeFrameBuffers() {
	for _, fb := range d.spareFrameBuffers {
		fb.close(d)
		d.frameBufferCount--
	}
	d.spareFrameBuffers = nil
}
