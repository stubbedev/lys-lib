package gpu

import (
	"fmt"
	"image"

	"github.com/stubbedev/lys-ui/third_party/vk"
)

// surfaceFormat is every surface's format: B8G8R8A8 in memory, the
// byte order of render's ARGB8888 canvases and of the targets.
const surfaceFormat = vk.FormatB8g8r8a8Unorm

// surface is an image the renderer draws into and samples: a target, a
// layer, an upload page. Its view, framebuffer and descriptor set come
// with its first use.
type surface struct {
	d      *Device
	image  vk.Image
	memory vk.DeviceMemory
	w, h   int
	// layout is the image's current layout, which a recording picks up
	// and leaves the image in.
	layout vk.ImageLayout
	view   vk.ImageView
	fb     vk.Framebuffer
	set    vk.DescriptorSet
}

// newSurface allocates a w x h device-local surface.
func (d *Device) newSurface(w, h int) (*surface, error) {
	s := &surface{d: d, w: w, h: h, layout: vk.ImageLayoutUndefined}
	ici := vk.ImageCreateInfo{
		SType: vk.StructureTypeImageCreateInfo, ImageType: vk.ImageType2d, Format: surfaceFormat,
		Extent: vk.Extent3D{Width: uint32(w), Height: uint32(h), Depth: 1}, MipLevels: 1, ArrayLayers: 1,
		Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingOptimal,
		Usage:       vk.ImageUsageFlags(vk.ImageUsageTransferDstBit | vk.ImageUsageTransferSrcBit | vk.ImageUsageColorAttachmentBit | vk.ImageUsageSampledBit),
		SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined,
	}
	if r := d.cmds.CreateImage(d.device, &ici, nil, &s.image); r != vk.Success {
		return nil, fmt.Errorf("gpu: vkCreateImage %dx%d: %d", w, h, r)
	}
	var req vk.MemoryRequirements
	d.cmds.GetImageMemoryRequirements(d.device, s.image, &req)
	mem, err := d.allocate(req, vk.MemoryPropertyDeviceLocalBit, nil)
	if err != nil {
		s.close()
		return nil, err
	}
	s.memory = mem
	if r := d.cmds.BindImageMemory(d.device, s.image, s.memory, 0); r != vk.Success {
		s.close()
		return nil, fmt.Errorf("gpu: vkBindImageMemory: %d", r)
	}
	return s, nil
}

// ready makes the view, framebuffer and descriptor set drawing into
// and sampling s need.
func (s *surface) ready() error {
	if s.set != 0 {
		return nil
	}
	d := s.d
	vci := vk.ImageViewCreateInfo{
		SType: vk.StructureTypeImageViewCreateInfo, Image: s.image, ViewType: vk.ImageViewType2d, Format: surfaceFormat,
		SubresourceRange: colorRange,
	}
	if r := d.cmds.CreateImageView(d.device, &vci, nil, &s.view); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateImageView: %d", r)
	}
	fci := vk.FramebufferCreateInfo{
		SType: vk.StructureTypeFramebufferCreateInfo, RenderPass: d.pass, AttachmentCount: 1, PAttachments: &s.view,
		Width: uint32(s.w), Height: uint32(s.h), Layers: 1,
	}
	if r := d.cmds.CreateFramebuffer(d.device, &fci, nil, &s.fb); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateFramebuffer %dx%d: %d", s.w, s.h, r)
	}
	set, err := d.textureSet(s.view)
	if err != nil {
		return err
	}
	s.set = set
	return nil
}

// colorRange is a surface's whole color subresource.
var colorRange = vk.ImageSubresourceRange{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LevelCount: 1, LayerCount: 1}

// to records a barrier moving s to layout after all earlier work and
// before all later work. Within a frame's few transitions the full
// barrier costs nothing measurable and cannot under-synchronize.
func (s *surface) to(cb vk.CommandBuffer, layout vk.ImageLayout) {
	if s.layout == layout {
		return
	}
	s.barrier(cb, layout, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
}

// barrier records the full barrier to layout, moving queue family from
// src to dst (both ignored for none).
func (s *surface) barrier(cb vk.CommandBuffer, layout vk.ImageLayout, src, dst uint32) {
	all := vk.AccessFlags(vk.AccessMemoryReadBit | vk.AccessMemoryWriteBit)
	b := vk.ImageMemoryBarrier{
		SType: vk.StructureTypeImageMemoryBarrier, SrcAccessMask: all, DstAccessMask: all,
		OldLayout: s.layout, NewLayout: layout,
		SrcQueueFamilyIndex: src, DstQueueFamilyIndex: dst, Image: s.image, SubresourceRange: colorRange,
	}
	stages := vk.PipelineStageFlags(vk.PipelineStageAllCommandsBit)
	s.d.cmds.CmdPipelineBarrier(cb, stages, stages, 0, 0, nil, 0, nil, 1, &b)
	s.layout = layout
}

// download records copying s's region r into b at offset, rows tight.
func (s *surface) download(cb vk.CommandBuffer, r image.Rectangle, b *buffer, offset int) {
	s.to(cb, vk.ImageLayoutTransferSrcOptimal)
	region := vk.BufferImageCopy{
		BufferOffset:     vk.DeviceSize(offset),
		ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
		ImageOffset:      vk.Offset3D{X: int32(r.Min.X), Y: int32(r.Min.Y)},
		ImageExtent:      vk.Extent3D{Width: uint32(r.Dx()), Height: uint32(r.Dy()), Depth: 1},
	}
	s.d.cmds.CmdCopyImageToBuffer(cb, s.image, vk.ImageLayoutTransferSrcOptimal, b.buf, 1, &region)
}

// upload records copying b's rows from offset, rowLength pixels apart,
// into s's region r.
func (s *surface) upload(cb vk.CommandBuffer, r image.Rectangle, b *buffer, offset, rowLength int) {
	s.to(cb, vk.ImageLayoutTransferDstOptimal)
	region := vk.BufferImageCopy{
		BufferOffset: vk.DeviceSize(offset), BufferRowLength: uint32(rowLength),
		ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
		ImageOffset:      vk.Offset3D{X: int32(r.Min.X), Y: int32(r.Min.Y)},
		ImageExtent:      vk.Extent3D{Width: uint32(r.Dx()), Height: uint32(r.Dy()), Depth: 1},
	}
	s.d.cmds.CmdCopyBufferToImage(cb, b.buf, s.image, vk.ImageLayoutTransferDstOptimal, 1, &region)
}

// close releases what s made; an image it does not own (a target's)
// the owner frees.
func (s *surface) close() {
	d := s.d
	if s.set != 0 {
		d.cmds.FreeDescriptorSets(d.device, d.descPool, 1, &s.set)
		s.set = 0
	}
	if s.fb != 0 {
		d.cmds.DestroyFramebuffer(d.device, s.fb, nil)
		s.fb = 0
	}
	if s.view != 0 {
		d.cmds.DestroyImageView(d.device, s.view, nil)
		s.view = 0
	}
	if s.memory == 0 {
		return
	}
	if s.image != 0 {
		d.cmds.DestroyImage(d.device, s.image, nil)
		s.image = 0
	}
	d.cmds.FreeMemory(d.device, s.memory, nil)
	s.memory = 0
}
