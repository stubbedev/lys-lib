package gpu

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"slices"
	"unsafe"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// Texture is an image a Pass samples, kept across frames: one the
// device allocates and the CPU uploads into (NewTexture), or a dmabuf
// imported from its producer (ImportTexture). Close it
// when done; the device closes the ones still open with it.
type Texture struct {
	d      *Device
	format fourcc.Format
	pf     pixelFormat
	w, h   int
	image  vk.Image
	memory []vk.DeviceMemory
	view   vk.ImageView
	sets   [2]textureSet
	layout vk.ImageLayout
	ycbcr  *ycbcrKind
	yuv444 *yuv444Kind
	// planes marks a YUV texture sampled through planeImages: a view each
	// for Y, Cb and Cr.
	planes      *planesKind
	planeImages []vk.Image
	planeViews  [3]vk.ImageView
	surf        *surface
	foreign     bool
	// nearest marks a texture its driver filters no other way.
	nearest bool
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
		return fmt.Errorf("gpu: vkCreateImage %dx%d texture: %w", t.w, t.h, resultError(r))
	}
	var req vk.MemoryRequirements
	d.cmds.GetImageMemoryRequirements(d.device, t.image, &req)
	mem, err := d.allocate(req, vk.MemoryPropertyDeviceLocalBit, nil)
	if err != nil {
		return err
	}
	t.memory = []vk.DeviceMemory{mem}
	if r := d.cmds.BindImageMemory(d.device, t.image, mem, 0); r != vk.Success {
		return fmt.Errorf("gpu: vkBindImageMemory: %w", resultError(r))
	}
	if err := t.createView(); err != nil {
		return err
	}
	cb, err := d.batchCommands()
	if err != nil {
		return err
	}
	fullBarrier(d, cb, t.image, vk.ImageLayoutUndefined, vk.ImageLayoutTransferDstOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
	var clear vk.ClearColorValue
	d.cmds.CmdClearColorImage(cb, t.image, vk.ImageLayoutTransferDstOptimal, &clear, 1, &colorRange)
	fullBarrier(d, cb, t.image, vk.ImageLayoutTransferDstOptimal, vk.ImageLayoutShaderReadOnlyOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
	return d.endBatchOp()
}

func (t *Texture) createView() error {
	vci := vk.ImageViewCreateInfo{
		SType: vk.StructureTypeImageViewCreateInfo, Image: t.image, ViewType: vk.ImageViewType2d, Format: t.pf.vk,
		Components: t.pf.components(), SubresourceRange: colorRange,
	}
	info := vk.SamplerYcbcrConversionInfo{SType: vk.StructureTypeSamplerYcbcrConversionInfo}
	if t.ycbcr != nil {
		info.Conversion = t.ycbcr.conv
		vci.PNext = (*uintptr)(unsafe.Pointer(&info))
	}
	r := t.d.cmds.CreateImageView(t.d.device, &vci, nil, &t.view)
	runtime.KeepAlive(&info)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateImageView (texture): %w", resultError(r))
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
	if t.surf != nil {
		return errors.New("gpu: a target's texture is drawn by a Pass, not uploaded")
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
	at, err := d.reserveUpload(row * r.Dy())
	if err != nil {
		return err
	}
	if stride == row {
		copy(d.upload.mapped[at:], pix[:n])
	} else {
		dst := d.upload.mapped[at:]
		for y := range r.Dy() {
			copy(dst[y*row:], pix[y*stride:y*stride+row])
		}
	}
	cb, err := d.stagedCommands(row * r.Dy())
	if err != nil {
		return err
	}
	fullBarrier(d, cb, t.image, vk.ImageLayoutShaderReadOnlyOptimal, vk.ImageLayoutTransferDstOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
	region := &d.args.region
	*region = vk.BufferImageCopy{
		BufferOffset:     intconv.Must[vk.DeviceSize](at),
		BufferRowLength:  intconv.Must[uint32](r.Dx()),
		ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
		ImageOffset:      offset3D(r.Min),
		ImageExtent:      extent3D(r.Dx(), r.Dy()),
	}
	d.cmds.CmdCopyBufferToImage(cb, d.upload.buf, t.image, vk.ImageLayoutTransferDstOptimal, 1, region)
	fullBarrier(d, cb, t.image, vk.ImageLayoutTransferDstOptimal, vk.ImageLayoutShaderReadOnlyOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
	if d.batchBytes >= flushBatchBytes {
		return d.flushBatch()
	}
	return d.endBatchOp()
}

// stagedCommands is the open batch's command buffer for a command that
// reads the n bytes last reserved in the staging ring (reserveUpload):
// the batch's submission holds them until it has finished.
func (d *Device) stagedCommands(n int) (vk.CommandBuffer, error) {
	cb, err := d.batchCommands()
	if err != nil {
		return 0, err
	}
	if d.batched == 0 {
		d.uploads.pending++
	}
	d.batched++
	d.batchEnd = d.uploads.head
	d.batchBytes += n
	return cb, nil
}

func (d *Device) batchCommands() (vk.CommandBuffer, error) {
	if d.batch != 0 {
		return d.batch, nil
	}
	cb, err := d.recording()
	if err != nil {
		return 0, err
	}
	d.batch = cb
	return cb, nil
}

func (d *Device) endBatchOp() error {
	d.batchOps++
	if d.batchOps >= maxBatchedUploads {
		return d.flushBatch()
	}
	return nil
}

// maxBatchedUploads bounds the command memory a loop of updates, new
// textures and passes with no submission can pile up.
const maxBatchedUploads = 64

const flushBatchBytes = 1 << 20

// maxUploadRing is how far a full staging ring grows rather than make
// Update wait for the GPU.
const maxUploadRing = 64 << 20

// reserveUpload is the offset of n free bytes in the staging ring,
// waiting for earlier uploads only when the ring is full.
func (d *Device) reserveUpload(n int) (int, error) {
	if d.upload == nil || len(d.upload.mapped) < n {
		if err := d.growUpload(n); err != nil {
			return 0, err
		}
	}
	if at, ok := d.uploads.reserve(n); ok {
		return at, nil
	}
	if err := d.flushBatch(); err != nil {
		return 0, err
	}
	d.reap(false)
	for {
		if at, ok := d.uploads.reserve(n); ok {
			return at, nil
		}
		if size := len(d.upload.mapped); size < maxUploadRing {
			if err := d.growUpload(size); err != nil {
				return 0, err
			}
			continue
		}
		if err := d.waitOldest(); err != nil {
			return 0, err
		}
	}
}

func (d *Device) growUpload(n int) error {
	grown, err := d.newBuffer(max(2*n, stagingMin), vk.BufferUsageTransferSrcBit)
	if err != nil {
		return err
	}
	if d.upload != nil {
		d.retire(d.upload)
	}
	for i := range d.inflight {
		d.inflight[i].staged = false
	}
	d.batched = 0
	d.upload = grown
	d.uploads = stagingRing{size: len(grown.mapped)}
	return nil
}

// Close releases the texture; closing it again, after its device, or
// closing a target's texture, which its target owns, does nothing.
func (t *Texture) Close() {
	if t.closed || t.surf != nil {
		return
	}
	t.closed = true
	delete(t.d.textures, t)
	t.d.retire(t)
}

func (t *Texture) release() {
	d := t.d
	for i := range t.sets {
		t.sets[i].free(d)
	}
	if t.surf != nil {
		t.view = 0
		return
	}
	if t.view != 0 {
		d.cmds.DestroyImageView(d.device, t.view, nil)
		t.view = 0
	}
	if t.image != 0 {
		d.cmds.DestroyImage(d.device, t.image, nil)
		t.image = 0
	}
	for i, v := range t.planeViews {
		if v != 0 {
			d.cmds.DestroyImageView(d.device, v, nil)
			t.planeViews[i] = 0
		}
	}
	for _, img := range t.planeImages {
		d.cmds.DestroyImage(d.device, img, nil)
	}
	t.planeImages = nil
	d.freeMemory(t.memory)
	t.memory = nil
}

func (t *Texture) acquireForeign(cb vk.CommandBuffer) {
	t.foreignBarrier(cb, vk.ImageLayoutShaderReadOnlyOptimal, vk.QueueFamilyForeignExt, t.d.family)
}

func (t *Texture) releaseForeign(cb vk.CommandBuffer) {
	t.foreignBarrier(cb, vk.ImageLayoutGeneral, t.d.family, vk.QueueFamilyForeignExt)
}

func (t *Texture) foreignBarrier(cb vk.CommandBuffer, layout vk.ImageLayout, src, dst uint32) {
	if t.image != 0 {
		fullBarrier(t.d, cb, t.image, t.layout, layout, src, dst)
	}
	for _, img := range t.planeImages {
		fullBarrier(t.d, cb, img, t.layout, layout, src, dst)
	}
	t.layout = layout
}
