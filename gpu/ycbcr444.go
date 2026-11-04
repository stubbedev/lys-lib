package gpu

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/stubbedev/lys-lib/third_party/gomath"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// yuv444Constants are ycbcr444.frag's specialization constants: what
// turns a sample in a packed 4:4:4 buffer into RGB.
type yuv444Constants struct {
	kr, kb                  float32
	lumaScale, lumaBase     float32
	chromaScale, chromaBase float32
}

type yuv444Key struct {
	matrix YCbCrMatrix
	rng    YCbCrRange
	bits   int
}

type yuv444Kind struct {
	constants yuv444Constants
	pipes     map[vk.Format]vk.Pipeline
	colored   coloredPipes
}

// constants derives the conversion, as Vulkan's own model conversions
// define it, for samples normalized from bits-bit codes. A format no
// Vulkan YCbCr format has the layout of samples like RGB, which a packed
// 4:4:4 buffer can as every pixel holds all three components.
func (k yuv444Key) constants() (yuv444Constants, error) {
	var c yuv444Constants
	switch k.matrix {
	case BT601:
		c.kr, c.kb = 0.299, 0.114
	case BT709:
		c.kr, c.kb = 0.2126, 0.0722
	case BT2020:
		c.kr, c.kb = 0.2627, 0.0593
	default:
		return c, fmt.Errorf("gpu: YCbCr matrix %d is none of BT.601, BT.709, BT.2020", k.matrix)
	}
	top := gomath.Exp2(float64(k.bits)) - 1
	unit := gomath.Exp2(float64(k.bits - 8))
	switch k.rng {
	case LimitedRange:
		c.lumaScale, c.lumaBase = float32(top/(219*unit)), 16.0/219
		c.chromaScale, c.chromaBase = float32(top/(224*unit)), 128.0/224
	case FullRange:
		c.lumaScale, c.lumaBase = 1, 0
		c.chromaScale, c.chromaBase = 1, float32(128*unit/top)
	default:
		return c, fmt.Errorf("gpu: YCbCr range %d is neither limited nor full", k.rng)
	}
	return c, nil
}

func (d *Device) yuv444KindOf(pf pixelFormat, c YCbCr) (*yuv444Kind, error) {
	if c.Chroma.X > SitedAtHalf || c.Chroma.Y > SitedAtHalf {
		return nil, fmt.Errorf("gpu: chroma siting %+v is not one Vulkan samples", c.Chroma)
	}
	k := yuv444Key{matrix: c.Matrix, rng: c.Range, bits: pf.yuvBits}
	constants, err := k.constants()
	if err != nil {
		return nil, err
	}
	if err := d.compInit(); err != nil {
		return nil, err
	}
	if kind, ok := d.comp.yuv444[k]; ok {
		return kind, nil
	}
	kind := &yuv444Kind{constants: constants, pipes: map[vk.Format]vk.Pipeline{}}
	d.comp.yuv444[k] = kind
	return kind, nil
}

func (kind *yuv444Kind) pipeline(d *Device, target vk.Format) (vk.Pipeline, error) {
	if p, ok := kind.pipes[target]; ok {
		return p, nil
	}
	ct, err := d.compTargetOf(target)
	if err != nil {
		return 0, err
	}
	build := func() (vk.Pipeline, error) {
		p, err := d.constantsPipelineOf(ct, d.comp.frags[fragYCbCr444], d.comp.layout, unsafe.Pointer(&kind.constants), unsafe.Sizeof(kind.constants))
		runtime.KeepAlive(kind)
		return p, err
	}
	return d.lazyPipeline(pipelineKey{owner: kind, target: target}, 0, build, func(p vk.Pipeline) { kind.pipes[target] = p })
}

func (kind *yuv444Kind) destroy(d *Device) {
	kind.colored.destroy(d)
	for _, p := range kind.pipes {
		d.cmds.DestroyPipeline(d.device, p, nil)
	}
}
