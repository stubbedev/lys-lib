package capture

import (
	"errors"
	"fmt"
	"sync"

	"github.com/neurlang/wayland/wl"
	"github.com/neurlang/wayland/wlclient"

	"github.com/stubbedev/gelm/wlr"
)

// Global names the client binds. They double as the protocol names in
// ErrUnsupported messages.
const (
	ifaceShm           = "wl_shm"
	ifaceOutput        = "wl_output"
	ifaceScreencopy    = "zwlr_screencopy_manager_v1"
	ifaceDmabuf        = "zwp_linux_dmabuf_v1"
	ifaceCopyCapture   = "ext_image_copy_capture_manager_v1"
	ifaceOutputSource  = "ext_output_image_capture_source_manager_v1"
	ifaceToplevelSrc   = "ext_foreign_toplevel_image_capture_source_manager_v1"
	ifaceToplevelList  = "ext_foreign_toplevel_list_v1"
	ifaceHyprlandShare = "hyprland_toplevel_export_manager_v1"
)

// Bind ceilings: the highest version of each global the client speaks.
// wl_output v4 carries the connector name; screencopy v3 adds the
// linux_dmabuf and buffer_done events; linux-dmabuf v4 is the newest
// the binding knows (only create_immed, v2, is used).
const (
	maxOutputVersion     = 4
	maxScreencopyVersion = 3
	maxDmabufVersion     = 4
	maxHyprlandVersion   = 2
)

// ErrUnsupported reports that the compositor does not offer a protocol
// a capture path needs. The wrapped message names the missing global.
var ErrUnsupported = errors.New("capture: protocol not offered by the compositor")

// ErrFailed reports that the compositor refused or aborted a copy: the
// frame's failed event, or a capture session that stopped.
var ErrFailed = errors.New("capture: the compositor failed the copy")

// ErrClosed reports use of a Client after Close.
var ErrClosed = errors.New("capture: client closed")

// Client is a private Wayland connection dedicated to screen capture.
// It binds whichever capture protocols the compositor offers and
// tracks the outputs; every capture call blocks on the calling
// goroutine until the compositor finished the copy (or refused it).
// A Client is safe for use from several goroutines, but its calls
// serialize: run captures that must overlap on separate Clients.
type Client struct {
	mu sync.Mutex

	display *wl.Display
	ctx     *wl.Context
	reg     *wl.Registry

	shm          *wl.Shm
	screencopy   *wlr.ZwlrScreencopyManagerV1
	scVersion    uint32
	dmabuf       *wlr.ZwpDmabufV1
	copyCapture  *wlr.ImageCopyCaptureManagerV1
	outputSource *wlr.OutputImageCaptureSourceManagerV1
	toplevelSrc  *wlr.ForeignToplevelImageCaptureSourceManagerV1
	toplevelList *wlr.ForeignToplevelListV1
	hyprland     *wlr.ToplevelExportManagerV1

	outputs   []*outputState
	toplevels []*toplevelState

	// slot is the reusable screencopy target (see shm.go); dmabufProbe
	// caches each output's advertised dmabuf format.
	slot        *shmBuffer
	dmabufProbe map[*wl.Output]dmabufProbe

	// protoErr is the compositor's fatal wl_display.error, once one
	// arrived; every later call fails with it.
	protoErr error
	closed   bool
}

// Connect opens a capture connection to the compositor named by
// WAYLAND_DISPLAY (inside XDG_RUNTIME_DIR) and binds the capture
// globals. Missing protocols are not an error here: each capture call
// reports ErrUnsupported for the path it needs, and the Has* methods
// probe ahead of time.
func Connect() (*Client, error) {
	return ConnectTo("")
}

// ConnectTo is Connect against an explicit socket name inside
// XDG_RUNTIME_DIR; "" means WAYLAND_DISPLAY.
func ConnectTo(socket string) (*Client, error) {
	display, err := wl.Connect(socket)
	if err != nil {
		return nil, fmt.Errorf("capture: connect: %w", err)
	}
	c := &Client{
		display:     display,
		ctx:         display.Context(),
		dmabufProbe: make(map[*wl.Output]dmabufProbe),
	}
	display.AddErrorHandler(c)
	display.AddDeleteIdHandler(c)
	reg, err := display.GetRegistry()
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("capture: registry: %w", err)
	}
	c.reg = reg
	wlclient.RegistryAddListener(reg, c)
	// The first roundtrip delivers the globals; the second the bound
	// objects' initial bursts (output geometry/mode/name, the
	// toplevel list's handles and their metadata).
	for range 2 {
		if err := c.roundtrip(); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("capture: initial roundtrip: %w", err)
		}
	}
	return c, nil
}

// Close drops the connection; every compositor-side capture object
// dies with it.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.slot != nil {
		c.slot.release()
		c.slot = nil
	}
	return c.ctx.Close()
}

// HasScreencopy reports whether wlr-screencopy (whole-output and
// region capture) is available.
func (c *Client) HasScreencopy() bool { return c.screencopy != nil && c.shm != nil }

// HasDmabuf reports whether linux-dmabuf is bound, the precondition of
// the zero-copy screencopy target.
func (c *Client) HasDmabuf() bool { return c.dmabuf != nil }

// HasToplevelCapture reports whether single windows can be captured
// through ext-image-copy-capture: the copy manager, the
// foreign-toplevel source manager, and the toplevel list are all
// offered.
func (c *Client) HasToplevelCapture() bool {
	return c.copyCapture != nil && c.toplevelSrc != nil && c.toplevelList != nil && c.shm != nil
}

// HasOutputCapture reports whether outputs can be captured through
// ext-image-copy-capture (the continuous Stream path).
func (c *Client) HasOutputCapture() bool {
	return c.copyCapture != nil && c.outputSource != nil && c.shm != nil
}

