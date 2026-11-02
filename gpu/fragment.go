package gpu

import (
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/lys-lib/third_party/vk"
)

// rgbMask writes colour and keeps the target's alpha.
const rgbMask = vk.ColorComponentFlags(vk.ColorComponentRBit | vk.ColorComponentGBit | vk.ColorComponentBBit)

// FragmentShader is a fragment shader the caller built at runtime, to draw
// a whole texture through. Its SPIR-V module takes the compositor pipeline's
// interface: the texture at set 0 binding 0, its coordinate at input location
// 0 (0,0 at the top left of dst), colour at output location 0, and comp.glsl's
// Draw push constants, of which DrawFragmentShader's constants are keep.
type FragmentShader struct {
	d         *Device
	module    vk.ShaderModule
	pipelines map[vk.Format]vk.Pipeline
}

// NewFragmentShader takes spv, whole SPIR-V words in the host's byte order.
// A driver's refusal is an error and leaves the device as it was; the module
// must still be valid SPIR-V, which nothing checks here.
func (d *Device) NewFragmentShader(spv []byte) (*FragmentShader, error) {
	if len(spv) == 0 {
		return nil, errors.New("gpu: an empty SPIR-V module")
	}
	if err := d.compInit(); err != nil {
		return nil, err
	}
	m, err := d.shaderModule(spv)
	if err != nil {
		return nil, err
	}
	return &FragmentShader{d: d, module: m, pipelines: map[vk.Format]vk.Pipeline{}}, nil
}

// Prepare builds what drawing into t takes, the part a driver may yet refuse,
// so that a caller can learn of that before it opens a pass.
func (s *FragmentShader) Prepare(t *Target) error {
	switch {
	case s.module == 0:
		return errors.New("gpu: Prepare of a closed fragment shader")
	case t == nil || t.closed:
		return errors.New("gpu: Prepare for a closed target")
	case t.s.d != s.d:
		return errors.New("gpu: Prepare for another device's target")
	}
	_, err := s.pipeline(t.s.format())
	return err
}

func (s *FragmentShader) pipeline(format vk.Format) (vk.Pipeline, error) {
	if p, ok := s.pipelines[format]; ok {
		return p, nil
	}
	ct, err := s.d.compTargetOf(format)
	if err != nil {
		return 0, err
	}
	p, err := s.d.graphicsPipeline([2]vk.ShaderModule{s.d.comp.vert, s.module}, s.d.comp.layout, ct.pass, vk.PipelineColorBlendAttachmentState{ColorWriteMask: rgbMask})
	if err != nil {
		return 0, fmt.Errorf("gpu: a pipeline of a fragment shader: %w", err)
	}
	s.pipelines[format] = p
	return p, nil
}

// Close waits for the device, as a frame in flight may still draw with it.
func (s *FragmentShader) Close() error {
	if s.module == 0 {
		return nil
	}
	err := s.d.idle()
	for _, p := range s.pipelines {
		s.d.cmds.DestroyPipeline(s.d.device, p, nil)
	}
	s.d.cmds.DestroyShaderModule(s.d.device, s.module, nil)
	s.module, s.pipelines = 0, nil
	return err
}

// DrawFragmentShader writes all of t through s into dst's damaged part,
// sampling t linearly, with no blending and the target's alpha kept.
func (p *Pass) DrawFragmentShader(s *FragmentShader, t *Texture, dst image.Rectangle, constants [4]float32) {
	if !p.open("DrawFragmentShader") || dst.Empty() {
		return
	}
	switch {
	case s == nil || s.module == 0:
		p.fail(errors.New("gpu: DrawFragmentShader of a closed shader"))
		return
	case s.d != p.d:
		p.fail(errors.New("gpu: DrawFragmentShader of another device's shader"))
		return
	case t == nil || t.closed:
		p.fail(errors.New("gpu: DrawFragmentShader of a closed texture"))
		return
	case t.d != p.d:
		p.fail(errors.New("gpu: DrawFragmentShader of another device's texture"))
		return
	case t.surf == &p.t.s:
		p.fail(errors.New("gpu: DrawFragmentShader of the pass's own target"))
		return
	case t.yuv():
		p.fail(errors.New("gpu: DrawFragmentShader of a YCbCr texture"))
		return
	}
	pipe, err := s.pipeline(p.t.s.format())
	if err != nil {
		p.fail(err)
		return
	}
	set, err := t.set(filterLinear)
	if err != nil {
		p.fail(err)
		return
	}
	dc := drawConstants{target: [4]float32{2: 1}, extra: [2][4]float32{{0, 0, 1, 0}, {0, 1, 1, 1}}, keep: constants}
	p.ops = append(p.ops, passOp{kind: opTexture, dst: dst, tex: t, set: set, pipe: pipe, layout: p.d.comp.layout, dc: dc})
}
