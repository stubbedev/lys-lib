package gpu

import (
	"fmt"
	"image"
	"unsafe"

	"github.com/stubbedev/lys-ui/dmabuf"
	"github.com/stubbedev/lys-ui/third_party/vk"
)

// Target is an image the GPU renders into whose memory is a dmabuf: a
// window's frame handed to the compositor, or an output's scanout
// buffer. Its layout (the DRM modifier) is one the caller offered, as
// the consumer imports it. Between frames it belongs to the consumer.
type Target struct {
	s      surface
	memory vk.DeviceMemory
	buf    dmabuf.Buffer
	// held is whether the device holds t, between acquire and release.
	held bool
	// offscreen marks a target no consumer imports (NewOffscreen).
	offscreen bool
	// closed marks a target Close released.
	closed bool
}

// NewOffscreen allocates a width x height target in device memory no
// other process imports: a frame rendered for its pixels (ReadPixels),
// a screenshot or a test.
func (d *Device) NewOffscreen(width, height int) (*Target, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("gpu: target size %dx%d is not positive", width, height)
	}
	s, err := d.newSurface(width, height)
	if err != nil {
		return nil, err
	}
	t := &Target{s: *s, offscreen: true, buf: dmabuf.Buffer{Width: width, Height: height}}
	d.targets[t] = struct{}{}
	return t, nil
}

// vkFormatOf maps a DRM fourcc to the Vulkan format with the same byte
// order in memory.
func vkFormatOf(fourcc uint32) (vk.Format, error) {
	switch fourcc {
	case dmabuf.FormatARGB8888, dmabuf.FormatXRGB8888:
		return vk.FormatB8g8r8a8Unorm, nil
	}
	return 0, fmt.Errorf("gpu: no Vulkan format for DRM format %#08x", fourcc)
}

// NewTarget allocates a width x height image in format (a DRM fourcc:
// dmabuf.FormatARGB8888, or FormatXRGB8888 with the same bytes) whose
// layout the driver picks among modifiers, the ones the consumer
// imports, that the device renders format in (Modifiers): linear when
// both take it. None in common, as with only the implicit
// dmabuf.ModifierInvalid on offer, is an error naming both lists.
func (d *Device) NewTarget(width, height int, format uint32, modifiers []uint64) (*Target, error) {
	vf, err := vkFormatOf(format)
	if err != nil {
		return nil, err
	}
	if vf != surfaceFormat {
		return nil, fmt.Errorf("gpu: DRM format %#08x renders as Vulkan format %d, surfaces are %d", format, vf, surfaceFormat)
	}
	e, err := d.export(width, height, format, modifiers, renderUse)
	if err != nil {
		return nil, err
	}
	t := &Target{s: surface{d: d, w: width, h: height, layout: vk.ImageLayoutUndefined, image: e.image}, memory: e.memory, buf: e.buf}
	d.targets[t] = struct{}{}
	return t, nil
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
	delete(d.targets, t)
	t.s.close()
	e := exported{image: t.s.image, memory: t.memory, buf: t.buf}
	e.close(d)
	t.s.image, t.memory, t.buf.Planes = 0, 0, nil
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
	return t.s.d.submit(func(cb vk.CommandBuffer) {
		t.acquire(cb)
		t.s.to(cb, vk.ImageLayoutTransferDstOptimal)
		var col vk.ClearColorValue
		copy(col[:], unsafe.Slice((*byte)(unsafe.Pointer(&rgba[0])), len(col)))
		t.s.d.cmds.CmdClearColorImage(cb, t.s.image, vk.ImageLayoutTransferDstOptimal, &col, 1, &colorRange)
		t.release(cb)
	})
}

// ReadPixels copies the target back into memory as premultiplied
// RGBA: tests and screenshots.
func (t *Target) ReadPixels() (*image.RGBA, error) {
	d := t.s.d
	w, h := t.buf.Width, t.buf.Height
	staging, err := d.newBuffer(w*h*4, vk.BufferUsageTransferDstBit)
	if err != nil {
		return nil, err
	}
	defer staging.close()
	err = d.submit(func(cb vk.CommandBuffer) {
		t.acquire(cb)
		t.s.download(cb, image.Rect(0, 0, w, h), staging, 0)
		t.release(cb)
	})
	if err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	src := staging.mapped
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = src[i+2], src[i+1], src[i], src[i+3]
	}
	return img, nil
}

// Modifiers are the DRM modifiers the device renders format into as a
// single-plane color attachment: what to intersect with the ones a
// consumer imports before NewTarget.
func (d *Device) Modifiers(format uint32) ([]uint64, error) { return d.modifiers(format, renderUse) }
