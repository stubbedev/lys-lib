package capture

import (
	"fmt"

	"github.com/stubbedev/gelm/wlr"
)

// hlEvents adapts copyState to the hyprland_toplevel_export_frame_v1
// handlers.
type hlEvents struct{ *copyState }

func (e hlEvents) HandleToplevelExportFrameV1Buffer(ev wlr.ToplevelExportFrameV1BufferEvent) {
	e.buffer(ev.Format, ev.Width, ev.Height, ev.Stride)
}

func (e hlEvents) HandleToplevelExportFrameV1Damage(ev wlr.ToplevelExportFrameV1DamageEvent) {
	e.addDamage(ev.X, ev.Y, ev.Width, ev.Height)
}

func (e hlEvents) HandleToplevelExportFrameV1Flags(ev wlr.ToplevelExportFrameV1FlagsEvent) {
	e.flags = ev.Flags
}

func (e hlEvents) HandleToplevelExportFrameV1Ready(wlr.ToplevelExportFrameV1ReadyEvent) {
	e.ready = true
}

func (e hlEvents) HandleToplevelExportFrameV1Failed(wlr.ToplevelExportFrameV1FailedEvent) {
	e.failed = true
}

func (e hlEvents) HandleToplevelExportFrameV1LinuxDmabuf(ev wlr.ToplevelExportFrameV1LinuxDmabufEvent) {
	e.linuxDmabuf(ev.Format, ev.Width, ev.Height)
}

func (e hlEvents) HandleToplevelExportFrameV1BufferDone(wlr.ToplevelExportFrameV1BufferDoneEvent) {
	e.bufferDone = true
}

func (e hlEvents) listen(f *wlr.ToplevelExportFrameV1) {
	f.AddBufferHandler(e)
	f.AddDamageHandler(e)
	f.AddFlagsHandler(e)
	f.AddReadyHandler(e)
	f.AddFailedHandler(e)
	f.AddLinuxDmabufHandler(e)
	f.AddBufferDoneHandler(e)
}

// CaptureHyprlandWindow copies one frame of a Hyprland window through
// hyprland-toplevel-export, addressed by the window's address (the
// hex handle `hyprctl clients` prints, as a number). The copy starts as
// soon as the shm layout arrives and ignores damage, so the first
// frame is always complete.
func (c *Client) CaptureHyprlandWindow(address uint64, opts Options) (*Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.usable(); err != nil {
		return nil, err
	}
	if !c.HasHyprlandExport() {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, ifaceHyprlandShare)
	}
	overlay := int32(0)
	if opts.Cursor {
		overlay = 1
	}
	// The protocol carries the address's low 32 bits, which is what
	// Hyprland matches windows by.
	frame, err := c.hyprland.CaptureToplevel(overlay, uint32(address))
	if err != nil {
		return nil, fmt.Errorf("capture: toplevel export request: %w", err)
	}
	defer func() { _ = frame.Destroy() }()
	st := &copyState{}
	hlEvents{st}.listen(frame)
	if err := c.dispatchUntil(func() bool { return st.failed || st.shm != nil }); err != nil {
		return nil, err
	}
	if st.failed {
		return nil, ErrFailed
	}
	buf, err := newShmBuffer(c.shm, st.shm.width, st.shm.height, st.shm.stride, st.shm.format)
	if err != nil {
		return nil, err
	}
	defer buf.release()
	if err := frame.Copy(buf.wl, 1); err != nil {
		return nil, fmt.Errorf("capture: toplevel export copy: %w", err)
	}
	if err := c.dispatchUntil(st.finished); err != nil {
		return nil, err
	}
	if st.failed {
		return nil, ErrFailed
	}
	out := buf.frame(opts.Dst)
	out.YInvert = st.flags&screencopyYInvert != 0
	out.Damage = st.damage
	return out, nil
}
