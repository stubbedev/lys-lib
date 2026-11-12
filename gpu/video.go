package gpu

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"unsafe"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/media/demux"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// Codec is a video coding format the encoder writes.
type Codec int

// The codecs.
const (
	H264 Codec = iota
	H265
)

// codecs are every codec, each with its name and the encode operation
// on a video queue that codes it.
var codecs = []struct {
	codec Codec
	name  string
	op    videoOp
}{
	{H264, "H.264", videoOp{"VK_KHR_video_encode_h264", vk.VideoCodecOperationEncodeH264BitKhr}},
	{H265, "H.265", videoOp{"VK_KHR_video_encode_h265", vk.VideoCodecOperationEncodeH265BitKhr}},
}

func (c Codec) String() string { return codecs[c].name }

func (c Codec) extension() string { return codecs[c].op.extension }

func (c Codec) operation() vk.VideoCodecOperationFlagBitsKHR { return codecs[c].op.operation }

// decodeCodecs are the codecs the decoder reads, each with the decode
// operation on a video queue that codes it and what reads its stream,
// given the track's configuration record.
var decodeCodecs = []decodeEntry{
	{demux.H264, videoOp{"VK_KHR_video_decode_h264", vk.VideoCodecOperationDecodeH264BitKhr}, newH264Decoder},
	{demux.H265, videoOp{"VK_KHR_video_decode_h265", vk.VideoCodecOperationDecodeH265BitKhr}, newH265Decoder},
	{demux.AV1, videoOp{"VK_KHR_video_decode_av1", vk.VideoCodecOperationDecodeAv1BitKhr}, newAV1Decoder},
	{demux.VP9, videoOp{"VK_KHR_video_decode_vp9", vk.VideoCodecOperationDecodeVp9BitKhr}, newVP9Decoder},
}

// decodeEntry is a codec the decoder reads.
type decodeEntry struct {
	codec demux.Codec
	op    videoOp
	open  func(config []byte) (decodeCodec, error)
}

// videoOp is a codec operation of a video queue: the device extension
// it needs and its operation bit.
type videoOp struct {
	extension string
	operation vk.VideoCodecOperationFlagBitsKHR
}

// videoExtensions are what any video encoding needs, beside a codec's
// own extension; decodeExtensions are decoding's.
var (
	videoExtensions  = []string{"VK_KHR_video_queue", "VK_KHR_video_encode_queue"}
	decodeExtensions = []string{"VK_KHR_video_queue", "VK_KHR_video_decode_queue"}
)

// videoQueue is a device's video encode queue family and the codecs
// it encodes.
type videoQueue struct {
	family uint32
	queue  vk.Queue
	codecs []Codec
}

// decodeQueue is a device's video decode queue family and the codecs
// it decodes.
type decodeQueue struct {
	family uint32
	queue  vk.Queue
	codecs []demux.Codec
	// transfer is whether the family copies images: on a device that
	// decodes only into its reference pictures, whether it copies them
	// out itself or leaves that to the graphics queue.
	transfer bool
}

// probeSync2 reports whether p has Vulkan 1.3's synchronization2,
// which video coding and the encoders' conversion record with.
func (d *Device) probeSync2(p vk.PhysicalDevice, apiVersion uint32) bool {
	if apiVersion < vkVersion(1, 3) {
		return false
	}
	f13 := vk.PhysicalDeviceVulkan13Features{SType: vk.StructureTypePhysicalDeviceVulkan13Features}
	f2 := vk.PhysicalDeviceFeatures2{SType: vk.StructureTypePhysicalDeviceFeatures2, PNext: (*uintptr)(unsafe.Pointer(&f13))}
	d.cmds.GetPhysicalDeviceFeatures2(p, &f2)
	runtime.KeepAlive(&f13)
	return f13.Synchronization2 != 0
}

// probeVideo finds p's video encode queue family and the codecs it
// encodes: nil when there is none. why says, for each codec p does not
// encode, why not: no synchronization2, which video coding records
// with, no timeline semaphores, which hand a converted frame to the
// video queue, a missing extension, no queue family that encodes video,
// or none that codes the codec.
func (d *Device) probeVideo(p vk.PhysicalDevice, sync2, timelines bool, have map[string]bool) (*videoQueue, map[Codec]error) {
	ops := make([]videoOp, len(codecs))
	for i, c := range codecs {
		ops[i] = c.op
	}
	var missing error
	switch {
	case !sync2:
		missing = errors.New("no Vulkan 1.3 synchronization2")
	case !timelines:
		missing = errors.New("no timeline semaphores to hand converted frames to the video queue")
	}
	family, _, offered, reasons := d.probeVideoFamily(p, have, missing, vk.QueueVideoEncodeBitKhr, videoExtensions, ops)
	why := map[Codec]error{}
	var found *videoQueue
	for i, c := range codecs {
		if !offered[i] {
			why[c.codec] = reasons[i]
			continue
		}
		if found == nil {
			found = &videoQueue{family: family}
		}
		found.codecs = append(found.codecs, c.codec)
	}
	return found, why
}

