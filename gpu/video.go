package gpu

import (
	"runtime"
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
// encodes that p has the extension for: nil when there is none, or p
// lacks Vulkan 1.3's synchronization2, which video coding records with.
func (d *Device) probeVideo(p vk.PhysicalDevice, apiVersion uint32, have map[string]bool) *videoQueue {
	if apiVersion < vkVersion(1, 3) {
		return nil
	}
	for _, ext := range videoExtensions {
		if !have[ext] {
			return nil
		}
	}
	f13 := vk.PhysicalDeviceVulkan13Features{SType: vk.StructureTypePhysicalDeviceVulkan13Features}
	f2 := vk.PhysicalDeviceFeatures2{SType: vk.StructureTypePhysicalDeviceFeatures2, PNext: (*uintptr)(unsafe.Pointer(&f13))}
	d.cmds.GetPhysicalDeviceFeatures2(p, &f2)
	runtime.KeepAlive(&f13)
	if f13.Synchronization2 == 0 {
		return nil
	}
	var n uint32
	d.cmds.GetPhysicalDeviceQueueFamilyProperties2(p, &n, nil)
	if n == 0 {
		return nil
	}
	vprops := make([]vk.QueueFamilyVideoPropertiesKHR, n)
	props := make([]vk.QueueFamilyProperties2, n)
	for i := range props {
		vprops[i].SType = vk.StructureTypeQueueFamilyVideoPropertiesKhr
		props[i] = vk.QueueFamilyProperties2{SType: vk.StructureTypeQueueFamilyProperties2, PNext: (*uintptr)(unsafe.Pointer(&vprops[i]))}
	}
	d.cmds.GetPhysicalDeviceQueueFamilyProperties2(p, &n, &props[0])
	runtime.KeepAlive(vprops)
	for i, fp := range props[:n] {
		if fp.QueueFamilyProperties.QueueFlags&vk.QueueFlags(vk.QueueVideoEncodeBitKhr) == 0 {
			continue
		}
		var found []Codec
		for _, c := range codecs {
			if have[c.extension] && vprops[i].VideoCodecOperations&vk.VideoCodecOperationFlagsKHR(c.operation) != 0 {
				found = append(found, c.codec)
			}
		}
		if len(found) > 0 {
			return &videoQueue{family: uint32(i), codecs: found}
		}
	}
	return nil
}

// extensions are the device extensions encoding on v needs.
func (v *videoQueue) extensions() []string {
	exts := append([]string(nil), videoExtensions...)
	for _, c := range v.codecs {
		exts = append(exts, c.extension())
	}
	return exts
}