// HasHyprlandExport reports whether Hyprland's toplevel-export protocol
// is available.
func (c *Client) HasHyprlandExport() bool { return c.hyprland != nil && c.shm != nil }

// HandleRegistryGlobal implements wl.RegistryGlobalHandler: bind the
// capture globals the compositor advertises.
func (c *Client) HandleRegistryGlobal(ev wl.RegistryGlobalEvent) {
	switch ev.Interface {
	case ifaceShm:
		c.shm = wlclient.RegistryBindShmInterface(c.reg, ev.Name, 1)
	case ifaceOutput:
		out := wl.NewOutput(c.ctx)
		if c.bind(ev, maxOutputVersion, out) {
			c.addOutput(out, ev.Name)
		}
	case ifaceScreencopy:
		mgr := wlr.NewZwlrScreencopyManagerV1(c.ctx)
		if c.bind(ev, maxScreencopyVersion, mgr) {
			c.screencopy = mgr
			c.scVersion = min(ev.Version, maxScreencopyVersion)
		}
	case ifaceDmabuf:
		d := wlr.NewZwpDmabufV1(c.ctx)
		if c.bind(ev, maxDmabufVersion, d) {
			c.dmabuf = d
		}
	case ifaceCopyCapture:
		mgr := wlr.NewImageCopyCaptureManagerV1(c.ctx)
		if c.bind(ev, 1, mgr) {
			c.copyCapture = mgr
		}
	case ifaceOutputSource:
		mgr := wlr.NewOutputImageCaptureSourceManagerV1(c.ctx)
		if c.bind(ev, 1, mgr) {
			c.outputSource = mgr
		}
	case ifaceToplevelSrc:
		mgr := wlr.NewForeignToplevelImageCaptureSourceManagerV1(c.ctx)
		if c.bind(ev, 1, mgr) {
			c.toplevelSrc = mgr
		}
	case ifaceToplevelList:
		list := wlr.NewForeignToplevelListV1(c.ctx)
		if c.bind(ev, 1, list) {
			c.toplevelList = list
			list.AddToplevelHandler(c)
		}
	case ifaceHyprlandShare:
		mgr := wlr.NewToplevelExportManagerV1(c.ctx)
		if c.bind(ev, maxHyprlandVersion, mgr) {
			c.hyprland = mgr
		}
	}
}

// HandleRegistryGlobalRemove implements wl.RegistryGlobalRemoveHandler:
// an unplugged output leaves the list.
func (c *Client) HandleRegistryGlobalRemove(ev wl.RegistryGlobalRemoveEvent) {
	for i, o := range c.outputs {
		if o.global == ev.Name {
			c.outputs = append(c.outputs[:i], c.outputs[i+1:]...)
			return
		}
	}
}

// bind binds one global at min(advertised, ceiling), reporting success.
func (c *Client) bind(ev wl.RegistryGlobalEvent, ceiling uint32, obj wl.Proxy) bool {
	return c.reg.Bind(ev.Name, ev.Interface, min(ev.Version, ceiling), obj) == nil
}

// HandleDisplayError implements wl.DisplayErrorHandler: keep the
// compositor's fatal verdict so the failing call names it.
func (c *Client) HandleDisplayError(ev wl.DisplayErrorEvent) {
	var id wl.ProxyId
	if ev.ObjectId != nil {
		id = ev.ObjectId.Id()
	}
	c.protoErr = fmt.Errorf("capture: compositor error on object %d (code %d): %s", id, ev.Code, ev.Message)
}

// HandleDisplayDeleteId implements wl.DisplayDeleteIdHandler: the
// compositor finished with a destroyed object, so its id leaves the
// proxy map. Without this every capture would leak its frame and
// buffer proxies for the connection's lifetime.
func (c *Client) HandleDisplayDeleteId(ev wl.DisplayDeleteIdEvent) {
	c.ctx.Unregister(wl.ProxyId(ev.Id))
}

// usable fails once the client is closed or the compositor killed the
// connection.
func (c *Client) usable() error {
	if c.closed {
		return ErrClosed
	}
	return c.protoErr
}

// roundtrip blocks until the compositor processed every request sent
// so far and every event it answered with was dispatched.
func (c *Client) roundtrip() error {
	cb, err := c.display.Sync()
	if err != nil {
		return err
	}
	return c.check(dispatchTolerant(func() error { return c.ctx.RunTill(cb) }))
}

// dispatchUntil dispatches events one at a time until done reports
// true.
func (c *Client) dispatchUntil(done func() bool) error {
	for !done() {
		if err := c.check(dispatchTolerant(c.ctx.Run)); err != nil {
			return err
		}
	}
	return nil
}

// check prefers the compositor's recorded verdict over the transport
// error it caused.
func (c *Client) check(err error) error {
	if err != nil && c.protoErr != nil {
		return c.protoErr
	}
	if err != nil {
		return fmt.Errorf("capture: dispatch: %w", err)
	}
	return c.protoErr
}

// maxProxyNilRetries bounds how many events for already-destroyed
// objects one dispatch step skips before giving up.
const maxProxyNilRetries = 64

// dispatchTolerant runs one dispatch step, skipping events addressed
// to objects this side already destroyed: a frame the client dropped
// can still have events in flight, and those abort the step with
// wl.ErrContextRunProxyNil without meaning anything.
func dispatchTolerant(run func() error) error {
	err := run()
	for i := 0; errors.Is(err, wl.ErrContextRunProxyNil); i++ {
		if i >= maxProxyNilRetries {
			return err
		}
		err = run()
	}
	return err
}