// probeDecode finds p's video decode queue family and the codecs it
// decodes, as probeVideo does for encoding; decoding also needs the
// sampler YCbCr conversion the renderer samples its pictures through,
// the timeline semaphores its queue hands them to the graphics queue
// with, and VP9 the videoDecodeVP9 feature.
func (d *Device) probeDecode(p vk.PhysicalDevice, sync2, timelines, ycbcr bool, have map[string]bool) (*decodeQueue, map[demux.Codec]error) {
	ops := make([]videoOp, len(decodeCodecs))
	for i, c := range decodeCodecs {
		ops[i] = c.op
	}
	var missing error
	switch {
	case !sync2:
		missing = errors.New("no Vulkan 1.3 synchronization2")
	case !ycbcr:
		missing = errors.New("no sampler YCbCr conversion to sample decoded pictures")
	case !timelines:
		missing = errors.New("no timeline semaphores to hand decoded pictures to the graphics queue")
	}
	family, queueFlags, offered, reasons := d.probeVideoFamily(p, have, missing, vk.QueueVideoDecodeBitKhr, decodeExtensions, ops)
	why := map[demux.Codec]error{}
	var found *decodeQueue
	for i, c := range decodeCodecs {
		if !offered[i] {
			why[c.codec] = reasons[i]
			continue
		}
		if c.codec == demux.VP9 && !d.probeVP9(p) {
			why[c.codec] = errors.New("no videoDecodeVP9 feature")
			continue
		}
		if found == nil {
			found = &decodeQueue{family: family, transfer: queueFlags&vk.QueueFlags(vk.QueueTransferBit) != 0}
		}
		found.codecs = append(found.codecs, c.codec)
	}
	return found, why
}

// probeVideoFamily finds the first of p's queue families with flag
// that offers one of ops whose extension p has, beside every one of
// queueExts, and its queue flags. offered says which of ops that family
// codes, why why each
// other one is not: missing (when not nil, for all of them), a missing
// extension, no family with flag, or none with the operation.
func (d *Device) probeVideoFamily(p vk.PhysicalDevice, have map[string]bool, missing error, flag vk.QueueFlagBits, queueExts []string, ops []videoOp) (family uint32, queueFlags vk.QueueFlags, offered []bool, why []error) {
	offered, why = make([]bool, len(ops)), make([]error, len(ops))
	none := func(err error) (uint32, vk.QueueFlags, []bool, []error) {
		for i := range why {
			why[i] = err
		}
		return 0, 0, offered, why
	}
	if missing != nil {
		return none(missing)
	}
	var absent []string
	for _, ext := range queueExts {
		if !have[ext] {
			absent = append(absent, ext)
		}
	}
	if len(absent) > 0 {
		return none(fmt.Errorf("no %s", strings.Join(absent, ", ")))
	}
	var n uint32
	d.cmds.GetPhysicalDeviceQueueFamilyProperties2(p, &n, nil)
	vprops := make([]vk.QueueFamilyVideoPropertiesKHR, n)
	props := make([]vk.QueueFamilyProperties2, n)
	for i := range props {
		vprops[i].SType = vk.StructureTypeQueueFamilyVideoPropertiesKhr
		props[i] = vk.QueueFamilyProperties2{SType: vk.StructureTypeQueueFamilyProperties2, PNext: (*uintptr)(unsafe.Pointer(&vprops[i]))}
	}
	if n > 0 {
		d.cmds.GetPhysicalDeviceQueueFamilyProperties2(p, &n, &props[0])
	}
	runtime.KeepAlive(vprops)
	var families []string
	found := false
	for i, fp := range props[:n] {
		if fp.QueueFamilyProperties.QueueFlags&flags[vk.QueueFlags](flag) == 0 {
			continue
		}
		codecOps := vprops[i].VideoCodecOperations
		families = append(families, fmt.Sprintf("%d (operations %#x)", i, codecOps))
		if found {
			continue
		}
		for j, op := range ops {
			offered[j] = have[op.extension] && codecOps&flags[vk.VideoCodecOperationFlagsKHR](op.operation) != 0
		}
		if found = slices.Contains(offered, true); found {
			family, queueFlags = uint32(i), fp.QueueFamilyProperties.QueueFlags
		}
	}
	if len(families) == 0 {
		return none(fmt.Errorf("no queue family has flag %#x", flag))
	}
	for i, op := range ops {
		switch {
		case offered[i]:
		case !have[op.extension]:
			why[i] = fmt.Errorf("no %s", op.extension)
		default:
			why[i] = fmt.Errorf("no queue family with flag %#x has operation %#x; families %s", flag, op.operation, strings.Join(families, ", "))
		}
	}
	return family, queueFlags, offered, why
}

