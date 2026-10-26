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

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-ui/dmabuf"
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
	"VK_EXT_queue_family_foreign",
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
	// maxDim is the largest image side the device makes.
	maxDim int
	// exports is whether the device renders into dmabufs.
	exports bool
	// video is the video encode queue, nil on a device that encodes
	// none.
	video *videoQueue
	// targets and buffers are the live ones, closed with the device.
	targets map[*Target]struct{}
	buffers map[*Buffer]struct{}
	// closed marks a device Close released.
	closed bool
	pipelines
	scratch
}

// Open picks the first Vulkan device with a graphics queue that
// exports dmabufs, else the first with a graphics queue.
func Open() (*Device, error) { return open(nil) }

// OpenNode opens the Vulkan device behind the DRM node dev, its device
// number (st_rdev, as of /dev/dri/renderD128 or /dev/dri/card0): the
// device a dmabuf consumer negotiated, which must export dmabufs.
// linux-dmabuf's main device may name either node of a GPU.
func OpenNode(dev uint64) (*Device, error) {
	return open(&drmNode{major: int64(unix.Major(dev)), minor: int64(unix.Minor(dev))})
}

// drmNode is a DRM render node's major and minor device number.
type drmNode struct{ major, minor int64 }

// open picks the first device node names (any, nil) with a graphics
// queue that exports dmabufs, else, with no node named, the first with
// a graphics queue.
func open(node *drmNode) (*Device, error) {
	if err := vk.Init(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoVulkan, err)
	}
	d := &Device{cmds: vk.NewCommands(), targets: map[*Target]struct{}{}, buffers: map[*Buffer]struct{}{}}
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
	modes := []bool{true, false}
	if node != nil {
		modes = modes[:1]
	}
	for _, exporting := range modes {
		for _, p := range phys {
			err := d.pick(p, exporting, node)
			if err == nil {
				if err = d.open(); err == nil {
					if err = d.createPipelines(); err == nil && exporting {
						err = d.proveExport()
					}
					if err == nil {
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
	ai := vk.ApplicationInfo{SType: vk.StructureTypeApplicationInfo, PApplicationName: uintptr(unsafe.Pointer(&app[0])), ApiVersion: vkVersion(1, 3)}
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

// pick takes p when it has a graphics queue, exporting the dmabuf
// export extensions, and when node is named it is that render node.
func (d *Device) pick(p vk.PhysicalDevice, exporting bool, node *drmNode) error {
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
	if node != nil {
		if !have["VK_EXT_physical_device_drm"] {
			return fmt.Errorf("%s cannot name its DRM node (no VK_EXT_physical_device_drm)", name)
		}
		drm := vk.PhysicalDeviceDrmPropertiesEXT{SType: vk.StructureTypePhysicalDeviceDrmPropertiesExt}
		props2 := vk.PhysicalDeviceProperties2{SType: vk.StructureTypePhysicalDeviceProperties2, PNext: (*uintptr)(unsafe.Pointer(&drm))}
		d.cmds.GetPhysicalDeviceProperties2(p, &props2)
		runtime.KeepAlive(&drm)
		render := drm.HasRender != 0 && drm.RenderMajor == node.major && drm.RenderMinor == node.minor
		primary := drm.HasPrimary != 0 && drm.PrimaryMajor == node.major && drm.PrimaryMinor == node.minor
		if !render && !primary {
			return fmt.Errorf("%s is not DRM node %d:%d", name, node.major, node.minor)
		}
	}
	d.cmds.GetPhysicalDeviceQueueFamilyProperties(p, &n, nil)
	fams := make([]vk.QueueFamilyProperties, n)
	if n > 0 {
		d.cmds.GetPhysicalDeviceQueueFamilyProperties(p, &n, &fams[0])
	}
	for i, f := range fams {
		if f.QueueFlags&vk.QueueFlags(vk.QueueGraphicsBit|vk.QueueComputeBit) == vk.QueueFlags(vk.QueueGraphicsBit|vk.QueueComputeBit) {
			d.phys, d.family, d.name, d.exports, d.maxDim = p, uint32(i), name, exporting, int(props.Limits.MaxImageDimension2D)
			d.video = d.probeVideo(p, props.ApiVersion, have)
			return nil
		}
	}
	return fmt.Errorf("%s has no graphics and compute queue", name)
}

// open creates the logical device: its graphics queue, and the video
// encode queue with synchronization2 where the device encodes.
func (d *Device) open() error {
	prio := float32(1)
	qcis := []vk.DeviceQueueCreateInfo{{SType: vk.StructureTypeDeviceQueueCreateInfo, QueueFamilyIndex: d.family, QueueCount: 1, PQueuePriorities: &prio}}
	dci := vk.DeviceCreateInfo{SType: vk.StructureTypeDeviceCreateInfo}
	var exts []string
	if d.exports {
		exts = append(exts, exportExtensions...)
	}
	f13 := vk.PhysicalDeviceVulkan13Features{SType: vk.StructureTypePhysicalDeviceVulkan13Features, Synchronization2: vk.True}
	if d.video != nil {
		if d.video.family != d.family {
			qcis = append(qcis, vk.DeviceQueueCreateInfo{SType: vk.StructureTypeDeviceQueueCreateInfo, QueueFamilyIndex: d.video.family, QueueCount: 1, PQueuePriorities: &prio})
		}
		exts = append(exts, d.video.extensions()...)
		dci.PNext = (*uintptr)(unsafe.Pointer(&f13))
	}
	dci.QueueCreateInfoCount, dci.PQueueCreateInfos = uint32(len(qcis)), &qcis[0]
	ptrs, keep := cstrs(exts)
	if len(ptrs) > 0 {
		dci.EnabledExtensionCount, dci.PpEnabledExtensionNames = uint32(len(ptrs)), uintptr(unsafe.Pointer(&ptrs[0]))
	}
	r := d.cmds.CreateDevice(d.phys, &dci, nil, &d.device)
	runtime.KeepAlive(keep)
	runtime.KeepAlive(qcis)
	runtime.KeepAlive(&f13)
	if r != vk.Success {
		return fmt.Errorf("%s: vkCreateDevice: %d", d.name, r)
	}
	if err := d.cmds.LoadDevice(d.device); err != nil {
		d.cmds.DestroyDevice(d.device, nil)
		return fmt.Errorf("%s: %w", d.name, err)
	}
	d.cmds.GetDeviceQueue(d.device, d.family, 0, &d.queue)
	if d.video != nil {
		d.cmds.GetDeviceQueue(d.device, d.video.family, 0, &d.video.queue)
	}
	d.cmds.GetPhysicalDeviceMemoryProperties(d.phys, &d.memProps)
	pci := vk.CommandPoolCreateInfo{SType: vk.StructureTypeCommandPoolCreateInfo, QueueFamilyIndex: d.family}
	if r := d.cmds.CreateCommandPool(d.device, &pci, nil, &d.pool); r != vk.Success {
		d.cmds.DestroyDevice(d.device, nil)
		return fmt.Errorf("%s: vkCreateCommandPool: %d", d.name, r)
	}
	return nil
}

// proveExport exports one small image: a driver can offer the export
// extensions and modifiers and still fail every export (lavapipe
// without /dev/udmabuf), which only an export shows.
func (d *Device) proveExport() error {
	mods, err := d.modifiers(dmabuf.FormatARGB8888, shareUse)
	if err != nil {
		return err
	}
	if len(mods) == 0 {
		return fmt.Errorf("%s exports ARGB8888 in no modifier", d.name)
	}
	e, err := d.export(1, 1, dmabuf.FormatARGB8888, mods, shareUse)
	if err != nil {
		return fmt.Errorf("%s cannot export a dmabuf: %w", d.name, err)
	}
	e.close(d)
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
	for b := range d.buffers {
		b.Close()
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

// errNoMemoryType reports memory requirements no memory type with the
// flags asked for meets.
var errNoMemoryType = errors.New("gpu: no memory type")

// memoryType is the first memory type in bits with every flag in want.
func (d *Device) memoryType(bits uint32, want vk.MemoryPropertyFlagBits) (uint32, error) {
	for i := range d.memProps.MemoryTypeCount {
		if bits&(1<<i) != 0 && d.memProps.MemoryTypes[i].PropertyFlags&vk.MemoryPropertyFlags(want) == vk.MemoryPropertyFlags(want) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%w with flags %#x in %#x", errNoMemoryType, want, bits)
}

// allocate allocates memory meeting req in the first type with every
// flag in want, next chained onto the allocate info.
func (d *Device) allocate(req vk.MemoryRequirements, want vk.MemoryPropertyFlagBits, next unsafe.Pointer) (vk.DeviceMemory, error) {
	mem, _, err := d.allocateIn(req, want, next)
	return mem, err
}

// allocateMapped is allocate for memory the CPU maps, and the
// properties of the type it landed in.
func (d *Device) allocateMapped(req vk.MemoryRequirements, want vk.MemoryPropertyFlagBits) (vk.DeviceMemory, vk.MemoryPropertyFlags, error) {
	mem, mt, err := d.allocateIn(req, want, nil)
	if err != nil {
		return 0, 0, err
	}
	return mem, d.memProps.MemoryTypes[mt].PropertyFlags, nil
}

func (d *Device) allocateIn(req vk.MemoryRequirements, want vk.MemoryPropertyFlagBits, next unsafe.Pointer) (vk.DeviceMemory, uint32, error) {
	mt, err := d.memoryType(req.MemoryTypeBits, want)
	if err != nil {
		return 0, 0, err
	}
	mai := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, PNext: (*uintptr)(next), AllocationSize: req.Size, MemoryTypeIndex: mt}
	var mem vk.DeviceMemory
	if r := d.cmds.AllocateMemory(d.device, &mai, nil, &mem); r != vk.Success {
		return 0, 0, fmt.Errorf("gpu: vkAllocateMemory %d bytes: %d", req.Size, r)
	}
	return mem, mt, nil
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
