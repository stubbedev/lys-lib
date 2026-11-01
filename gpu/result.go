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

func (r resultError) Error() string {
	if vk.Result(r) == vk.ErrorDeviceLost {
		return "VK_ERROR_DEVICE_LOST"
	}
	return fmt.Sprintf("VkResult %d", vk.Result(r))
}

// Is makes VK_ERROR_DEVICE_LOST, wrapped wherever a result became an
// error, match ErrDeviceLost.
func (r resultError) Is(target error) bool {
	return target == ErrDeviceLost && vk.Result(r) == vk.ErrorDeviceLost
}