// extensions are the device extensions encoding on v needs.
func (v *videoQueue) extensions() []string {
	exts := slices.Clone(videoExtensions)
	for _, c := range v.codecs {
		exts = append(exts, c.extension())
	}
	return exts
}

// extensions are the device extensions decoding on q needs.
func (q *decodeQueue) extensions() []string {
	exts := slices.Clone(decodeExtensions)
	for _, c := range decodeCodecs {
		if slices.Contains(q.codecs, c.codec) {
			exts = append(exts, c.op.extension)
		}
	}
	return exts
}

// probeTimeline reports whether p, a Vulkan 1.3 device, has timeline
// semaphores, which a device that codes video enables.
func (d *Device) probeTimeline(p vk.PhysicalDevice) bool {
	timeline := vk.PhysicalDeviceTimelineSemaphoreFeatures{SType: vk.StructureTypePhysicalDeviceTimelineSemaphoreFeatures}
	f2 := vk.PhysicalDeviceFeatures2{SType: vk.StructureTypePhysicalDeviceFeatures2, PNext: (*uintptr)(unsafe.Pointer(&timeline))}
	d.cmds.GetPhysicalDeviceFeatures2(p, &f2)
	runtime.KeepAlive(&timeline)
	return timeline.TimelineSemaphore != 0
}

// probeVP9 reports whether p has the videoDecodeVP9 feature, which
// VK_KHR_video_decode_vp9 decodes with once enabled.
func (d *Device) probeVP9(p vk.PhysicalDevice) bool {
	vp9 := vk.PhysicalDeviceVideoDecodeVP9FeaturesKHR{SType: vk.StructureTypePhysicalDeviceVideoDecodeVp9FeaturesKhr}
	f2 := vk.PhysicalDeviceFeatures2{SType: vk.StructureTypePhysicalDeviceFeatures2, PNext: (*uintptr)(unsafe.Pointer(&vp9))}
	d.cmds.GetPhysicalDeviceFeatures2(p, &f2)
	runtime.KeepAlive(&vp9)
	return vp9.VideoDecodeVP9 != 0
}

// chromaFormat is how a stream's pictures subsample their chroma.
type chromaFormat uint8

// The chroma formats: half the luma's resolution both ways, half across
// alone, or the luma's.
const (
	chroma420 chromaFormat = iota
	chroma422
	chroma444
)

func (c chromaFormat) String() string { return [...]string{"4:2:0", "4:2:2", "4:4:4"}[c] }

// videoSamples is how a stream's pictures hold their samples: their
// depth in bits and their chroma format, which pick the pictures'
// format.
type videoSamples struct {
	bits   uint8
	chroma chromaFormat
}

// nv12Samples are 8-bit 4:2:0 pictures: what the encoder codes.
var nv12Samples = videoSamples{bits: 8, chroma: chroma420}

// vk is the format pictures of the samples are coded in: luma, then a
// plane of interleaved chroma, a sample of more than 8 bits in the high
// bits of its 16. It is VK_FORMAT_UNDEFINED for samples pictures have
// no format in.
func (s videoSamples) vk() vk.Format {
	switch s {
	case videoSamples{8, chroma420}:
		return nv12
	case videoSamples{10, chroma420}:
		return vk.FormatG10x6B10x6r10x62plane420Unorm3pack16
	case videoSamples{12, chroma420}:
		return vk.FormatG12x4B12x4r12x42plane420Unorm3pack16
	case videoSamples{8, chroma422}:
		return vk.FormatG8B8r82plane422Unorm
	case videoSamples{10, chroma422}:
		return vk.FormatG10x6B10x6r10x62plane422Unorm3pack16
	case videoSamples{12, chroma422}:
		return vk.FormatG12x4B12x4r12x42plane422Unorm3pack16
	case videoSamples{8, chroma444}:
		return vk.FormatG8B8r82plane444Unorm
	case videoSamples{10, chroma444}:
		return vk.FormatG10x6B10x6r10x62plane444Unorm3pack16
	case videoSamples{12, chroma444}:
		return vk.FormatG12x4B12x4r12x42plane444Unorm3pack16
	}
	return vk.FormatUndefined
}

