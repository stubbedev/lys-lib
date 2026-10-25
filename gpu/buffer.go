package gpu

import (
	"fmt"
	"unsafe"

	"github.com/stubbedev/lys-ui/third_party/vk"
)

// buffer is host-visible, coherent device memory mapped for the CPU to
// write: the frame's primitives and its uploads.
type buffer struct {
	d      *Device
	buf    vk.Buffer
	memory vk.DeviceMemory
	usage  vk.BufferUsageFlagBits
	// mapped is the whole buffer, written by the CPU.
	mapped []byte
}

// newBuffer allocates size bytes for usage, mapped.
func (d *Device) newBuffer(size int, usage vk.BufferUsageFlagBits) (*buffer, error) {
	b := &buffer{d: d, usage: usage}
	bci := vk.BufferCreateInfo{SType: vk.StructureTypeBufferCreateInfo, Size: vk.DeviceSize(size), Usage: vk.BufferUsageFlags(usage), SharingMode: vk.SharingModeExclusive}
	if r := d.cmds.CreateBuffer(d.device, &bci, nil, &b.buf); r != vk.Success {
		return nil, fmt.Errorf("gpu: vkCreateBuffer %d bytes: %d", size, r)
	}
	var req vk.MemoryRequirements
	d.cmds.GetBufferMemoryRequirements(d.device, b.buf, &req)
	mt, err := d.memoryType(req.MemoryTypeBits, vk.MemoryPropertyFlags(vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit))
	if err != nil {
		b.close()
		return nil, err
	}
	mai := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: mt}
	if r := d.cmds.AllocateMemory(d.device, &mai, nil, &b.memory); r != vk.Success {
		b.close()
		return nil, fmt.Errorf("gpu: vkAllocateMemory %d bytes: %d", req.Size, r)
	}
	if r := d.cmds.BindBufferMemory(d.device, b.buf, b.memory, 0); r != vk.Success {
		b.close()
		return nil, fmt.Errorf("gpu: vkBindBufferMemory: %d", r)
	}
	var ptr unsafe.Pointer
	if r := d.cmds.MapMemory(d.device, b.memory, 0, vk.DeviceSize(size), 0, uintptr(unsafe.Pointer(&ptr))); r != vk.Success {
		b.close()
		return nil, fmt.Errorf("gpu: vkMapMemory %d bytes: %d", size, r)
	}
	b.mapped = unsafe.Slice((*byte)(ptr), size)
	return b, nil
}

// fit returns b when it holds size bytes, else a buffer twice the size
// needed with b closed: the caller replaces b with the result. The
// device must be idle with b.
func (b *buffer) fit(size int) (*buffer, error) {
	if b != nil && len(b.mapped) >= size {
		return b, nil
	}
	d, usage := b.d, b.usage
	b.close()
	return d.newBuffer(max(2*size, 1<<16), usage)
}

func (b *buffer) close() {
	d := b.d
	if b.mapped != nil {
		d.cmds.UnmapMemory(d.device, b.memory)
		b.mapped = nil
	}
	if b.buf != 0 {
		d.cmds.DestroyBuffer(d.device, b.buf, nil)
		b.buf = 0
	}
	if b.memory != 0 {
		d.cmds.FreeMemory(d.device, b.memory, nil)
		b.memory = 0
	}
}
