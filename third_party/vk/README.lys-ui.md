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
Only this package is carried: it depends on nothing of gogpu but goffi,
which it no longer uses either (below).

## Patches

Each bug fix goes upstream as its own pull request; drop it here once
merged.

### Calls go through purego, not goffi

`internal/ffi` is the slice of goffi's API the bindings use
(`PrepareCallInterface`, `CallFunction`, `LoadLibrary`, `GetSymbol`,
`FreeLibrary` and the type descriptors), carried out by
github.com/ebitengine/purego. Every source imports it in place of
`goffi/ffi` and `goffi/types`. goffi and purego each link a runtime
shim (`internal/fakecgo`), so a cgo-free program holding both fails to
link with a duplicated `_cgo_init`. lys-shell loads its other native
libraries through purego. Vulkan's arguments and results are integers,
handles and pointers; a float argument (vkCmdSetLineWidth and a few
others, which lys-ui does not call) panics, since purego.SyscallN
passes integer registers only. gpu's TestOneFakecgo keeps a second
shim out. This is a lys-ui integration choice rather than an upstream
bug.

### C `int` is `int32`

Upstream: https://github.com/gogpu/wgpu/pull/372

`commands_gen.go`, `types_gen.go`: vk-gen has no mapping for C `int`, so
it passed through as Go's 64-bit `int`. `vkGetMemoryFdKHR`,
`vkGetSemaphoreFdKHR` and `vkGetFenceFdKHR` wrote a 32-bit fd through a
pointer to a 64-bit int, leaving its upper half garbage. The `Fd` fields
of the import-fd structs, and `VkNativeBufferANDROID`'s `int` fields,
also had the wrong size and so a wrong struct layout. All are `int32`.

### The dmabuf export device functions load

Upstream: https://github.com/gogpu/wgpu/pull/373

`commands.go`, `LoadDevice`: `vkGetMemoryFdKHR`,
`vkGetMemoryFdPropertiesKHR` and `vkGetImageDrmFormatModifierPropertiesEXT`
had fields and wrappers but were never loaded, so calling them jumped to
nil.

### vkGetPhysicalDeviceFormatProperties2 loads

Upstream: https://github.com/gogpu/wgpu/pull/373

`commands.go`, `LoadInstance`: it had a field and a wrapper but was
never loaded, so the DRM-modifier query (a format's modifier list
chained into its properties) jumped to nil.

### Surface-less instances load

Upstream: https://github.com/gogpu/wgpu/pull/374

`commands.go`, `LoadInstance`: it failed unless the VK_KHR_surface query
functions loaded, though an instance rendering offscreen or into
exported memory enables no surface extension. Surface users check
`HasWSIQueries`, which already existed.

### Commands vk-gen has no signature for

Upstream: https://github.com/gogpu/wgpu/pull/376 and, for
`vkCmdPushConstants`, https://github.com/gogpu/wgpu/pull/377

`commands_lysui.go`: wrappers for `vkGetImageSubresourceLayout` (a dmabuf
plane's offset and stride), `vkCmdClearColorImage`,
`vkGetMemoryFdPropertiesKHR` (the memory types an imported dmabuf binds
as) and `vkCmdPushConstants`, which vk-gen skips as unsupported
signatures.

### Core 1.1 structure types

Upstream: https://github.com/gogpu/wgpu/pull/375

`const_lysui.go`: vk-gen emits only the extension aliases of the
external-memory, dedicated-allocation and format-properties structure
types, so the core names were missing. Upstream found the cause: vk-gen read
enum values from the extensions section only and ignored `extnumber`,
so about 60 other constants here carry wrong values too (lys-ui uses
none of them).

### 64-bit flag types

`types_gen.go`: vk-gen typed thirteen `VkFlags64` types as 32-bit
`Flags` (among them `PipelineStageFlags2`, `AccessFlags2` and
`FormatFeatureFlags2`, so every synchronization2 barrier was laid out
short) and four 32-bit `VkFlags` types as `Flags64` (the H.264 encode
capability, std-syntax and rate-control flags, and the H.264 decode
picture layout). Each now has its header's width;
`TestVideoStructSizes` pins the sizes of the structures the GPU
encoder passes against a C compiler's.

### Video coding and synchronization2 functions load

`commands.go`: `LoadInstance` loads
`vkGetPhysicalDeviceQueueFamilyProperties2` and the video capability,
format and quality-level queries; `LoadDevice` loads
`vkCmdPipelineBarrier2`, `vkQueueSubmit2` and the
VK_KHR_video_queue and VK_KHR_video_encode_queue commands. All had
fields and wrappers and were never loaded.

### vkGetQueryPoolResults

`commands_lysui.go`: a wrapper vk-gen skipped (its eight arguments
had no signature template), for the encoder's feedback queries.

### H.264 and H.265 StdVideo structures

`stdvideo_lysui.go`, `stdvideo_h265_lysui.go`: vk-gen carries the codec
headers' structures as opaque byte arrays of guessed sizes. The H.264
parameter sets and encode structures, and every structure of
`vulkan_video_codec_h265std.h` and `vulkan_video_codec_h265std_encode.h`,
are laid out as the headers lay them out, their arrays dropped from
`types_gen.go`; `TestStdVideoH264Layouts` and `TestStdVideoH265Layouts`
pin every size and field offset against a C compiler's.

### Core 1.1 and 1.3 values the encoder names

`const_lysui.go`: the queue family properties and 1.3 feature
structure types, synchronization2's structure types, the two-plane
4:2:0 format and its plane aspects, all left out of `const_gen.go`.

### vkGetPhysicalDeviceImageFormatProperties2 loads

`commands.go`, `LoadInstance`: it had a field and a wrapper but was
never loaded; the dmabuf export checks ask it whether a modifier
exports. Its structure types, and the external image format ones,
join the core values in `const_lysui.go`.