// chromaOfIdc is the chroma format an H.264 or H.265 chroma_format_idc
// names; false for monochrome, which pictures have no format in.
func chromaOfIdc(idc uint8) (chromaFormat, bool) {
	if idc < 1 || idc > 3 {
		return 0, false
	}
	return chromaFormat(idc - 1), true
}

// chromaOfSubsampling is the chroma format an AV1 or VP9 stream's
// subsampling flags name; false for 4:4:0, chroma halved down alone,
// which pictures have no format in.
func chromaOfSubsampling(x, y bool) (chromaFormat, bool) {
	switch {
	case x && y:
		return chroma420, true
	case x:
		return chroma422, true
	case !y:
		return chroma444, true
	}
	return 0, false
}

// samplesOf are bits-bit samples in chroma; false for a depth pictures
// have no format in.
func samplesOf(bits int, chroma chromaFormat) (videoSamples, bool) {
	if bits < 0 || bits > 255 {
		return videoSamples{}, false
	}
	s := videoSamples{bits: uint8(bits), chroma: chroma}
	return s, s.vk() != vk.FormatUndefined
}

func (s videoSamples) String() string { return fmt.Sprintf("%d-bit %s", s.bits, s.chroma) }

// bytes is how many bytes a sample takes.
func (s videoSamples) bytes() int { return (int(s.bits) + 7) / 8 }

func (s videoSamples) planeLayout() yuvImageLayout {
	r, rg, bits := vk.FormatR8Unorm, vk.FormatR8g8Unorm, 8
	if s.bits > 8 {
		r, rg, bits = vk.FormatR16Unorm, vk.FormatR16g16Unorm, 16
	}
	switch s.chroma {
	case chroma422:
		return biplanar(r, rg, bits, 2, 1, false)
	case chroma444:
		return biplanar(r, rg, bits, 1, 1, false)
	}
	return biplanar(r, rg, bits, 2, 2, false)
}

// pixel is the samples' picture format as the renderer samples it,
// through a YCbCr conversion.
func (s videoSamples) pixel() pixelFormat { return pixelFormat{vk: s.vk(), planes: 2, ycbcr: true} }

func (s videoSamples) whole(size [2]int) [2]int {
	switch s.chroma {
	case chroma420:
		return [2]int{alignUp(size[0], 2), alignUp(size[1], 2)}
	case chroma422:
		return [2]int{alignUp(size[0], 2), size[1]}
	}
	return size
}

// chromaSize is the size of the chroma plane of a w x h picture.
func (s videoSamples) chromaSize(w, h int) (int, int) {
	switch s.chroma {
	case chroma420:
		return (w + 1) / 2, (h + 1) / 2
	case chroma422:
		return (w + 1) / 2, h
	}
	return w, h
}

// component is the depth as a video profile names it.
func (s videoSamples) component() vk.VideoComponentBitDepthFlagsKHR {
	switch s.bits {
	case 10:
		return vk.VideoComponentBitDepthFlagsKHR(vk.VideoComponentBitDepth10BitKhr)
	case 12:
		return vk.VideoComponentBitDepthFlagsKHR(vk.VideoComponentBitDepth12BitKhr)
	}
	return vk.VideoComponentBitDepthFlagsKHR(vk.VideoComponentBitDepth8BitKhr)
}

// subsampling is the chroma format as a video profile names it.
func (s videoSamples) subsampling() vk.VideoChromaSubsamplingFlagsKHR {
	switch s.chroma {
	case chroma422:
		return vk.VideoChromaSubsamplingFlagsKHR(vk.VideoChromaSubsampling422BitKhr)
	case chroma444:
		return vk.VideoChromaSubsamplingFlagsKHR(vk.VideoChromaSubsampling444BitKhr)
	}
	return vk.VideoChromaSubsamplingFlagsKHR(vk.VideoChromaSubsampling420BitKhr)
}

