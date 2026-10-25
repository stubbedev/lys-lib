# github.com/gogpu/wgpu hal/vulkan/vk at 3e9824c, patched for lys-ui

A copy of the `hal/vulkan/vk` package of github.com/gogpu/wgpu at
3e9824c (MIT, LICENSE), its non-test sources only, carried as a package
of the lys-ui module and imported as
`github.com/stubbedev/lys-ui/third_party/vk`. These are pure-Go Vulkan
bindings generated from vk.xml, loading libvulkan at runtime through
goffi, so lys-ui stays `CGO_ENABLED=0`. lys-ui's GPU renderer (#298)
draws through it.

## Why it lives inside the module

As with `third_party/oksvg`: Go ignores a dependency's `replace`
directives, so a patched copy wired in through go.mod would reach lys-ui's
own builds and none of its consumers' (lys-shell, lys-wm). As a package
of the lys-ui module, the patches reach every application importing it.
Only this package is carried: it depends on nothing of gogpu but goffi.

## Patches

Each goes upstream as its own pull request; drop it here once merged.

### C `int` is `int32`

`commands_gen.go`, `types_gen.go`: vk-gen has no mapping for C `int`, so
it passed through as Go's 64-bit `int`. `vkGetMemoryFdKHR`,
`vkGetSemaphoreFdKHR` and `vkGetFenceFdKHR` wrote a 32-bit fd through a
pointer to a 64-bit int, leaving its upper half garbage. The `Fd` fields
of the import-fd structs, and `VkNativeBufferANDROID`'s `int` fields,
also had the wrong size and so a wrong struct layout. All are `int32`.

### The dmabuf export device functions load

`commands.go`, `LoadDevice`: `vkGetMemoryFdKHR`,
`vkGetMemoryFdPropertiesKHR` and `vkGetImageDrmFormatModifierPropertiesEXT`
had fields and wrappers but were never loaded, so calling them jumped to
nil.

### vkGetPhysicalDeviceFormatProperties2 loads

`commands.go`, `LoadInstance`: it had a field and a wrapper but was
never loaded, so the DRM-modifier query (a format's modifier list
chained into its properties) jumped to nil.

### Surface-less instances load

`commands.go`, `LoadInstance`: it failed unless the VK_KHR_surface query
functions loaded, though an instance rendering offscreen or into
exported memory enables no surface extension. Surface users check
`HasWSIQueries`, which already existed.

### Commands vk-gen has no signature for

`commands_lysui.go`: wrappers for `vkGetImageSubresourceLayout` (a dmabuf
plane's offset and stride), `vkCmdClearColorImage`, and
`vkGetMemoryFdPropertiesKHR` (the memory types an imported dmabuf binds
as), which vk-gen skips as unsupported signatures.

### Core 1.1 structure types

`const_lysui.go`: vk-gen emits only the extension aliases of the
external-memory, dedicated-allocation and format-properties structure
types, so the core names were missing.
