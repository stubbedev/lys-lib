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

// Error names the results Vulkan names an error by: the lost device,
// and what a video capability query refuses a profile with.
func (r resultError) Error() string {
	switch vk.Result(r) {
	case vk.ErrorDeviceLost:
		return "VK_ERROR_DEVICE_LOST"
	case vk.ErrorImageUsageNotSupportedKhr:
		return "VK_ERROR_IMAGE_USAGE_NOT_SUPPORTED_KHR"
	case vk.ErrorVideoPictureLayoutNotSupportedKhr:
		return "VK_ERROR_VIDEO_PICTURE_LAYOUT_NOT_SUPPORTED_KHR"
	case vk.ErrorVideoProfileOperationNotSupportedKhr:
		return "VK_ERROR_VIDEO_PROFILE_OPERATION_NOT_SUPPORTED_KHR"
	case vk.ErrorVideoProfileFormatNotSupportedKhr:
		return "VK_ERROR_VIDEO_PROFILE_FORMAT_NOT_SUPPORTED_KHR"
	case vk.ErrorVideoProfileCodecNotSupportedKhr:
		return "VK_ERROR_VIDEO_PROFILE_CODEC_NOT_SUPPORTED_KHR"
	case vk.ErrorVideoStdVersionNotSupportedKhr:
		return "VK_ERROR_VIDEO_STD_VERSION_NOT_SUPPORTED_KHR"
	}
	return fmt.Sprintf("VkResult %d", vk.Result(r))
}

// Is makes VK_ERROR_DEVICE_LOST, wrapped wherever a result became an
// error, match ErrDeviceLost.
func (r resultError) Is(target error) bool {
	return target == ErrDeviceLost && vk.Result(r) == vk.ErrorDeviceLost
}