// videoTiling finds the tiling of samples' format among the formats the
// device codes pictures of usage in, in the profiles list names. none
// is what the device lacks when there is no such format:
// ErrNoVideoDecoder or ErrNoVideoEncoder.
func (d *Device) videoTiling(list *vk.VideoProfileListInfoKHR, usage vk.ImageUsageFlags, samples videoSamples, none error) (vk.ImageTiling, error) {
	fi := vk.PhysicalDeviceVideoFormatInfoKHR{SType: vk.StructureTypePhysicalDeviceVideoFormatInfoKhr, PNext: (*uintptr)(unsafe.Pointer(list)), ImageUsage: usage}
	var n uint32
	if r := d.cmds.GetPhysicalDeviceVideoFormatPropertiesKHR(d.phys, &fi, &n, nil); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkGetPhysicalDeviceVideoFormatPropertiesKHR usage %#x: %w", usage, resultError(r))
	}
	props := make([]vk.VideoFormatPropertiesKHR, n)
	for i := range props {
		props[i].SType = vk.StructureTypeVideoFormatPropertiesKhr
	}
	if n > 0 {
		if r := d.cmds.GetPhysicalDeviceVideoFormatPropertiesKHR(d.phys, &fi, &n, &props[0]); r != vk.Success {
			return 0, fmt.Errorf("gpu: vkGetPhysicalDeviceVideoFormatPropertiesKHR usage %#x: %w", usage, resultError(r))
		}
	}
	runtime.KeepAlive(list)
	for _, p := range props[:n] {
		if p.Format == samples.vk() && p.ImageType == vk.ImageType2d {
			return p.ImageTiling, nil
		}
	}
	return 0, fmt.Errorf("%w: %s codes no %s pictures for usage %#x (vkGetPhysicalDeviceVideoFormatPropertiesKHR lists %d formats, none of them VkFormat %d)", none, d.name, samples, usage, n, samples.vk())
}

// bindVideoSession allocates and binds the memory session needs.
func (d *Device) bindVideoSession(session vk.VideoSessionKHR) ([]vk.DeviceMemory, error) {
	var n uint32
	if r := d.cmds.GetVideoSessionMemoryRequirementsKHR(d.device, session, &n, nil); r != vk.Success {
		return nil, fmt.Errorf("gpu: vkGetVideoSessionMemoryRequirementsKHR: %w", resultError(r))
	}
	reqs := make([]vk.VideoSessionMemoryRequirementsKHR, n)
	for i := range reqs {
		reqs[i].SType = vk.StructureTypeVideoSessionMemoryRequirementsKhr
	}
	if n > 0 {
		if r := d.cmds.GetVideoSessionMemoryRequirementsKHR(d.device, session, &n, &reqs[0]); r != vk.Success {
			return nil, fmt.Errorf("gpu: vkGetVideoSessionMemoryRequirementsKHR: %w", resultError(r))
		}
	}
	mems := make([]vk.DeviceMemory, 0, n)
	binds := make([]vk.BindVideoSessionMemoryInfoKHR, 0, n)
	for _, req := range reqs[:n] {
		mem, err := d.allocate(req.MemoryRequirements, vk.MemoryPropertyDeviceLocalBit, nil)
		if errors.Is(err, errNoMemoryType) {
			mem, err = d.allocate(req.MemoryRequirements, 0, nil)
		}
		if err != nil {
			d.freeMemory(mems)
			return nil, fmt.Errorf("gpu: video session memory %d: %w", req.MemoryBindIndex, err)
		}
		mems = append(mems, mem)
		binds = append(binds, vk.BindVideoSessionMemoryInfoKHR{
			SType: vk.StructureTypeBindVideoSessionMemoryInfoKhr, MemoryBindIndex: req.MemoryBindIndex, Memory: mem, MemorySize: req.MemoryRequirements.Size,
		})
	}
	if len(binds) > 0 {
		if r := d.cmds.BindVideoSessionMemoryKHR(d.device, session, count(binds), &binds[0]); r != vk.Success {
			d.freeMemory(mems)
			return nil, fmt.Errorf("gpu: vkBindVideoSessionMemoryKHR: %w", resultError(r))
		}
	}
	return mems, nil
}

// videoImage is a picture of a video session: its image, memory and
// the views the coding commands name it by (one per array layer).
type videoImage struct {
	image  vk.Image
	memory vk.DeviceMemory
	views  []vk.ImageView
}

