package gpu

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// Readback is host-visible memory a target is copied into for the CPU to
// read, without stalling whoever draws: Copy only queues the copy, and
// Wait, on any goroutine, blocks until it is done. It needs neither a
// dmabuf nor a sync_file, so it works on every device. Close it when
// done; the device closes the ones still open with it.
type Readback struct {
	d      *Device
	buf    *buffer
	fence  vk.Fence
	w, h   int
	copied bool
	closed bool
}

// NewReadback allocates memory for copies of a width x height target.
func (d *Device) NewReadback(width, height int) (*Readback, error) {
	if err := d.checkSize(width, height); err != nil {
		return nil, err
	}
	buf, err := d.newReadBuffer(width*height*4, vk.BufferUsageTransferDstBit, nil)
	if err != nil {
		return nil, fmt.Errorf("gpu: a %dx%d readback: %w", width, height, err)
	}
	fence, err := d.fence()
	if err != nil {
		buf.close()
		return nil, err
	}
	r := &Readback{d: d, buf: buf, fence: fence, w: width, h: height}
	d.readbacks[r] = struct{}{}
	return r, nil
}

// Copy queues copying src, a target of the readback's size that has been
// drawn, over what the readback held. A copy still running refuses it.
func (r *Readback) Copy(src *Target) error {
	d := r.d
	switch {
	case r.closed || src == nil || src.closed:
		return errors.New("gpu: Readback.Copy with a closed readback or target")
	case src.s.d != d:
		return errors.New("gpu: Readback.Copy across devices")
	case src.Size() != image.Pt(r.w, r.h):
		return fmt.Errorf("gpu: Readback.Copy of a %v target into a %dx%d readback", src.Size(), r.w, r.h)
	case src.s.layout == vk.ImageLayoutUndefined:
		return errors.New("gpu: Readback.Copy of a target never drawn")
	case src.s.vf == floatTargetFormat:
		return errors.New("gpu: Readback.Copy of a float target")
	}
	if r.copied {
		if s := d.cmds.GetFenceStatus(d.device, r.fence); s != vk.Success {
			if s == vk.NotReady {
				return errors.New("gpu: Readback.Copy while a copy is running")
			}
			return fmt.Errorf("gpu: vkGetFenceStatus (readback): %w", resultError(s))
		}
		if err := resetFence(d, r.fence); err != nil {
			return err
		}
		r.copied = false
	}
	err := d.submitAsync(func(cb vk.CommandBuffer) {
		src.acquire(cb)
		src.s.download(cb, image.Rect(0, 0, r.w, r.h), r.buf, 0)
		d.hostReadBarrier(cb)
		src.release(cb)
	}, submission{})
	if err != nil {
		return err
	}
	if s := d.cmds.QueueSubmit(d.queue, 0, nil, r.fence); s != vk.Success {
		return fmt.Errorf("gpu: vkQueueSubmit (readback fence): %w", resultError(s))
	}
	r.copied = true
	return nil
}

// Wait blocks until the last Copy is done, on any goroutine, while the
// device goes on drawing on another; Close must not overlap it.
func (r *Readback) Wait() error {
	if r.closed || !r.copied {
		return errors.New("gpu: Readback.Wait with no copy running")
	}
	if s := r.d.cmds.WaitForFences(r.d.device, 1, &r.fence, vk.True, intconv.Must[uint64](r.d.waitLimit.Nanoseconds())); s != vk.Success {
		return fmt.Errorf("gpu: vkWaitForFences (readback): %w", resultError(s))
	}
	return nil
}

// Pixels is the last finished copy: the target's rows in its own format,
// 4 bytes a pixel, and the bytes between rows. They stay valid until the
// next Copy or Close.
func (r *Readback) Pixels() ([]byte, int, error) {
	if r.closed || !r.copied {
		return nil, 0, errors.New("gpu: Readback.Pixels with no copy")
	}
	if s := r.d.cmds.GetFenceStatus(r.d.device, r.fence); s != vk.Success {
		return nil, 0, fmt.Errorf("gpu: Readback.Pixels before its copy finished: %w", resultError(s))
	}
	if err := r.buf.invalidate(); err != nil {
		return nil, 0, err
	}
	return r.buf.mapped, r.w * 4, nil
}

// Close releases the readback; closing it again, or after its device,
// does nothing.
func (r *Readback) Close() {
	if r.closed {
		return
	}
	r.closed = true
	d := r.d
	delete(d.readbacks, r)
	d.retire(r)
}

// hostReadBarrier records making the transfers before it available to
// the host: a fence alone signals their end, not their visibility.
func (d *Device) hostReadBarrier(cb vk.CommandBuffer) {
	b := &d.args.memory
	*b = vk.MemoryBarrier{
		SType:         vk.StructureTypeMemoryBarrier,
		SrcAccessMask: vk.AccessFlags(vk.AccessTransferWriteBit),
		DstAccessMask: vk.AccessFlags(vk.AccessHostReadBit),
	}
	d.cmds.CmdPipelineBarrier(cb, vk.PipelineStageFlags(vk.PipelineStageTransferBit), vk.PipelineStageFlags(vk.PipelineStageHostBit), 0, 1, b, 0, nil, 0, nil)
}
