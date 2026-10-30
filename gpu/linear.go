package gpu

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"slices"
	"unsafe"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// Linear is a linear single-plane dmabuf imported as a buffer, not an
// image: what a copy engine writes a frame into for a device that
// scans out only linear buffers another GPU allocated, even where this
// device renders into no linear layout (NVIDIA's). Close it when done;
// the device closes the ones still open with it.
type Linear struct {
	d      *Device
	buf    vk.Buffer
	memory vk.DeviceMemory
	format fourcc.Format
	w, h   int
	offset int
	stride int
	closed bool
}

// ImportLinear imports b, a dmabuf in one of TextureFormats with the
// linear modifier and one plane, as a Linear that CopyToLinear writes.
// b's fd stays the caller's.
func (d *Device) ImportLinear(b dmabuf.Buffer) (*Linear, error) {
	if !d.exports {
		return nil, ErrNoDmabuf
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	switch {
	case !slices.Contains(textureFormats, b.Format):
		return nil, fmt.Errorf("gpu: no linear buffer in DRM format %s (%v)", b.Format, textureFormats)
	case b.Modifier != fourcc.Linear:
		return nil, fmt.Errorf("gpu: a linear buffer in modifier %s", b.Modifier)
	case len(b.Planes) != 1:
		return nil, fmt.Errorf("gpu: a linear buffer of %d planes, want 1", len(b.Planes))
	}
	p := b.Planes[0]
	px := pixelFormats[b.Format].size
	if int(p.Stride) < b.Width*px || int(p.Stride)%px != 0 {
		return nil, fmt.Errorf("gpu: stride %d for %d pixels of %d bytes", p.Stride, b.Width, px)
	}
	size, err := dmabufSize(p.FD)
	if err != nil {
		return nil, err
	}
	if need := int64(p.Offset) + int64(p.Stride)*int64(b.Height-1) + int64(b.Width*px); size < need {
		return nil, fmt.Errorf("gpu: dmabuf %d holds %d bytes, a %dx%d %s buffer at offset %d, stride %d needs %d", p.FD, size, b.Width, b.Height, b.Format, p.Offset, p.Stride, need)
	}
	l := &Linear{d: d, format: b.Format, w: b.Width, h: b.Height, offset: int(p.Offset), stride: int(p.Stride)}
	if err := l.create(p.FD, size); err != nil {
		l.release()
		return nil, err
	}
	d.linears[l] = struct{}{}
	return l, nil
}

func (l *Linear) create(fd int, size int64) error {
	d := l.d
	ext := vk.ExternalMemoryBufferCreateInfo{SType: vk.StructureTypeExternalMemoryBufferCreateInfo, HandleTypes: vk.ExternalMemoryHandleTypeFlags(vk.ExternalMemoryHandleTypeDmaBufBitExt)}
	bci := vk.BufferCreateInfo{
		SType: vk.StructureTypeBufferCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&ext)), Size: intconv.Must[vk.DeviceSize](size),
		Usage: flags[vk.BufferUsageFlags](vk.BufferUsageTransferDstBit | vk.BufferUsageTransferSrcBit), SharingMode: vk.SharingModeExclusive,
	}
	r := d.cmds.CreateBuffer(d.device, &bci, nil, &l.buf)
	runtime.KeepAlive(&ext)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateBuffer importing a %d-byte dmabuf: %d", size, r)
	}
	var req vk.MemoryRequirements
	d.cmds.GetBufferMemoryRequirements(d.device, l.buf, &req)
	mem, err := d.importMemory(fd, req, &vk.MemoryDedicatedAllocateInfo{Buffer: l.buf})
	if err != nil {
		return err
	}
	l.memory = mem
	if r := d.cmds.BindBufferMemory(d.device, l.buf, l.memory, 0); r != vk.Success {
		return fmt.Errorf("gpu: vkBindBufferMemory (dmabuf): %d", r)
	}
	return nil
}

// Size is the buffer's size in pixels.
func (l *Linear) Size() image.Point { return image.Pt(l.w, l.h) }

// Close releases the buffer; closing it again, or after its device,
// does nothing.
func (l *Linear) Close() {
	if l.closed {
		return
	}
	l.closed = true
	_ = l.d.idle()
	delete(l.d.linears, l)
	l.release()
}

func (l *Linear) release() {
	d := l.d
	if l.buf != 0 {
		d.cmds.DestroyBuffer(d.device, l.buf, nil)
		l.buf = 0
	}
	if l.memory != 0 {
		d.cmds.FreeMemory(d.device, l.memory, nil)
		l.memory = 0
	}
}

// CopyToLinear copies rects of src, clipped to it, into the same rects
// of dst, a buffer of src's size and format, with the copy engine:
// no render pass touches dst. Like a Pass it is submitted without
// waiting; SyncFile signals when it is done. dst outside rects keeps
// what it held.
func (d *Device) CopyToLinear(dst *Linear, src *Target, rects []image.Rectangle) error {
	switch {
	case dst == nil || dst.closed || src == nil || src.closed:
		return errors.New("gpu: CopyToLinear with a closed buffer or target")
	case dst.d != d || src.s.d != d:
		return errors.New("gpu: CopyToLinear across devices")
	case dst.Size() != src.Size() || pixelFormats[dst.format].vk != src.s.vf:
		return fmt.Errorf("gpu: CopyToLinear from a %v %s target into a %v %s buffer", src.Size(), src.buf.Format, dst.Size(), dst.format)
	case src.s.layout == vk.ImageLayoutUndefined:
		return errors.New("gpu: CopyToLinear from a target never drawn")
	}
	px := pixelFormats[dst.format].size
	bounds := image.Rectangle{Max: src.Size()}
	copies := make([]vk.BufferImageCopy, 0, len(rects))
	for _, r := range rects {
		if r = r.Intersect(bounds); r.Empty() {
			continue
		}
		copies = append(copies, vk.BufferImageCopy{
			BufferOffset:     intconv.Must[vk.DeviceSize](dst.offset + r.Min.Y*dst.stride + r.Min.X*px),
			BufferRowLength:  intconv.Must[uint32](dst.stride / px),
			ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
			ImageOffset:      offset3D(r.Min),
			ImageExtent:      extent3D(r.Dx(), r.Dy()),
		})
	}
	if len(copies) == 0 {
		return nil
	}
	return d.submitAsync(func(cb vk.CommandBuffer) {
		src.acquire(cb)
		src.s.to(cb, vk.ImageLayoutTransferSrcOptimal)
		dst.barrier(cb, vk.QueueFamilyForeignExt, d.family)
		d.cmds.CmdCopyImageToBuffer(cb, src.s.image, vk.ImageLayoutTransferSrcOptimal, dst.buf, count(copies), &copies[0])
		runtime.KeepAlive(copies)
		dst.barrier(cb, d.family, vk.QueueFamilyForeignExt)
		src.release(cb)
	}, false)
}

func (l *Linear) barrier(cb vk.CommandBuffer, from, to uint32) {
	all := vk.AccessFlags(vk.AccessMemoryReadBit | vk.AccessMemoryWriteBit)
	b := vk.BufferMemoryBarrier{
		SType: vk.StructureTypeBufferMemoryBarrier, SrcAccessMask: all, DstAccessMask: all,
		SrcQueueFamilyIndex: from, DstQueueFamilyIndex: to, Buffer: l.buf, Size: vk.DeviceSize(vk.WholeSize),
	}
	stages := vk.PipelineStageFlags(vk.PipelineStageAllCommandsBit)
	l.d.cmds.CmdPipelineBarrier(cb, stages, stages, 0, 0, nil, 1, &b, 0, nil)
}