func (vi *videoImage) close(d *Device) {
	for _, v := range vi.views {
		d.cmds.DestroyImageView(d.device, v, nil)
	}
	vi.views = nil
	if vi.image != 0 {
		d.cmds.DestroyImage(d.device, vi.image, nil)
		vi.image = 0
	}
	if vi.memory != 0 {
		d.cmds.FreeMemory(d.device, vi.memory, nil)
		vi.memory = 0
	}
}

// codingUsage are the usages of a picture the coding commands name.
const codingUsage = vk.ImageUsageFlags(vk.ImageUsageVideoDecodeDstBitKhr | vk.ImageUsageVideoDecodeDpbBitKhr | vk.ImageUsageVideoEncodeSrcBitKhr | vk.ImageUsageVideoEncodeDpbBitKhr)

// pictureSpec is a picture newPicture makes: its size in luma samples,
// its samples, the profiles of the session that codes it, its tiling as
// videoTiling found it for usage, its create flags, its array layers,
// and the video queue family that codes it, which shares it with the
// graphics queue when shared.
type pictureSpec struct {
	extent   [2]int
	samples  videoSamples
	profiles *vk.VideoProfileListInfoKHR
	tiling   vk.ImageTiling
	usage    vk.ImageUsageFlags
	flags    vk.ImageCreateFlags
	layers   int
	family   uint32
	shared   bool
}

// newPicture makes the picture s describes. One the coding commands
// name (its usage has a coding usage) is made for the session's
// profiles, with a view of each layer for them.
func (d *Device) newPicture(s pictureSpec) (videoImage, error) {
	ici := vk.ImageCreateInfo{
		SType: vk.StructureTypeImageCreateInfo, Flags: s.flags,
		ImageType: vk.ImageType2d, Format: s.samples.vk(), Extent: extent3D(s.extent[0], s.extent[1]),
		MipLevels: 1, ArrayLayers: intconv.Must[uint32](s.layers), Samples: vk.SampleCount1Bit, Tiling: s.tiling,
		Usage: s.usage, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined,
	}
	coding := s.usage & codingUsage
	if coding != 0 {
		ici.PNext = (*uintptr)(unsafe.Pointer(s.profiles))
	}
	families := [2]uint32{d.family, s.family}
	if s.shared && families[0] != families[1] {
		ici.SharingMode, ici.QueueFamilyIndexCount, ici.PQueueFamilyIndices = vk.SharingModeConcurrent, 2, &families[0]
	}
	var vi videoImage
	r := d.cmds.CreateImage(d.device, &ici, nil, &vi.image)
	runtime.KeepAlive(s.profiles)
	runtime.KeepAlive(&families)
	if r != vk.Success {
		return videoImage{}, fmt.Errorf("vkCreateImage %s %dx%d x%d: %w", s.samples, s.extent[0], s.extent[1], s.layers, resultError(r))
	}
	var req vk.MemoryRequirements
	d.cmds.GetImageMemoryRequirements(d.device, vi.image, &req)
	mem, err := d.allocate(req, vk.MemoryPropertyDeviceLocalBit, nil)
	if err != nil {
		vi.close(d)
		return videoImage{}, err
	}
	vi.memory = mem
	if r := d.cmds.BindImageMemory(d.device, vi.image, vi.memory, 0); r != vk.Success {
		vi.close(d)
		return videoImage{}, fmt.Errorf("vkBindImageMemory: %w", resultError(r))
	}
	if coding == 0 {
		return vi, nil
	}
	viewUsage := vk.ImageViewUsageCreateInfo{SType: vk.StructureTypeImageViewUsageCreateInfo, Usage: coding}
	for l := range s.layers {
		vci := vk.ImageViewCreateInfo{
			SType: vk.StructureTypeImageViewCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&viewUsage)), Image: vi.image, ViewType: vk.ImageViewType2d, Format: s.samples.vk(),
			SubresourceRange: vk.ImageSubresourceRange{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LevelCount: 1, BaseArrayLayer: uint32(l), LayerCount: 1},
		}
		var view vk.ImageView
		r := d.cmds.CreateImageView(d.device, &vci, nil, &view)
		runtime.KeepAlive(&viewUsage)
		if r != vk.Success {
			vi.close(d)
			return videoImage{}, fmt.Errorf("vkCreateImageView %s layer %d: %w", s.samples, l, resultError(r))
		}
		vi.views = append(vi.views, view)
	}
	return vi, nil
}
