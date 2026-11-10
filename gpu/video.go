package gpu

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"unsafe"

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
	// transfer is whether the family copies images, which a device
	// that decodes only into its reference pictures needs.
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
// with, a missing extension, no queue family that encodes video, or
// none that codes the codec.
func (d *Device) probeVideo(p vk.PhysicalDevice, sync2 bool, have map[string]bool) (*videoQueue, map[Codec]error) {
	ops := make([]videoOp, len(codecs))
	for i, c := range codecs {
		ops[i] = c.op
	}
	var missing error
	if !sync2 {
		missing = errors.New("no Vulkan 1.3 synchronization2")
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
// and VP9 the videoDecodeVP9 feature.
func (d *Device) probeDecode(p vk.PhysicalDevice, sync2, ycbcr bool, have map[string]bool) (*decodeQueue, map[demux.Codec]error) {
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

// probeVP9 reports whether p has the videoDecodeVP9 feature, which
// VK_KHR_video_decode_vp9 decodes with once enabled.
func (d *Device) probeVP9(p vk.PhysicalDevice) bool {
	vp9 := vk.PhysicalDeviceVideoDecodeVP9FeaturesKHR{SType: vk.StructureTypePhysicalDeviceVideoDecodeVp9FeaturesKhr}
	f2 := vk.PhysicalDeviceFeatures2{SType: vk.StructureTypePhysicalDeviceFeatures2, PNext: (*uintptr)(unsafe.Pointer(&vp9))}
	d.cmds.GetPhysicalDeviceFeatures2(p, &f2)
	runtime.KeepAlive(&vp9)
	return vp9.VideoDecodeVP9 != 0
}
