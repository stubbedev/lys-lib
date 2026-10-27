package gpu

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stubbedev/lys-lib/debug"
)

// driversSelect is the loader variable that limits which Vulkan
// drivers (ICD manifests, by file name glob) an instance loads.
const driversSelect = "VK_LOADER_DRIVERS_SELECT"

// driverChoices are the loader variables through which a user or a
// test already picks the drivers; any of them set, the process
// instance loads what they say.
var driverChoices = []string{
	driversSelect,
	"VK_LOADER_DRIVERS_DISABLE",
	"VK_DRIVER_FILES",
	"VK_ICD_FILENAMES",
	"VK_ADD_DRIVER_FILES",
}

// driverManifests maps a DRM node's kernel driver to the manifests of
// the Vulkan drivers that drive it, as driversSelect globs.
var driverManifests = map[string]string{
	"i915":       "intel_*",
	"xe":         "intel_*",
	"amdgpu":     "radeon_*,amd_*",
	"radeon":     "radeon_*,amd_*",
	"nouveau":    "nouveau_*",
	"nvidia":     "nvidia_*",
	"virtio_gpu": "virtio_*",
	"msm":        "freedreno_*",
	"panfrost":   "panfrost_*",
	"panthor":    "panfrost_*",
	"v3d":        "broadcom_*",
	"asahi":      "asahi_*",
	"powervr":    "powervr_*",
}

// sysDevChar is where sysfs links a character device number to its
// device.
const sysDevChar = "/sys/dev/char"

// kernelDriver names the kernel driver behind node, read from sysfs
// under root.
func kernelDriver(root string, node drmNode) (string, error) {
	link, err := os.Readlink(filepath.Join(root, fmt.Sprintf("%d:%d", node.major, node.minor), "device", "driver"))
	if err != nil {
		return "", fmt.Errorf("gpu: the kernel driver of DRM node %d:%d: %w", node.major, node.minor, err)
	}
	return filepath.Base(link), nil
}

// driverGlob is the driversSelect value that loads only the Vulkan
// drivers for node, "" to load them all: no node named, a choice
// already in the environment, or a kernel driver not mapped.
func driverGlob(root string, node *drmNode, getenv func(string) string) string {
	if node == nil {
		return ""
	}
	for _, v := range driverChoices {
		if getenv(v) != "" {
			return ""
		}
	}
	driver, err := kernelDriver(root, *node)
	if err != nil {
		debug.Log("gpu", "loading every Vulkan driver: %v", err)
		return ""
	}
	return driverManifests[driver]
}

// selectDrivers limits the loader to glob's drivers while create
// runs, the instance creation: the loader reads the variable then, and
// children the process starts later must not inherit it.
func selectDrivers(glob string, create func() error) error {
	if glob == "" {
		return create()
	}
	if err := os.Setenv(driversSelect, glob); err != nil {
		return fmt.Errorf("gpu: set %s=%s: %w", driversSelect, glob, err)
	}
	defer os.Unsetenv(driversSelect) //nolint:errcheck // Unsetenv fails only on a key with '=' or NUL
	return create()
}
