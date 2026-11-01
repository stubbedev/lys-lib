package wlr

// DeferredRequest is true of set_source and set_destination: double
// buffered state a wl_surface commit applies.
func (p *WpViewport) DeferredRequest(opcode uint32) bool { return opcode != wpViewportDestroy }

const wpViewportDestroy uint32 = 0
