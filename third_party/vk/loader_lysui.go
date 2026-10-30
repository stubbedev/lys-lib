//go:build !(js && wasm)

package vk

const LibraryEnv = "LYS_LIBVULKAN"

var defaultLibrary = "libvulkan.so.1"

func vulkanLibraryPath(getenv func(string) string, goos string) string {
	if path := getenv(LibraryEnv); path != "" {
		return path
	}
	return vulkanLibraryNameFor(goos)
}
