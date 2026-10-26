package gpu

import (
	"fmt"
	"image"

	"github.com/stubbedev/lys-lib/internal/intconv"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// count is len(s) as the uint32 element count Vulkan takes.
func count[T any](s []T) uint32 { return intconv.Must[uint32](len(s)) }

// offset3D is p as a Vulkan image offset.
func offset3D(p image.Point) vk.Offset3D {
	return vk.Offset3D{X: intconv.Must[int32](p.X), Y: intconv.Must[int32](p.Y)}
}

// extent2D is a w x h size as a Vulkan extent.
func extent2D(w, h int) vk.Extent2D {
	return vk.Extent2D{Width: intconv.Must[uint32](w), Height: intconv.Must[uint32](h)}
}

// extent3D is a w x h size as a one-deep Vulkan extent.
func extent3D(w, h int) vk.Extent3D {
	return vk.Extent3D{Width: intconv.Must[uint32](w), Height: intconv.Must[uint32](h), Depth: 1}
}

// checkSize reports a w x h image the device cannot make: a side not
// positive or past its largest.
func (d *Device) checkSize(w, h int) error {
	if w <= 0 || h <= 0 || w > d.maxDim || h > d.maxDim {
		return fmt.Errorf("gpu: image size %dx%d is not 1 to %d a side", w, h, d.maxDim)
	}
	return nil
}

// flags is a Vulkan FlagBits enumerant, or several ORed, as the Flags
// word Vulkan takes them in: the same 32 bits.
func flags[F ~uint32, B ~int32](b B) F { return F(intconv.Bits32(int32(b))) }
