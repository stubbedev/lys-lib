//go:build !(js && wasm)

package vk

import "testing"

func TestVulkanLibraryPathPrefersTheEnvironment(t *testing.T) {
	getenv := func(key string) string {
		if key == LibraryEnv {
			return "/nix/store/loader/lib/libvulkan.so.1"
		}
		return ""
	}
	if got := vulkanLibraryPath(getenv, "linux"); got != "/nix/store/loader/lib/libvulkan.so.1" {
		t.Fatalf("vulkanLibraryPath with %s set = %q", LibraryEnv, got)
	}
}

func TestVulkanLibraryPathFallsBackToTheLinkedDefault(t *testing.T) {
	unset := func(string) string { return "" }
	saved := defaultLibrary
	t.Cleanup(func() { defaultLibrary = saved })
	defaultLibrary = "/nix/store/pinned/lib/libvulkan.so.1"
	if got := vulkanLibraryPath(unset, "linux"); got != defaultLibrary {
		t.Fatalf("vulkanLibraryPath on linux = %q, want the -X pinned %q", got, defaultLibrary)
	}
	if got := vulkanLibraryPath(unset, "windows"); got != "vulkan-1.dll" {
		t.Fatalf("vulkanLibraryPath on windows = %q", got)
	}
}
