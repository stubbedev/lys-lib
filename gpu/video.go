package gpu

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"unsafe"

	"github.com/stubbedev/lys-ui/third_party/vk"
)

// Codec is a video coding format the encoder writes.
type Codec int

// The codecs.
const (
	H264 Codec = iota
	H265
)

// codecs are every codec, each with its name, the device extension
// encoding it needs and its encode operation bit.
var codecs = []struct {
	codec     Codec
	name      string
	extension string
	operation vk.VideoCodecOperationFlagBitsKHR
}{
	{H264, "H.264", "VK_KHR_video_encode_h264", vk.VideoCodecOperationEncodeH264BitKhr},
	{H265, "H.265", "VK_KHR_video_encode_h265", vk.VideoCodecOperationEncodeH265BitKhr},
}

func (c Codec) String() string { return codecs[c].name }

func (c Codec) extension() string { return codecs[c].extension }

func (c Codec) operation() vk.VideoCodecOperationFlagBitsKHR { return codecs[c].operation }

// videoExtensions are what any video encoding needs, beside a codec's
// own extension.
var videoExtensions = []string{"VK_KHR_video_queue", "VK_KHR_video_encode_queue"}

// videoQueue is a device's video encode queue family and the codecs
// it encodes.
type videoQueue struct {
	family uint32
	queue  vk.Queue
	codecs []Codec
}

// probeVideo finds p's video encode queue family and the codecs it
// encodes: nil when there is none. why says, for each codec p does not
// encode, why not: below Vulkan 1.3 or without its synchronization2,
// which video coding records with, a missing extension, no queue
// family that encodes video, or none that codes the codec.
func (d *Device) probeVideo(p vk.PhysicalDevice, apiVersion uint32, have map[string]bool) (*videoQueue, map[Codec]error) {
	why := map[Codec]error{}
	none := func(err error) (*videoQueue, map[Codec]error) {
		for _, c := range codecs {
			why[c.codec] = err
		}
		return nil, why
	}
	if apiVersion < vkVersion(1, 3) {
		return none(fmt.Errorf("the device's Vulkan %d.%d is below the 1.3 video coding needs", apiVersion>>22, apiVersion>>12&0x3ff))
	}
	var missing []string
	for _, ext := range videoExtensions {
		if !have[ext] {
			missing = append(missing, ext)
		}
	}
	if len(missing) > 0 {
		return none(fmt.Errorf("no %s", strings.Join(missing, ", ")))
	}
	f13 := vk.PhysicalDeviceVulkan13Features{SType: vk.StructureTypePhysicalDeviceVulkan13Features}
	f2 := vk.PhysicalDeviceFeatures2{SType: vk.StructureTypePhysicalDeviceFeatures2, PNext: (*uintptr)(unsafe.Pointer(&f13))}
	d.cmds.GetPhysicalDeviceFeatures2(p, &f2)
	runtime.KeepAlive(&f13)
	if f13.Synchronization2 == 0 {
		return none(errors.New("no synchronization2 feature"))
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
	var found *videoQueue
	for i, fp := range props[:n] {
		if fp.QueueFamilyProperties.QueueFlags&vk.QueueFlags(vk.QueueVideoEncodeBitKhr) == 0 {
			continue
		}
		ops := vprops[i].VideoCodecOperations
		families = append(families, fmt.Sprintf("%d (operations %#x)", i, ops))
		var cs []Codec
		for _, c := range codecs {
			if have[c.extension] && ops&vk.VideoCodecOperationFlagsKHR(c.operation) != 0 {
				cs = append(cs, c.codec)
			}
		}
		if found == nil && len(cs) > 0 {
			found = &videoQueue{family: uint32(i), codecs: cs}
		}
	}
	if len(families) == 0 {
		return none(errors.New("no queue family encodes video"))
	}
	for _, c := range codecs {
		switch {
		case found != nil && slices.Contains(found.codecs, c.codec):
		case !have[c.extension]:
			why[c.codec] = fmt.Errorf("no %s", c.extension)
		default:
			why[c.codec] = fmt.Errorf("no encode queue family has operation %#x; families %s", c.operation, strings.Join(families, ", "))
		}
	}
	return found, why
}

// extensions are the device extensions encoding on v needs.
func (v *videoQueue) extensions() []string {
	exts := append([]string(nil), videoExtensions...)
	for _, c := range v.codecs {
		exts = append(exts, c.extension())
	}
	return exts
}
