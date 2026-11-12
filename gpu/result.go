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

// ErrTimeout reports work the device had not finished within its wait
// limit. An Encoder keeps the frame it waited for in flight: a later
// Encode or Flush waits for it again.
var ErrTimeout = errors.New("gpu: the device did not finish within the wait limit")

// resultError is a Vulkan result other than VK_SUCCESS as an error.
type resultError vk.Result

// Error names the results Vulkan names an error by: the timeout, the
// lost device, and what a video capability query refuses a profile with.
func (r resultError) Error() string {
	switch vk.Result(r) {
	case vk.Timeout:
		return "VK_TIMEOUT"
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

// Is makes VK_ERROR_DEVICE_LOST and VK_TIMEOUT, wrapped wherever a
// result became an error, match ErrDeviceLost and ErrTimeout.
func (r resultError) Is(target error) bool {
	switch target {
	case ErrDeviceLost:
		return vk.Result(r) == vk.ErrorDeviceLost
	case ErrTimeout:
		return vk.Result(r) == vk.Timeout
	}
	return false
}
