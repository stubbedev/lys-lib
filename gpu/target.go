package gpu

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"syscall"
	"unsafe"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// Target is an image the GPU renders into whose memory is a dmabuf: a
// window's frame handed to the compositor, or an output's scanout
// buffer. Its layout (the DRM modifier) is one the caller offered, as
// the consumer imports it. Between frames it belongs to the consumer.
type Target struct {
	s      surface
	memory []vk.DeviceMemory
	buf    dmabuf.Buffer
	// held is whether the device holds t, between acquire and release.
	held bool
	// offscreen marks a target no consumer imports (NewOffscreen).
	offscreen bool
	// closed marks a target Close released.
	closed bool
	// tex samples an offscreen target (Texture).
	tex *Texture
}

// NewOffscreen allocates a width x height target in device memory no
// other process imports: a frame rendered for its pixels (ReadPixels),
// a screenshot or a test.
func (d *Device) NewOffscreen(width, height int) (*Target, error) {
	s, err := d.newSurface(width, height)
	if err != nil {
		return nil, err
	}
	t := &Target{s: *s, offscreen: true, buf: dmabufOf(width, height)}
	d.targets[t] = struct{}{}
	return t, nil
}

func dmabufOf(width, height int) dmabuf.Buffer { return dmabuf.Buffer{Width: width, Height: height} }

// NewOffscreenFloat is NewOffscreen in half floats: premultiplied linear light
// that keeps values above one and below zero, for a compositor's working
// space. A pass draws into it and samples it as any other, with the same
// premultiplied blending; its pixels are not read back. It starts transparent:
// a NaN in fresh memory would survive every blend over it.
func (d *Device) NewOffscreenFloat(width, height int) (*Target, error) {
	s, err := d.newSurfaceIn(width, height, floatTargetFormat)
	if err != nil {
		return nil, err
	}
	t := &Target{s: *s, offscreen: true, buf: dmabufOf(width, height)}
	d.targets[t] = struct{}{}
	if err := t.clear([4]float32{}); err != nil {
		t.Close()
		return nil, err
	}
	return t, nil
}

// vkFormatOf maps a DRM format to the Vulkan format with the same byte
// order in memory.
func vkFormatOf(format fourcc.Format) (vk.Format, error) {
	if format != fourcc.ARGB8888 && format != fourcc.XRGB8888 {
		return 0, fmt.Errorf("gpu: a Buffer is ARGB8888 or XRGB8888, not DRM format %s", format)
	}
	vf, _, err := exportFormatOf(format)
	return vf, err
}

// exportFormatOf is the Vulkan format of an image exported in format,
// and how many memory planes it has: the targets' formats and the
// encoder's NV12.
func exportFormatOf(format fourcc.Format) (vk.Format, int, error) {
	if format != fourcc.NV12 && !slices.Contains(textureFormats, format) {
		return 0, 0, fmt.Errorf("gpu: no Vulkan format for DRM format %s", format)
	}
	pf, err := pixelFormatOf(format)
	return pf.vk, pf.planes, err
}

// NewTarget allocates a width x height image in format (one of
// TextureFormats; Render draws only into ARGB8888 and XRGB8888, a Pass
// into any) whose layout the
// driver picks among modifiers, the ones the consumer imports, that the
// device renders format in (Modifiers): linear when both take it. None
// in common, as with only the implicit fourcc.Invalid on offer, is an
// error naming both lists.
func (d *Device) NewTarget(width, height int, format fourcc.Format, modifiers []fourcc.Modifier) (*Target, error) {
	if !slices.Contains(textureFormats, format) {
		return nil, fmt.Errorf("gpu: no target in DRM format %s (%v)", format, textureFormats)
	}
	t, err := d.exportedTarget(width, height, format, modifiers)
	if err != nil {
		return nil, err
	}
	d.targets[t] = struct{}{}
	return t, nil
}

func (d *Device) exportedTarget(width, height int, format fourcc.Format, modifiers []fourcc.Modifier) (*Target, error) {
	vf := pixelFormats[format].vk
	e, err := d.export(width, height, format, modifiers, renderUse)
	if err != nil {
		return nil, err
	}
	return &Target{s: surface{d: d, w: width, h: height, vf: vf, layout: vk.ImageLayoutUndefined, image: e.image}, memory: []vk.DeviceMemory{e.memory}, buf: e.buf}, nil
}

