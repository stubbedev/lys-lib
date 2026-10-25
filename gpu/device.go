// Package gpu renders on the GPU through Vulkan, into memory shared
// with the compositor (or a display) as dmabufs: what a frame draws is
// handed over by file descriptor, with no copy. lys-ui's windows,
// lys-shell's surfaces and lys-wm's output all draw through it.
//
// Vulkan loads at runtime (third_party/vk, pure Go, no cgo). Open fails
// with ErrNoVulkan or ErrNoDevice where there is no loader or no device
// that draws; callers then draw on the CPU (package render). A device
// that cannot export dmabufs still renders offscreen (NewOffscreen);
// its NewTarget fails with ErrNoDmabuf.
//
// A Device and everything made from it belong to one goroutine.
package gpu

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"unsafe"

	"github.com/stubbedev/lys-ui/third_party/vk"
)

// ErrNoVulkan reports that the Vulkan loader (libvulkan) is missing.
var ErrNoVulkan = errors.New("gpu: no Vulkan loader")

// ErrNoDevice reports that no Vulkan device has a graphics queue.
var ErrNoDevice = errors.New("gpu: no Vulkan device draws")

// ErrNoDmabuf reports a device that cannot render into dmabufs.
var ErrNoDmabuf = errors.New("gpu: the device does not export dmabufs")

// exportExtensions are what rendering into exported dmabufs needs.
var exportExtensions = []string{
	"VK_KHR_external_memory_fd",
	"VK_EXT_external_memory_dma_buf",
	"VK_EXT_image_drm_format_modifier",
}

// Device is a Vulkan device rendering into dmabufs.
type Device struct {
	cmds     *vk.Commands
	instance vk.Instance
	phys     vk.PhysicalDevice
	device   vk.Device
	queue    vk.Queue
	family   uint32
	pool     vk.CommandPool
	memProps vk.PhysicalDeviceMemoryProperties
	name     string
	// exports is whether the device renders into dmabufs.
	exports bool
	// targets are the live targets, closed with the device.
	targets map[*Target]struct{}
	// closed marks a device Close released.
	closed bool
	pipelines
	scratch
}

// Open picks the first Vulkan device with a graphics queue that
// exports dmabufs, else the first with a graphics queue.
func Open() (*Device, error) {
	if err := vk.Init(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoVulkan, err)
	}
	d := &Device{cmds: vk.NewCommands(), targets: map[*Target]struct{}{}}
	if err := d.cmds.LoadGlobal(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoVulkan, err)
	}
	if err := d.createInstance(); err != nil {
		return nil, err
	}
	var n uint32
	d.cmds.EnumeratePhysicalDevices(d.instance, &n, nil)
	phys := make([]vk.PhysicalDevice, n)
	if n > 0 {
		d.cmds.EnumeratePhysicalDevices(d.instance, &n, &phys[0])
	}
	var reasons []error
	for _, exporting := range []bool{true, false} {
		for _, p := range phys {
			err := d.pick(p, exporting)
			if err == nil {
				if err = d.open(); err == nil {
					if err = d.createPipelines(); err == nil {
						return d, nil
					}
					d.closeDevice()
				}
			}
			if exporting {
				reasons = append(reasons, err)
			}
		}
	}
	d.cmds.DestroyInstance(d.instance, nil)
	return nil, fmt.Errorf("%w: %w", ErrNoDevice, errors.Join(reasons...))
}

func (d *Device) createInstance() error {
	app := cstr("lys-ui")
	ai := vk.ApplicationInfo{SType: vk.StructureTypeApplicationInfo, PApplicationName: uintptr(unsafe.Pointer(&app[0])), ApiVersion: vkVersion(1, 2)}
	ici := vk.InstanceCreateInfo{SType: vk.StructureTypeInstanceCreateInfo, PApplicationInfo: &ai}
	r := d.cmds.CreateInstance(&ici, nil, &d.instance)
	runtime.KeepAlive(app)
	if r != vk.Success {
		return fmt.Errorf("%w: vkCreateInstance: %d", ErrNoDevice, r)
	}
	if err := d.cmds.LoadInstance(d.instance); err != nil {
		d.cmds.DestroyInstance(d.instance, nil)
		return fmt.Errorf("%w: %w", ErrNoDevice, err)
	}
	vk.SetDeviceProcAddr(d.instance)
	return nil
}

// pick takes p when it has a graphics queue and, exporting, the
// dmabuf export extensions.
func (d *Device) pick(p vk.PhysicalDevice, exporting bool) error {
	var props vk.PhysicalDeviceProperties
	d.cmds.GetPhysicalDeviceProperties(p, &props)
	name := cname(props.DeviceName[:])
	var n uint32
	d.cmds.EnumerateDeviceExtensionProperties(p, 0, &n, nil)
	exts := make([]vk.ExtensionProperties, n)
	if n > 0 {
		d.cmds.EnumerateDeviceExtensionProperties(p, 0, &n, &exts[0])
	}
	have := make(map[string]bool, len(exts))
	for _, e := range exts {
		have[cname(e.ExtensionName[:])] = true
	}
	if exporting {
		for _, want := range exportExtensions {
			if !have[want] {
				return fmt.Errorf("%s lacks %s", name, want)
			}
		}
	}
	d.cmds.GetPhysicalDeviceQueueFamilyProperties(p, &n, nil)
	fams := make([]vk.QueueFamilyProperties, n)
	if n > 0 {
		d.cmds.GetPhysicalDeviceQueueFamilyProperties(p, &n, &fams[0])
	}
	for i, f := range fams {
		if f.QueueFlags&vk.QueueFlags(vk.QueueGraphicsBit) != 0 {
			d.phys, d.family, d.name, d.exports = p, uint32(i), name, exporting
			return nil
		}
	}
	return fmt.Errorf("%s has no graphics queue", name)
}

