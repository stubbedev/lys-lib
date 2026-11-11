package gpu

import (
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/testbuild"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// The lookups that replaced the package's maps answer on the stack: an
// import asks them for every buffer.
func TestFormatLookupsAllocateNothing(t *testing.T) {
	testbuild.SkipAllocBudget(t)
	formats := slices.Concat(textureFormats, floatFormats, yuv444Formats, ycbcrFormats)
	planes := 0
	allocs := testing.AllocsPerRun(100, func() {
		for _, f := range formats {
			pf, _ := lookupPixelFormat(f)
			planes += pf.planes
			if l, ok := yuvImageLayoutOf(f); ok {
				planes += len(l.planes())
			}
		}
	})
	if allocs != 0 {
		t.Errorf("looking %d formats up allocates %v times, want 0", len(formats), allocs)
	}
	if planes == 0 {
		t.Error("no format has a plane")
	}
}

func TestEveryOfferedFormatHasAPixelFormat(t *testing.T) {
	for _, f := range slices.Concat(textureFormats, floatFormats, yuv444Formats, ycbcrFormats) {
		pf, ok := lookupPixelFormat(f)
		if !ok || pf.planes < 1 || pf.vk == vk.FormatUndefined {
			t.Errorf("%s: pixel format %+v, known %v", f, pf, ok)
		}
		if got, err := pixelFormatOf(f); err != nil || got != pf {
			t.Errorf("%s: pixelFormatOf = %+v, %v, want %+v", f, got, err, pf)
		}
	}
	if pf, ok := lookupPixelFormat(fourcc.Format(0)); ok || pf != (pixelFormat{}) {
		t.Errorf("no format at all has pixel format %+v", pf)
	}
	if _, err := pixelFormatOf(fourcc.Format(0)); err == nil {
		t.Error("pixelFormatOf takes no format at all")
	}
}

func TestPlainImageLayoutsReadEverySampleFromAnImageTheyHave(t *testing.T) {
	for _, f := range ycbcrFormats {
		l, ok := yuvImageLayoutOf(f)
		if !ok {
			t.Errorf("%s has no plain image layout", f)
			continue
		}
		images := l.planes()
		if len(images) < 2 || len(images) > len(l.images) {
			t.Errorf("%s reads %d images", f, len(images))
			continue
		}
		for _, s := range []planeSample{l.y, l.cb, l.cr} {
			if s.image < 0 || s.image >= len(images) {
				t.Errorf("%s reads a sample from image %d of %d", f, s.image, len(images))
			}
		}
		for i, img := range images {
			if img.vf == vk.FormatUndefined || img.hsub < 1 || img.vsub < 1 || img.plane < 0 || img.plane >= f.Planes() {
				t.Errorf("%s: image %d is %+v over a buffer of %d planes", f, i, img, f.Planes())
			}
		}
	}
	for _, f := range slices.Concat(textureFormats, floatFormats, yuv444Formats) {
		if l, ok := yuvImageLayoutOf(f); ok {
			t.Errorf("%s, no format of planes or macropixels, reads as %+v", f, l)
		}
	}
}

func TestResultErrorsNameWhatVulkanNames(t *testing.T) {
	for r, want := range map[vk.Result]string{
		vk.ErrorDeviceLost:                           "VK_ERROR_DEVICE_LOST",
		vk.ErrorImageUsageNotSupportedKhr:            "VK_ERROR_IMAGE_USAGE_NOT_SUPPORTED_KHR",
		vk.ErrorVideoPictureLayoutNotSupportedKhr:    "VK_ERROR_VIDEO_PICTURE_LAYOUT_NOT_SUPPORTED_KHR",
		vk.ErrorVideoProfileOperationNotSupportedKhr: "VK_ERROR_VIDEO_PROFILE_OPERATION_NOT_SUPPORTED_KHR",
		vk.ErrorVideoProfileFormatNotSupportedKhr:    "VK_ERROR_VIDEO_PROFILE_FORMAT_NOT_SUPPORTED_KHR",
		vk.ErrorVideoProfileCodecNotSupportedKhr:     "VK_ERROR_VIDEO_PROFILE_CODEC_NOT_SUPPORTED_KHR",
		vk.ErrorVideoStdVersionNotSupportedKhr:       "VK_ERROR_VIDEO_STD_VERSION_NOT_SUPPORTED_KHR",
		vk.ErrorOutOfDeviceMemory:                    "VkResult -2",
	} {
		if got := resultError(r).Error(); got != want {
			t.Errorf("result %d reads %q, want %q", r, got, want)
		}
	}
}

func TestKernelDriversNameTheirManifests(t *testing.T) {
	for driver, want := range map[string]string{
		"i915": "intel_*", "xe": "intel_*", "amdgpu": "radeon_*,amd_*", "radeon": "radeon_*,amd_*",
		"nouveau": "nouveau_*", "nvidia": "nvidia_*", "virtio_gpu": "virtio_*", "msm": "freedreno_*",
		"panfrost": "panfrost_*", "panthor": "panfrost_*", "v3d": "broadcom_*", "asahi": "asahi_*",
		"powervr": "powervr_*", "vkms": "", "": "",
	} {
		if got := driverManifests(driver); got != want {
			t.Errorf("kernel driver %q loads %q, want %q", driver, got, want)
		}
	}
}

func TestPicturesHaveAFormatForEachDepthAndChroma(t *testing.T) {
	seen := map[vk.Format]videoSamples{}
	for _, chroma := range []chromaFormat{chroma420, chroma422, chroma444} {
		for _, bits := range []int{8, 10, 12} {
			s, ok := samplesOf(bits, chroma)
			if !ok || s.vk() == vk.FormatUndefined {
				t.Errorf("%d-bit %s pictures have no format", bits, chroma)
				continue
			}
			if other, dup := seen[s.vk()]; dup {
				t.Errorf("%s and %s pictures share VkFormat %d", s, other, s.vk())
			}
			seen[s.vk()] = s
			if !strings.Contains(s.String(), chroma.String()) {
				t.Errorf("samples read %q, without %s", s, chroma)
			}
		}
		for _, bits := range []int{-1, 0, 9, 14, 16, 256} {
			if s, ok := samplesOf(bits, chroma); ok {
				t.Errorf("%d-bit %s pictures have format %d", bits, chroma, s.vk())
			}
		}
	}
	if nv12Samples.vk() != nv12 {
		t.Errorf("8-bit 4:2:0 pictures are VkFormat %d, want NV12's %d", nv12Samples.vk(), nv12)
	}
}

func TestEncoderTriesEachCodecsProfilesBestFirst(t *testing.T) {
	if got := vaCodecProfiles(H264); len(got) != 2 || got[0].std != vk.StdVideoH264ProfileIdcHigh || got[1].std != vk.StdVideoH264ProfileIdcMain {
		t.Errorf("H.264 tries %+v, want High then Main", got)
	}
	if got := vaCodecProfiles(H265); len(got) != 1 {
		t.Errorf("H.265 tries %+v, want Main alone", got)
	}
	if got := vaCodecProfiles(Codec(255)); got != nil {
		t.Errorf("no codec at all tries %+v", got)
	}
}