// Texture is an offscreen target as a texture a later Pass samples:
// what the target's last pass drew, premultiplied ARGB8888. It lives
// as long as the target; a pass cannot draw a target into itself.
func (t *Target) Texture() (*Texture, error) {
	switch {
	case t.closed:
		return nil, errors.New("gpu: Texture of a closed target")
	case !t.offscreen:
		return nil, errors.New("gpu: only an offscreen target is sampled; a dmabuf target belongs to its consumer")
	case t.tex != nil:
		return t.tex, nil
	}
	if err := t.s.ready(); err != nil {
		return nil, err
	}
	format := fourcc.ARGB8888
	if t.s.vf == floatTargetFormat {
		format = fourcc.ABGR16161616F
	}
	t.tex = &Texture{d: t.s.d, format: format, pf: pixelFormats[format], w: t.s.w, h: t.s.h, view: t.s.view, surf: &t.s}
	return t.tex, nil
}

// Dmabuf describes the target's memory for its consumer. Its fd is the
// target's, open until Close; an offscreen target has none.
func (t *Target) Dmabuf() dmabuf.Buffer { return t.buf }

// Size is the target's size in pixels.
func (t *Target) Size() image.Point { return image.Pt(t.buf.Width, t.buf.Height) }

// Close releases the target and its dmabuf fd; closing it again, or
// after its device, does nothing.
func (t *Target) Close() {
	if t.closed {
		return
	}
	t.closed = true
	d := t.s.d
	d.adoptTargets()
	delete(d.targets, t)
	if t.tex != nil {
		t.tex.closed = true
	}
	for _, p := range t.buf.Planes {
		_ = syscall.Close(p.FD)
	}
	t.buf.Planes = nil
	d.retire(t)
}

// acquire records taking t back from its consumer for drawing; its
// contents stand, except before its first frame.
func (t *Target) acquire(cb vk.CommandBuffer) {
	if t.held {
		return
	}
	t.held = true
	if t.offscreen || t.s.layout == vk.ImageLayoutUndefined {
		return
	}
	t.s.barrier(cb, vk.ImageLayoutGeneral, vk.QueueFamilyForeignExt, t.s.d.family)
}

// release records handing t to its consumer outside this device: a
// queue-family release to the foreign family, its writes made visible.
func (t *Target) release(cb vk.CommandBuffer) {
	t.held = false
	if t.offscreen {
		return
	}
	t.s.barrier(cb, vk.ImageLayoutGeneral, t.s.d.family, vk.QueueFamilyForeignExt)
}

// clear fills t with the premultiplied color rgba and hands it to its
// consumer.
func (t *Target) clear(rgba [4]float32) error {
	return t.s.d.submitAsync(func(cb vk.CommandBuffer) {
		t.acquire(cb)
		t.s.to(cb, vk.ImageLayoutTransferDstOptimal)
		var col vk.ClearColorValue
		copy(col[:], unsafe.Slice((*byte)(unsafe.Pointer(&rgba[0])), len(col)))
		t.s.d.cmds.CmdClearColorImage(cb, t.s.image, vk.ImageLayoutTransferDstOptimal, &col, 1, &colorRange)
		t.release(cb)
	}, submission{})
}

// Carry is what a consumer rotating through targets copies into the one
// it reuses before drawing: Rects of From, which was shown since, the
// rest of the target already being up to date. From is read where it
// stands, since it may still be on screen. A rect r takes From's pixels at r
// moved by Offset, as scrolling does, and the part of r without a source stays
// as it was. From may be the target itself when Offset is not zero: the pixels
// then go through a scratch surface, as one image's overlapping copies are
// undefined.
type Carry struct {
	From   *Target
	Rects  []image.Rectangle
	Offset image.Point
}

func (d *Device) checkCarry(dst *Target, c Carry) error {
	src := c.From
	switch {
	case src == nil || src.closed:
		return errors.New("gpu: Render carrying from a closed target")
	case src == dst && c.Offset == (image.Point{}):
		return errors.New("gpu: Render carrying a target onto itself in place")
	case src.s.d != d:
		return errors.New("gpu: Render carrying across devices")
	case dst.Size() != src.Size() || dst.s.vf != src.s.vf:
		return fmt.Errorf("gpu: Render carrying from %v %s onto %v %s", src.Size(), src.buf.Format, dst.Size(), dst.buf.Format)
	case src.s.layout == vk.ImageLayoutUndefined:
		return errors.New("gpu: Render carrying from a target never drawn")
	}
	return nil
}

