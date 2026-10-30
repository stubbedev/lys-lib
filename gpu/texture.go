package gpu

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// Texture is an image a Pass samples, kept across frames: one the
// device allocates and the CPU uploads into (NewTexture), or a dmabuf
// imported from its producer (ImportTexture). Close it
// when done; the device closes the ones still open with it.
type Texture struct {
	d       *Device
	format  fourcc.Format
	pf      pixelFormat
	w, h    int
	image   vk.Image
	memory  []vk.DeviceMemory
	view    vk.ImageView
	sets    [2]textureSet
	layout  vk.ImageLayout
	foreign bool
	closed  bool
}

// NewTexture allocates a width x height texture in format, one of
// TextureFormats; the X formats sample opaque. It holds transparent
// black until Update writes it.
func (d *Device) NewTexture(format fourcc.Format, width, height int) (*Texture, error) {
	if !slices.Contains(textureFormats, format) {
		return nil, fmt.Errorf("gpu: no texture in DRM format %s (%v)", format, textureFormats)
	}
	if err := d.checkSize(width, height); err != nil {
		return nil, err
	}
	t := &Texture{d: d, format: format, pf: pixelFormats[format], w: width, h: height}
	if err := t.create(); err != nil {
		t.release()
		return nil, err
	}
	d.textures[t] = struct{}{}
	return t, nil
}

func (t *Texture) create() error {
	d := t.d
	ici := vk.ImageCreateInfo{
		SType: vk.StructureTypeImageCreateInfo, ImageType: vk.ImageType2d, Format: t.pf.vk,
		Extent: extent3D(t.w, t.h), MipLevels: 1, ArrayLayers: 1,
		Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingOptimal,
		Usage:       vk.ImageUsageFlags(vk.ImageUsageTransferDstBit | vk.ImageUsageTransferSrcBit | vk.ImageUsageSampledBit),
		SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined,
	}
	if r := d.cmds.CreateImage(d.device, &ici, nil, &t.image); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateImage %dx%d texture: %d", t.w, t.h, r)
	}
	var req vk.MemoryRequirements
	d.cmds.GetImageMemoryRequirements(d.device, t.image, &req)
	mem, err := d.allocate(req, vk.MemoryPropertyDeviceLocalBit, nil)
	if err != nil {
		return err
	}
	t.memory = []vk.DeviceMemory{mem}
	if r := d.cmds.BindImageMemory(d.device, t.image, mem, 0); r != vk.Success {
		return fmt.Errorf("gpu: vkBindImageMemory: %d", r)
	}
	if err := t.createView(); err != nil {
		return err
	}
	return d.submit(func(cb vk.CommandBuffer) {
		fullBarrier(d, cb, t.image, vk.ImageLayoutUndefined, vk.ImageLayoutTransferDstOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
		var clear vk.ClearColorValue
		d.cmds.CmdClearColorImage(cb, t.image, vk.ImageLayoutTransferDstOptimal, &clear, 1, &colorRange)
		fullBarrier(d, cb, t.image, vk.ImageLayoutTransferDstOptimal, vk.ImageLayoutShaderReadOnlyOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
	})
}

func (t *Texture) createView() error {
	vci := vk.ImageViewCreateInfo{
		SType: vk.StructureTypeImageViewCreateInfo, Image: t.image, ViewType: vk.ImageViewType2d, Format: t.pf.vk,
		Components: t.pf.components(), SubresourceRange: colorRange,
	}
	if r := t.d.cmds.CreateImageView(t.d.device, &vci, nil, &t.view); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateImageView (texture): %d", r)
	}
	return nil
}

// Size is the texture's size in pixels.
func (t *Texture) Size() image.Point { return image.Pt(t.w, t.h) }

// Format is the texture's DRM format.
func (t *Texture) Format() fourcc.Format { return t.format }

// Update copies pix into r of the texture, rows stride bytes apart;
// the rest of the texture keeps what it held.
func (t *Texture) Update(r image.Rectangle, stride int, pix []byte) error {
	if t.closed {
		return errors.New("gpu: update of a closed texture")
	}
	if t.foreign {
		return errors.New("gpu: an imported texture is written by its producer, not uploaded")
	}
	if r.Empty() || !r.In(image.Rectangle{Max: t.Size()}) {
		return fmt.Errorf("gpu: update %v outside a %dx%d texture", r, t.w, t.h)
	}
	px := t.pf.size
	row := r.Dx() * px
	if stride < row || stride%px != 0 {
		return fmt.Errorf("gpu: stride %d for rows of %d bytes in %d-byte pixels", stride, row, px)
	}
	n := stride*(r.Dy()-1) + row
	if len(pix) < n {
		return fmt.Errorf("gpu: %d bytes for %v at stride %d, want %d", len(pix), r, stride, n)
	}
	d := t.d
	if err := d.fitUpload(n); err != nil {
		return err
	}
	copy(d.upload.mapped, pix[:n])
	return d.submit(func(cb vk.CommandBuffer) {
		fullBarrier(d, cb, t.image, vk.ImageLayoutShaderReadOnlyOptimal, vk.ImageLayoutTransferDstOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
		region := vk.BufferImageCopy{
			BufferRowLength:  intconv.Must[uint32](stride / px),
			ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
			ImageOffset:      offset3D(r.Min),
			ImageExtent:      extent3D(r.Dx(), r.Dy()),
		}
		d.cmds.CmdCopyBufferToImage(cb, d.upload.buf, t.image, vk.ImageLayoutTransferDstOptimal, 1, &region)
		fullBarrier(d, cb, t.image, vk.ImageLayoutTransferDstOptimal, vk.ImageLayoutShaderReadOnlyOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
	})
}

func (d *Device) fitUpload(n int) error {
	if d.upload != nil {
		var err error
		d.upload, err = d.upload.fit(n)
		return err
	}
	b, err := d.newBuffer(max(n, stagingMin), vk.BufferUsageTransferSrcBit)
	if err != nil {
		return err
	}
	d.upload = b
	return nil
}

// Close releases the texture; closing it again, or after its device,
// does nothing.
func (t *Texture) Close() {
	if t.closed {
		return
	}
	t.closed = true
	delete(t.d.textures, t)
	t.release()
}

func (t *Texture) release() {
	d := t.d
	for i := range t.sets {
		t.sets[i].free(d)
	}
	if t.view != 0 {
		d.cmds.DestroyImageView(d.device, t.view, nil)
		t.view = 0
	}
	if t.image != 0 {
		d.cmds.DestroyImage(d.device, t.image, nil)
		t.image = 0
	}
	d.freeMemory(t.memory)
	t.memory = nil
}

func (t *Texture) acquireForeign(cb vk.CommandBuffer) {
	fullBarrier(t.d, cb, t.image, t.layout, vk.ImageLayoutShaderReadOnlyOptimal, vk.QueueFamilyForeignExt, t.d.family)
	t.layout = vk.ImageLayoutShaderReadOnlyOptimal
}

func (t *Texture) releaseForeign(cb vk.CommandBuffer) {
	fullBarrier(t.d, cb, t.image, t.layout, vk.ImageLayoutGeneral, t.d.family, vk.QueueFamilyForeignExt)
	t.layout = vk.ImageLayoutGeneral
}
