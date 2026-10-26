package gpu

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/stubbedev/lys-ui/third_party/vk"
)

// buffer is host-visible device memory mapped for the CPU: the frame's
// primitives and uploads it writes, and the encoder's bitstream it
// reads.
type buffer struct {
	d      *Device
	buf    vk.Buffer
	memory vk.DeviceMemory
	usage  vk.BufferUsageFlagBits
	// read marks a buffer the CPU reads (newReadBuffer), which fit
	// reallocates as one.
	read bool
	// flags are the memory's properties: coherent memory shows device
	// writes in mapped without an invalidate.
	flags vk.MemoryPropertyFlags
	// mapped is the whole buffer.
	mapped []byte
}

// newBuffer allocates size coherent bytes for usage, mapped, for the
// CPU to write.
func (d *Device) newBuffer(size int, usage vk.BufferUsageFlagBits) (*buffer, error) {
	return d.newMappedBuffer(size, usage, nil, vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit)
}

// newReadBuffer allocates size bytes for usage, mapped, for the CPU to
// read: cached memory where the device has it, coherent otherwise.
// next chains onto the buffer's create info.
func (d *Device) newReadBuffer(size int, usage vk.BufferUsageFlagBits, next unsafe.Pointer) (*buffer, error) {
	b, err := d.newMappedBuffer(size, usage, next, vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCachedBit)
	if err != nil {
		b, err = d.newMappedBuffer(size, usage, next, vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit)
	}
	if err != nil {
		return nil, err
	}
	b.read = true
	return b, nil
}

// newMappedBuffer allocates size bytes for usage in memory with every
// flag in want, mapped.
func (d *Device) newMappedBuffer(size int, usage vk.BufferUsageFlagBits, next unsafe.Pointer, want vk.MemoryPropertyFlagBits) (*buffer, error) {
	b := &buffer{d: d, usage: usage}
	bci := vk.BufferCreateInfo{SType: vk.StructureTypeBufferCreateInfo, PNext: (*uintptr)(next), Size: vk.DeviceSize(size), Usage: vk.BufferUsageFlags(usage), SharingMode: vk.SharingModeExclusive}
	if r := d.cmds.CreateBuffer(d.device, &bci, nil, &b.buf); r != vk.Success {
		return nil, fmt.Errorf("gpu: vkCreateBuffer %d bytes: %d", size, r)
	}
	var req vk.MemoryRequirements
	d.cmds.GetBufferMemoryRequirements(d.device, b.buf, &req)
	mem, flags, err := d.allocateMapped(req, want)
	if err != nil {
		b.close()
		return nil, err
	}
	b.memory, b.flags = mem, flags
	if r := d.cmds.BindBufferMemory(d.device, b.buf, b.memory, 0); r != vk.Success {
		b.close()
		return nil, fmt.Errorf("gpu: vkBindBufferMemory: %d", r)
	}
	// C writes the mapping's address through an integer, so where it
	// writes is pinned: a stack slot could move before it does.
	var pin runtime.Pinner
	defer pin.Unpin()
	ptr := new(unsafe.Pointer)
	pin.Pin(ptr)
	if r := d.cmds.MapMemory(d.device, b.memory, 0, vk.DeviceSize(size), 0, uintptr(unsafe.Pointer(ptr))); r != vk.Success {
		b.close()
		return nil, fmt.Errorf("gpu: vkMapMemory %d bytes: %d", size, r)
	}
	b.mapped = unsafe.Slice((*byte)(*ptr), size)
	return b, nil
}

// fit returns b when it holds size bytes, else a buffer twice the size
// needed with b closed: the caller replaces b with the result. The
// device must be idle with b.
func (b *buffer) fit(size int) (*buffer, error) {
	if b != nil && len(b.mapped) >= size {
		return b, nil
	}
	d, usage, read := b.d, b.usage, b.read
	b.close()
	if read {
		return d.newReadBuffer(max(2*size, 1<<16), usage, nil)
	}
	return d.newBuffer(max(2*size, 1<<16), usage)
}

// invalidate makes the device's writes to b visible in mapped.
func (b *buffer) invalidate() error {
	if b.flags&vk.MemoryPropertyFlags(vk.MemoryPropertyHostCoherentBit) != 0 {
		return nil
	}
	r := vk.MappedMemoryRange{SType: vk.StructureTypeMappedMemoryRange, Memory: b.memory, Size: vk.DeviceSize(vk.WholeSize)}
	if res := b.d.cmds.InvalidateMappedMemoryRanges(b.d.device, 1, &r); res != vk.Success {
		return fmt.Errorf("gpu: vkInvalidateMappedMemoryRanges: %d", res)
	}
	return nil
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

// deviceBuffer is device-local memory the CPU never maps: what one
// pass writes for the next to read.
type deviceBuffer struct {
	d      *Device
	buf    vk.Buffer
	memory vk.DeviceMemory
}

// newDeviceBuffer allocates size device-local bytes for usage.
func (d *Device) newDeviceBuffer(size int, usage vk.BufferUsageFlagBits) (*deviceBuffer, error) {
	b := &deviceBuffer{d: d}
	bci := vk.BufferCreateInfo{SType: vk.StructureTypeBufferCreateInfo, Size: vk.DeviceSize(size), Usage: vk.BufferUsageFlags(usage), SharingMode: vk.SharingModeExclusive}
	if r := d.cmds.CreateBuffer(d.device, &bci, nil, &b.buf); r != vk.Success {
		return nil, fmt.Errorf("gpu: vkCreateBuffer %d bytes: %d", size, r)
	}
	var req vk.MemoryRequirements
	d.cmds.GetBufferMemoryRequirements(d.device, b.buf, &req)
	mem, err := d.allocate(req, vk.MemoryPropertyDeviceLocalBit, nil)
	if err != nil {
		b.close()
		return nil, err
	}
	b.memory = mem
	if r := d.cmds.BindBufferMemory(d.device, b.buf, b.memory, 0); r != vk.Success {
		b.close()
		return nil, fmt.Errorf("gpu: vkBindBufferMemory: %d", r)
	}
	return b, nil
}

func (b *deviceBuffer) close() {
	d := b.d
	if b.buf != 0 {
		d.cmds.DestroyBuffer(d.device, b.buf, nil)
		b.buf = 0
	}
	if b.memory != 0 {
		d.cmds.FreeMemory(d.device, b.memory, nil)
		b.memory = 0
	}
}