func (f *frame) recordCarry(cb vk.CommandBuffer, carry Carry) {
	d, dst, src := f.d, f.target, carry.From
	bounds := image.Rectangle{Max: dst.Size()}
	if src == dst {
		f.recordSelfCarry(cb, carry)
		return
	}
	layers := vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1}
	f.copies = f.copies[:0]
	for _, r := range carry.Rects {
		if r = r.Intersect(bounds).Intersect(bounds.Sub(carry.Offset)); r.Empty() {
			continue
		}
		f.copies = append(f.copies, vk.ImageCopy{
			SrcSubresource: layers, SrcOffset: offset3D(r.Min.Add(carry.Offset)), DstSubresource: layers, DstOffset: offset3D(r.Min),
			Extent: extent3D(r.Dx(), r.Dy()),
		})
	}
	if len(f.copies) == 0 {
		return
	}
	src.acquire(cb)
	srcLayout := vk.ImageLayoutGeneral
	if src.offscreen {
		src.s.to(cb, vk.ImageLayoutTransferSrcOptimal)
		srcLayout = vk.ImageLayoutTransferSrcOptimal
	}
	dst.s.to(cb, vk.ImageLayoutTransferDstOptimal)
	d.cmds.CmdCopyImage(cb, src.s.image, srcLayout, dst.s.image, vk.ImageLayoutTransferDstOptimal, count(f.copies), &f.copies[0])
	src.release(cb)
}

// recordSelfCarry moves pixels within the frame's own target: each rect's source
// into the filter surface at the rect's place, then all of them back.
func (f *frame) recordSelfCarry(cb vk.CommandBuffer, carry Carry) {
	d, dst, temp := f.d, f.target, f.d.filterTemp
	bounds := image.Rectangle{Max: dst.Size()}
	layers := vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1}
	f.copies, f.selfCopies = f.copies[:0], f.selfCopies[:0]
	for _, r := range carry.Rects {
		if r = r.Intersect(bounds).Intersect(bounds.Sub(carry.Offset)); r.Empty() {
			continue
		}
		at, size := offset3D(r.Min), extent3D(r.Dx(), r.Dy())
		f.copies = append(f.copies, vk.ImageCopy{SrcSubresource: layers, SrcOffset: offset3D(r.Min.Add(carry.Offset)), DstSubresource: layers, DstOffset: at, Extent: size})
		f.selfCopies = append(f.selfCopies, vk.ImageCopy{SrcSubresource: layers, SrcOffset: at, DstSubresource: layers, DstOffset: at, Extent: size})
	}
	if len(f.copies) == 0 {
		return
	}
	dst.s.to(cb, vk.ImageLayoutTransferSrcOptimal)
	temp.to(cb, vk.ImageLayoutTransferDstOptimal)
	d.cmds.CmdCopyImage(cb, dst.s.image, vk.ImageLayoutTransferSrcOptimal, temp.image, vk.ImageLayoutTransferDstOptimal, count(f.copies), &f.copies[0])
	temp.to(cb, vk.ImageLayoutTransferSrcOptimal)
	dst.s.to(cb, vk.ImageLayoutTransferDstOptimal)
	d.cmds.CmdCopyImage(cb, temp.image, vk.ImageLayoutTransferSrcOptimal, dst.s.image, vk.ImageLayoutTransferDstOptimal, count(f.selfCopies), &f.selfCopies[0])
}

// ReadPixels copies the target back into memory as premultiplied
// RGBA: tests and screenshots.
func (t *Target) ReadPixels() (*image.RGBA, error) {
	if t.s.vf == floatTargetFormat {
		return nil, errors.New("gpu: a float target's pixels are not read back")
	}
	w, h := t.buf.Width, t.buf.Height
	return t.s.d.readPixels(w, h, t.s.vf, false, func(cb vk.CommandBuffer, staging *buffer) {
		t.acquire(cb)
		t.s.download(cb, image.Rect(0, 0, w, h), staging, 0)
		t.s.d.hostReadBarrier(cb)
		t.release(cb)
	})
}

// readPixels reads a w x h image in vf back as 8-bit RGBA: record
// copies it, tight, into the staging buffer it is handed. opaque reads
// every alpha as 255, for a format whose alpha is padding.
func (d *Device) readPixels(w, h int, vf vk.Format, opaque bool, record func(cb vk.CommandBuffer, staging *buffer)) (*image.RGBA, error) {
	staging, err := d.newReadBuffer(w*h*4, vk.BufferUsageTransferDstBit, nil)
	if err != nil {
		return nil, err
	}
	defer staging.close()
	if err := d.submit(func(cb vk.CommandBuffer) { record(cb, staging) }); err != nil {
		return nil, err
	}
	if err := staging.invalidate(); err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	src := staging.mapped
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = decode(vf, [4]byte(src[i:]))
		if opaque {
			img.Pix[i+3] = 0xff
		}
	}
	return img, nil
}

// Modifiers are the DRM modifiers the device renders format into as a
// single-plane color attachment: what to intersect with the ones a
// consumer imports before NewTarget.
func (d *Device) Modifiers(format fourcc.Format) ([]fourcc.Modifier, error) {
	return d.modifiers(format, renderUse)
}