func (d *Device) open() error {
	prio := float32(1)
	qci := vk.DeviceQueueCreateInfo{SType: vk.StructureTypeDeviceQueueCreateInfo, QueueFamilyIndex: d.family, QueueCount: 1, PQueuePriorities: &prio}
	dci := vk.DeviceCreateInfo{SType: vk.StructureTypeDeviceCreateInfo, QueueCreateInfoCount: 1, PQueueCreateInfos: &qci}
	ptrs, keep := cstrs(exportExtensions)
	if d.exports {
		dci.EnabledExtensionCount, dci.PpEnabledExtensionNames = uint32(len(ptrs)), uintptr(unsafe.Pointer(&ptrs[0]))
	}
	r := d.cmds.CreateDevice(d.phys, &dci, nil, &d.device)
	runtime.KeepAlive(keep)
	if r != vk.Success {
		return fmt.Errorf("%s: vkCreateDevice: %d", d.name, r)
	}
	if err := d.cmds.LoadDevice(d.device); err != nil {
		d.cmds.DestroyDevice(d.device, nil)
		return fmt.Errorf("%s: %w", d.name, err)
	}
	d.cmds.GetDeviceQueue(d.device, d.family, 0, &d.queue)
	d.cmds.GetPhysicalDeviceMemoryProperties(d.phys, &d.memProps)
	pci := vk.CommandPoolCreateInfo{SType: vk.StructureTypeCommandPoolCreateInfo, QueueFamilyIndex: d.family}
	if r := d.cmds.CreateCommandPool(d.device, &pci, nil, &d.pool); r != vk.Success {
		d.cmds.DestroyDevice(d.device, nil)
		return fmt.Errorf("%s: vkCreateCommandPool: %d", d.name, r)
	}
	return nil
}

// Exports reports whether the device renders into dmabufs, for
// NewTarget.
func (d *Device) Exports() bool { return d.exports }

// Name is the device's name, "NVIDIA GeForce RTX 3060 Ti" or
// "llvmpipe (LLVM 21.1.8, 256 bits)".
func (d *Device) Name() string { return d.name }

// Close releases the device and the targets still open on it: a target
// closed after is already closed. Closing it again does nothing.
func (d *Device) Close() {
	if d.closed {
		return
	}
	d.closed = true
	for t := range d.targets {
		t.Close()
	}
	d.closeDevice()
	d.cmds.DestroyInstance(d.instance, nil)
}

// closeDevice releases the logical device and what was made on it.
func (d *Device) closeDevice() {
	d.cmds.DeviceWaitIdle(d.device)
	d.closeScratch()
	d.destroyPipelines()
	d.pipelines = pipelines{}
	d.cmds.DestroyCommandPool(d.device, d.pool, nil)
	d.cmds.DestroyDevice(d.device, nil)
}

// memoryType is the first memory type in bits with every flag in want.
func (d *Device) memoryType(bits uint32, want vk.MemoryPropertyFlags) (uint32, error) {
	for i := range d.memProps.MemoryTypeCount {
		if bits&(1<<i) != 0 && d.memProps.MemoryTypes[i].PropertyFlags&want == want {
			return i, nil
		}
	}
	return 0, fmt.Errorf("gpu: no memory type with flags %#x in %#x", want, bits)
}

// submit records one command buffer with record, runs it and waits for
// it to finish.
func (d *Device) submit(record func(cb vk.CommandBuffer)) error {
	cai := vk.CommandBufferAllocateInfo{SType: vk.StructureTypeCommandBufferAllocateInfo, CommandPool: d.pool, Level: vk.CommandBufferLevelPrimary, CommandBufferCount: 1}
	var cb vk.CommandBuffer
	if r := d.cmds.AllocateCommandBuffers(d.device, &cai, &cb); r != vk.Success {
		return fmt.Errorf("gpu: vkAllocateCommandBuffers: %d", r)
	}
	defer d.cmds.FreeCommandBuffers(d.device, d.pool, 1, &cb)
	cbi := vk.CommandBufferBeginInfo{SType: vk.StructureTypeCommandBufferBeginInfo, Flags: vk.CommandBufferUsageFlags(vk.CommandBufferUsageOneTimeSubmitBit)}
	if r := d.cmds.BeginCommandBuffer(cb, &cbi); r != vk.Success {
		return fmt.Errorf("gpu: vkBeginCommandBuffer: %d", r)
	}
	record(cb)
	if r := d.cmds.EndCommandBuffer(cb); r != vk.Success {
		return fmt.Errorf("gpu: vkEndCommandBuffer: %d", r)
	}
	si := vk.SubmitInfo{SType: vk.StructureTypeSubmitInfo, CommandBufferCount: 1, PCommandBuffers: &cb}
	if r := d.cmds.QueueSubmit(d.queue, 1, &si, 0); r != vk.Success {
		return fmt.Errorf("gpu: vkQueueSubmit: %d", r)
	}
	if r := d.cmds.QueueWaitIdle(d.queue); r != vk.Success {
		return fmt.Errorf("gpu: vkQueueWaitIdle: %d", r)
	}
	return nil
}

func vkVersion(major, minor uint32) uint32 { return major<<22 | minor<<12 }

func cstr(s string) []byte { return append([]byte(s), 0) }

func cstrs(names []string) ([]uintptr, [][]byte) {
	keep := make([][]byte, len(names))
	ptrs := make([]uintptr, len(names))
	for i, n := range names {
		keep[i] = cstr(n)
		ptrs[i] = uintptr(unsafe.Pointer(&keep[i][0]))
	}
	return ptrs, keep
}

func cname(b []byte) string {
	if i := slices.Index(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}
