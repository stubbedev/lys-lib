package capture

import (
	"github.com/stubbedev/gelm/third_party/neurlang-wayland/wl"
)

// Output is one wl_output as the capture connection sees it: the
// connector identity (wl_output v4 name and description), the current
// mode in hardware pixels, the integer scale, and the geometry event's
// position and transform. The position is what wl_output.geometry
// reports - logical coordinates on wlroots compositors, which is what
// layouts of several outputs are built from.
type Output struct {
	// Name is the connector name (DP-1, HDMI-A-1); empty when the
	// compositor offers wl_output below version 4.
	Name string
	// Description is the human-readable output description.
	Description string
	// X and Y are the output's position in the global compositor space.
	X, Y int32
	// Width and Height are the current mode in hardware pixels, before
	// the transform.
	Width, Height int32
	// RefreshMHz is the current mode's refresh rate in mHz; 0 when no
	// mode carried the current flag.
	RefreshMHz int32
	// Scale is the integer buffer scale (1 before any scale event).
	Scale int32
	// Transform is how the compositor rotates/flips buffer contents on
	// this output.
	Transform Transform

	wl *wl.Output
}

// outputState tracks one bound wl_output as its events arrive.
type outputState struct {
	out    Output
	global uint32
}

func (c *Client) addOutput(o *wl.Output, global uint32) {
	st := &outputState{out: Output{Scale: 1, wl: o}, global: global}
	c.outputs = append(c.outputs, st)
	o.AddGeometryHandler(st)
	o.AddModeHandler(st)
	o.AddScaleHandler(st)
	o.AddNameHandler(st)
	o.AddDescriptionHandler(st)
}

// HandleOutputGeometry implements wl.OutputGeometryHandler.
func (s *outputState) HandleOutputGeometry(ev wl.OutputGeometryEvent) {
	s.out.X, s.out.Y = ev.X, ev.Y
	s.out.Transform = Transform(ev.Transform)
}

// HandleOutputMode implements wl.OutputModeHandler: only the current
// mode counts.
func (s *outputState) HandleOutputMode(ev wl.OutputModeEvent) {
	if ev.Flags&wl.OutputModeCurrent == 0 {
		return
	}
	s.out.Width, s.out.Height = ev.Width, ev.Height
	s.out.RefreshMHz = ev.Refresh
}

// HandleOutputScale implements wl.OutputScaleHandler.
func (s *outputState) HandleOutputScale(ev wl.OutputScaleEvent) { s.out.Scale = ev.Factor }

// HandleOutputName implements wl.OutputNameHandler.
func (s *outputState) HandleOutputName(ev wl.OutputNameEvent) { s.out.Name = ev.Name }

// HandleOutputDescription implements wl.OutputDescriptionHandler.
func (s *outputState) HandleOutputDescription(ev wl.OutputDescriptionEvent) {
	s.out.Description = ev.Description
}

// Outputs lists the outputs in registry order, as of the last
// dispatch. Hotplug and mode changes are picked up by any later
// capture call (or Refresh).
func (c *Client) Outputs() []Output {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Output, len(c.outputs))
	for i, o := range c.outputs {
		out[i] = o.out
	}
	return out
}

// OutputByName returns the output with the given connector name.
func (c *Client) OutputByName(name string) (Output, bool) {
	for _, o := range c.Outputs() {
		if o.Name == name {
			return o, true
		}
	}
	return Output{}, false
}

// Refresh roundtrips the connection so output hotplug, mode changes,
// and toplevel list updates are current.
func (c *Client) Refresh() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.usable(); err != nil {
		return err
	}
	return c.roundtrip()
}
