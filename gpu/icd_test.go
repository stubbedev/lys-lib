package gpu

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeSysfs links node 226:128 to a device driven by driver under a
// temporary sysfs root.
func fakeSysfs(t *testing.T, driver string) string {
	t.Helper()
	root := t.TempDir()
	dev := filepath.Join(root, "226:128", "device")
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "bus", "pci", "drivers", driver), filepath.Join(dev, "driver")); err != nil {
		t.Fatal(err)
	}
	return root
}

// The process instance loads only the Vulkan drivers for the named
// node's kernel driver, unless the environment already chooses them.
func TestDriverGlob(t *testing.T) {
	node := &drmNode{major: 226, minor: 128}
	unset := func(string) string { return "" }
	tests := []struct {
		name   string
		driver string
		node   *drmNode
		getenv func(string) string
		want   string
	}{
		{"nvidia", "nvidia", node, unset, "nvidia_*"},
		{"intel xe", "xe", node, unset, "intel_*"},
		{"amdgpu", "amdgpu", node, unset, "radeon_*,amd_*"},
		{"an unmapped driver loads them all", "simpledrm", node, unset, ""},
		{"no node named loads them all", "nvidia", nil, unset, ""},
		{"a chosen driver file wins", "nvidia", node, func(k string) string {
			if k == "VK_DRIVER_FILES" {
				return "/lvp_icd.json"
			}
			return ""
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := driverGlob(fakeSysfs(t, tt.driver), tt.node, tt.getenv); got != tt.want {
				t.Errorf("driverGlob = %q, want %q", got, tt.want)
			}
		})
	}
	if got := driverGlob(t.TempDir(), node, unset); got != "" {
		t.Errorf("a node sysfs does not know loads %q, want every driver", got)
	}
}

// The selection holds while the instance is made and is gone after, so
// processes started later choose their own drivers.
func TestSelectDriversRestoresTheEnvironment(t *testing.T) {
	t.Setenv(driversSelect, "")
	os.Unsetenv(driversSelect)
	var during string
	if err := selectDrivers("nvidia_*", func() error {
		during = os.Getenv(driversSelect)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if during != "nvidia_*" {
		t.Errorf("the loader saw %s=%q, want nvidia_*", driversSelect, during)
	}
	if v, ok := os.LookupEnv(driversSelect); ok {
		t.Errorf("%s=%q outlived the instance creation", driversSelect, v)
	}
}
