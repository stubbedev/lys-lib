package gpu

import (
	"fmt"
	"image"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// surfaceFormat is every surface's format: B8G8R8A8 in memory, the
// byte order of render's ARGB8888 canvases and of the targets.
const surfaceFormat = vk.FormatB8g8r8a8Unorm

// floatTargetFormat is NewOffscreenFloat's format. Vulkan requires every
// device to draw into, blend in and filter it.
const floatTargetFormat = vk.FormatR16g16b16a16Sfloat

// surface is an image the renderer draws into and samples: a target, a
// layer, an upload page. Its view, framebuffer and descriptor set come
// with its first use.
type surface struct {
	d      *Device
	image  vk.Image
	memory vk.DeviceMemory
	w, h   int
	vf     vk.Format
	// layout is the image's current layout, which a recording picks up
	// and leaves the image in.
	layout vk.ImageLayout
	// sampled marks a surface nothing draws into: it has no framebuffer.
	sampled bool
	view    vk.ImageView
	fb      vk.Framebuffer
	set     vk.DescriptorSet
}

// newSurface allocates a w x h device-local surface in surfaceFormat.
func (d *Device) newSurface(w, h int) (*surface, error) {
	return d.newSurfaceIn(w, h, surfaceFormat)
}

// newSurfaceIn allocates a w x h device-local surface in vf.
func (d *Device) newSurfaceIn(w, h int, vf vk.Format) (*surface, error) {
	return d.allocSurface(w, h, vf, vk.ImageUsageTransferDstBit|vk.ImageUsageTransferSrcBit|vk.ImageUsageColorAttachmentBit|vk.ImageUsageSampledBit)
}

// newImageSurface allocates a surface for an image's pixels, R, G, B, A in
// memory as *image.RGBA holds them, so an upload is a copy.
func (d *Device) newImageSurface(w, h int) (*surface, error) {
	s, err := d.allocSurface(w, h, vk.FormatR8g8b8a8Unorm, vk.ImageUsageTransferDstBit|vk.ImageUsageSampledBit)
	if err != nil {
		return nil, err
	}
	s.sampled = true
	return s, nil
}

func (d *Device) allocSurface(w, h int, vf vk.Format, usage vk.ImageUsageFlagBits) (*surface, error) {
	if err := d.checkSize(w, h); err != nil {
		return nil, err
	}
	d.surfacesMade.Add(1)
	s := &surface{d: d, w: w, h: h, vf: vf, layout: vk.ImageLayoutUndefined}
	ici := vk.ImageCreateInfo{
		SType: vk.StructureTypeImageCreateInfo, ImageType: vk.ImageType2d, Format: vf,
		Extent: extent3D(w, h), MipLevels: 1, ArrayLayers: 1,
		Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingOptimal,
		Usage:       flags[vk.ImageUsageFlags](usage),
		SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined,
	}
	if r := d.cmds.CreateImage(d.device, &ici, nil, &s.image); r != vk.Success {
		return nil, fmt.Errorf("gpu: vkCreateImage %dx%d: %w", w, h, resultError(r))
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
		return nil, fmt.Errorf("gpu: vkBindImageMemory: %w", resultError(r))
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
	pass := d.pass
	if s.vf != surfaceFormat && !s.sampled {
		ct, err := d.compTargetOf(s.vf)
		if err != nil {
			return err
		}
		pass = ct.pass
	}
	vci := vk.ImageViewCreateInfo{
		SType: vk.StructureTypeImageViewCreateInfo, Image: s.image, ViewType: vk.ImageViewType2d, Format: s.vf,
		SubresourceRange: colorRange,
	}
	if r := d.cmds.CreateImageView(d.device, &vci, nil, &s.view); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateImageView: %w", resultError(r))
	}
	if !s.sampled {
		fci := vk.FramebufferCreateInfo{
			SType: vk.StructureTypeFramebufferCreateInfo, RenderPass: pass, AttachmentCount: 1, PAttachments: &s.view,
			Width: intconv.Must[uint32](s.w), Height: intconv.Must[uint32](s.h), Layers: 1,
		}
		if r := d.cmds.CreateFramebuffer(d.device, &fci, nil, &s.fb); r != vk.Success {
			return fmt.Errorf("gpu: vkCreateFramebuffer %dx%d: %w", s.w, s.h, resultError(r))
		}
	}
	set, err := d.textureSet(s.view)
	if err != nil {
		return err
	}
	s.set = set
	return nil
}

func (s *surface) format() vk.Format { return s.vf }

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
	fullBarrier(s.d, cb, s.image, s.layout, layout, src, dst)
	s.layout = layout
}

// fullBarrier records moving img's color from one layout to another
// and from queue family src to dst, after all earlier work and before
// all later: core Vulkan, for what records on devices without
// synchronization2.
func fullBarrier(d *Device, cb vk.CommandBuffer, img vk.Image, from, to vk.ImageLayout, src, dst uint32) {
	all := vk.AccessFlags(vk.AccessMemoryReadBit | vk.AccessMemoryWriteBit)
	b := &d.args.barrier
	*b = vk.ImageMemoryBarrier{
		SType: vk.StructureTypeImageMemoryBarrier, SrcAccessMask: all, DstAccessMask: all,
		OldLayout: from, NewLayout: to,
		SrcQueueFamilyIndex: src, DstQueueFamilyIndex: dst, Image: img, SubresourceRange: colorRange,
	}
	stages := vk.PipelineStageFlags(vk.PipelineStageAllCommandsBit)
	d.cmds.CmdPipelineBarrier(cb, stages, stages, 0, 0, nil, 0, nil, 1, b)
}

// download records copying s's region r into b at offset, rows tight.
func (s *surface) download(cb vk.CommandBuffer, r image.Rectangle, b *buffer, offset int) {
	s.to(cb, vk.ImageLayoutTransferSrcOptimal)
	region := &s.d.args.region
	*region = vk.BufferImageCopy{
		BufferOffset:     intconv.Must[vk.DeviceSize](offset),
		ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
		ImageOffset:      offset3D(r.Min),
		ImageExtent:      extent3D(r.Dx(), r.Dy()),
	}
	s.d.cmds.CmdCopyImageToBuffer(cb, s.image, vk.ImageLayoutTransferSrcOptimal, b.buf, 1, region)
}

// upload records copying b's rows from offset, rowLength pixels apart,
// into s's region r.
func (s *surface) upload(cb vk.CommandBuffer, r image.Rectangle, b *buffer, offset, rowLength int) {
	s.to(cb, vk.ImageLayoutTransferDstOptimal)
	region := &s.d.args.region
	*region = vk.BufferImageCopy{
		BufferOffset: intconv.Must[vk.DeviceSize](offset), BufferRowLength: intconv.Must[uint32](rowLength),
		ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
		ImageOffset:      offset3D(r.Min),
		ImageExtent:      extent3D(r.Dx(), r.Dy()),
	}
	s.d.cmds.CmdCopyBufferToImage(cb, b.buf, s.image, vk.ImageLayoutTransferDstOptimal, 1, region)
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
