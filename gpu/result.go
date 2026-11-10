package gpu

import (
	"errors"
	"fmt"

	"github.com/stubbedev/lys-lib/third_party/vk"
)

// ErrDeviceLost reports a Vulkan device that was lost (VK_ERROR_DEVICE_LOST):
// a GPU reset or a driver fault. Nothing further submitted to it runs: a
// consumer closes it and opens a new one, or ends so that a supervisor can.
var ErrDeviceLost = errors.New("gpu: the Vulkan device is lost")

// resultError is a Vulkan result other than VK_SUCCESS as an error.
type resultError vk.Result

// resultNames are the results an error names as Vulkan does: the lost
// device, and what a video capability query refuses a profile with.
var resultNames = map[vk.Result]string{
	vk.ErrorDeviceLost:                           "VK_ERROR_DEVICE_LOST",
	vk.ErrorImageUsageNotSupportedKhr:            "VK_ERROR_IMAGE_USAGE_NOT_SUPPORTED_KHR",
	vk.ErrorVideoPictureLayoutNotSupportedKhr:    "VK_ERROR_VIDEO_PICTURE_LAYOUT_NOT_SUPPORTED_KHR",
	vk.ErrorVideoProfileOperationNotSupportedKhr: "VK_ERROR_VIDEO_PROFILE_OPERATION_NOT_SUPPORTED_KHR",
	vk.ErrorVideoProfileFormatNotSupportedKhr:    "VK_ERROR_VIDEO_PROFILE_FORMAT_NOT_SUPPORTED_KHR",
	vk.ErrorVideoProfileCodecNotSupportedKhr:     "VK_ERROR_VIDEO_PROFILE_CODEC_NOT_SUPPORTED_KHR",
	vk.ErrorVideoStdVersionNotSupportedKhr:       "VK_ERROR_VIDEO_STD_VERSION_NOT_SUPPORTED_KHR",
}

func (r resultError) Error() string {
	if name, ok := resultNames[vk.Result(r)]; ok {
		return name
	}
	return fmt.Sprintf("VkResult %d", vk.Result(r))
}

// Is makes VK_ERROR_DEVICE_LOST, wrapped wherever a result became an
// error, match ErrDeviceLost.
func (r resultError) Is(target error) bool {
	return target == ErrDeviceLost && vk.Result(r) == vk.ErrorDeviceLost
}
