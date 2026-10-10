// Package dmabufwl builds zwp_linux_buffer_params_v1 objects from
// dmabuf descriptions, the step every linux-dmabuf import shares.
package dmabufwl

import (
	"fmt"

	"github.com/stubbedev/gelm/dmabuf"
	"github.com/stubbedev/gelm/wlr"
)

// Params validates b and returns params carrying its planes, ready for
// create or create_immed.
func Params(mgr *wlr.ZwpDmabufV1, b dmabuf.Buffer) (*wlr.ZwpBufferParamsV1, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	params, err := mgr.CreateParams()
	if err != nil {
		return nil, fmt.Errorf("dmabuf: create params: %w", err)
	}
	hi, lo := uint32(b.Modifier>>32), uint32(b.Modifier)
	for i, p := range b.Planes {
		if err := params.Add(uintptr(p.FD), uint32(i), p.Offset, p.Stride, hi, lo); err != nil {
			_ = params.Destroy()
			return nil, fmt.Errorf("dmabuf: plane %d: %w", i, err)
		}
	}
	return params, nil
}
