package wltest

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image/color"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/wayland/wl"
	"github.com/stubbedev/lys-lib/third_party/wayland/wlclient"
	"github.com/stubbedev/lys-lib/third_party/wayland/xdg"
	"github.com/stubbedev/lys-lib/wlr"
)

// Layer anchors, wlr-layer-shell's: a layer anchored to two opposite
// edges stretches between them.
const (
	AnchorTop    = wlr.ZwlrLayerSurfaceV1AnchorTop
	AnchorBottom = wlr.ZwlrLayerSurfaceV1AnchorBottom
	AnchorLeft   = wlr.ZwlrLayerSurfaceV1AnchorLeft
	AnchorRight  = wlr.ZwlrLayerSurfaceV1AnchorRight
)

// Painter is a Wayland client that shows solid-colored surfaces: layer
// surfaces on the first output and toplevel windows, each repainted at
// whatever size the compositor configures it to. Its events run on a
// goroutine of their own until the test ends.
type Painter struct {
	t       *testing.T
	display *wl.Display
	ctx     *wl.Context
	reg     *wl.Registry
	comp    *wl.Compositor
	shm     *wl.Shm
	wm      *xdg.WmBase
	layers  *wlr.ZwlrLayerShellV1
	output  *wl.Output
	mu      sync.Mutex
	err     error
	surface []*painted
	// paintMu serializes painting between the event goroutine's
	// configures, the test's resizes and the connection's close; closed
	// stops the configures still buffered at the close from painting
	// on a closed connection.
	paintMu sync.Mutex
	closed  bool
}

// painted is one surface the painter fills with color.
type painted struct {
	p     *Painter
	s     *wl.Surface
	color color.RGBA
	layer *wlr.ZwlrLayerSurfaceV1
	xs    *xdg.Surface
	top   *xdg.Toplevel
	w, h  int32
	// configW and configH are the latest toplevel configure, applied at
	// the xdg_surface configure that ends it; 0 leaves an axis as it is.
	configW, configH int32
	shown            bool
	// paints counts the buffers committed.
	paints int
	buffer *wl.Buffer
}

// NewPainter connects to the compositor WAYLAND_DISPLAY names and
// binds what painting needs; the connection closes at cleanup.
func NewPainter(t *testing.T) *Painter {
	t.Helper()
	display, err := wl.Connect("")
	if err != nil {
		t.Fatalf("wltest: connect: %v", err)
	}
	p := &Painter{t: t, display: display, ctx: display.Context()}
	reg, err := display.GetRegistry()
	if err != nil {
		t.Fatalf("wltest: registry: %v", err)
	}
	p.reg = reg
	wlclient.RegistryAddListener(reg, p)
	if err := p.roundtrip(); err != nil {
		t.Fatalf("wltest: globals: %v", err)
	}
	if p.comp == nil || p.shm == nil || p.wm == nil || p.layers == nil || p.output == nil {
		t.Fatalf("wltest: the compositor lacks a global painting needs (compositor %v, shm %v, xdg_wm_base %v, layer shell %v, output %v)",
			p.comp != nil, p.shm != nil, p.wm != nil, p.layers != nil, p.output != nil)
	}
	t.Cleanup(p.close)
	return p
}

// HandleRegistryGlobal binds the globals painting needs.
func (p *Painter) HandleRegistryGlobal(ev wl.RegistryGlobalEvent) {
	switch ev.Interface {
	case "wl_compositor":
		p.comp = wlclient.RegistryBindCompositorInterface(p.reg, ev.Name, min(ev.Version, 4))
	case "wl_shm":
		p.shm = wlclient.RegistryBindShmInterface(p.reg, ev.Name, 1)
	case "xdg_wm_base":
		p.wm = wlclient.RegistryBindWmBaseInterface(p.reg, ev.Name, 1)
		p.wm.AddPingHandler(p)
	case "zwlr_layer_shell_v1":
		l := wlr.NewZwlrLayerShellV1(p.ctx)
		if p.reg.Bind(ev.Name, ev.Interface, min(ev.Version, 4), l) == nil {
			p.layers = l
		}
	case "wl_output":
		if p.output == nil {
			p.output = wlclient.RegistryBindOutputInterface(p.reg, ev.Name, 1)
		}
	}
}

// HandleRegistryGlobalRemove implements the registry listener.
func (p *Painter) HandleRegistryGlobalRemove(wl.RegistryGlobalRemoveEvent) {}

// HandleWmBasePing answers the compositor's ping.
func (p *Painter) HandleWmBasePing(ev xdg.WmBasePingEvent) { p.fail(p.wm.Pong(ev.Serial)) }

// close closes the connection once no configure is painting.
func (p *Painter) close() {
	p.paintMu.Lock()
	defer p.paintMu.Unlock()
	p.closed = true
	_ = p.ctx.Close()
}

func (p *Painter) roundtrip() error {
	cb, err := p.display.Sync()
	if err != nil {
		return err
	}
	return p.ctx.RunTill(cb)
}

func (p *Painter) fail(err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == nil {
		p.err = err
	}
}

// Layer shows an overlay layer surface of c on the first output,
// anchored to anchor, w x h where it is not stretched between two
// anchored edges (0 there).
func (p *Painter) Layer(anchor uint32, w, h int32, c color.RGBA) {
	p.t.Helper()
	s := p.newSurface(c)
	ls, err := p.layers.GetLayerSurface(s.s, p.output, wlr.ZwlrLayerShellV1LayerOverlay, "lys-test")
	if err != nil {
		p.t.Fatalf("wltest: layer surface: %v", err)
	}
	s.layer = ls
	ls.AddConfigureHandler(s)
	sw, errW := intconv.Exact[uint32](w)
	sh, errH := intconv.Exact[uint32](h)
	if err := errors.Join(errW, errH); err != nil {
		p.t.Fatalf("wltest: layer size %dx%d: %v", w, h, err)
	}
	if err := ls.SetSize(sw, sh); err != nil {
		p.t.Fatal(err)
	}
	if err := ls.SetAnchor(anchor); err != nil {
		p.t.Fatal(err)
	}
	p.show(s)
}

// Window is a toplevel a Painter shows, painted at the size the compositor
// configures, as a tiling compositor holds it to whatever the client asks.
type Window struct{ s *painted }

// Window shows a toplevel of c titled title with app id appID, w x h on an
// axis the compositor leaves to the client (Size is what it took).
func (p *Painter) Window(appID, title string, w, h int32, c color.RGBA) *Window {
	p.t.Helper()
	s := p.newSurface(c)
	s.w, s.h = w, h
	xs, err := p.wm.GetSurface(s.s)
	if err != nil {
		p.t.Fatalf("wltest: xdg surface: %v", err)
	}
	s.xs = xs
	xs.AddConfigureHandler(s)
	top, err := xs.GetToplevel()
	if err != nil {
		p.t.Fatalf("wltest: toplevel: %v", err)
	}
	s.top = top
	top.AddConfigureHandler(s)
	if err := top.SetAppId(appID); err != nil {
		p.t.Fatal(err)
	}
	if err := top.SetTitle(title); err != nil {
		p.t.Fatal(err)
	}
	p.show(s)
	return &Window{s: s}
}

// Size is the size the window is painted at.
func (w *Window) Size() (width, height int32) {
	p := w.s.p
	p.paintMu.Lock()
	defer p.paintMu.Unlock()
	return w.s.w, w.s.h
}

// Fullscreen asks the compositor to make the window fullscreen; it repaints
// at the size that configures.
func (w *Window) Fullscreen() {
	p := w.s.p
	p.t.Helper()
	p.paintMu.Lock()
	defer p.paintMu.Unlock()
	if err := w.s.top.SetFullscreen(nil); err != nil {
		p.t.Fatalf("wltest: fullscreen the window: %v", err)
	}
}

// Repaint paints the window again at its size: a change of content the
// compositor sees, with no change of size.
func (w *Window) Repaint() {
	p := w.s.p
	p.t.Helper()
	p.paintMu.Lock()
	defer p.paintMu.Unlock()
	if err := w.s.paint(); err != nil {
		p.t.Fatalf("wltest: paint the window at %dx%d: %v", w.s.w, w.s.h, err)
	}
}

// Close unmaps and destroys the window.
func (w *Window) Close() {
	p := w.s.p
	p.t.Helper()
	p.paintMu.Lock()
	defer p.paintMu.Unlock()
	s := w.s
	err := errors.Join(s.top.Destroy(), s.xs.Destroy(), s.s.Destroy())
	if s.buffer != nil {
		err = errors.Join(err, s.buffer.Destroy())
	}
	if err != nil {
		p.t.Fatalf("wltest: close the window: %v", err)
	}
}

func (p *Painter) newSurface(c color.RGBA) *painted {
	p.t.Helper()
	ws, err := p.comp.CreateSurface()
	if err != nil {
		p.t.Fatalf("wltest: surface: %v", err)
	}
	s := &painted{p: p, s: ws, color: c}
	p.surface = append(p.surface, s)
	return s
}

// show commits s bare, waits for its first configure, which paints
// it, and hands later events to the event goroutine once every
// surface is up.
func (p *Painter) show(s *painted) {
	p.t.Helper()
	if err := s.s.Commit(); err != nil {
		p.t.Fatal(err)
	}
	for !s.shown {
		if err := p.ctx.Run(); err != nil {
			p.t.Fatalf("wltest: waiting for the first configure: %v", err)
		}
		p.mu.Lock()
		err := p.err
		p.mu.Unlock()
		if err != nil {
			p.t.Fatalf("wltest: painting: %v", err)
		}
	}
	// A compositor answers a first buffer with the size it lays the surface
	// out at: settled once a roundtrip brings no repaint.
	for {
		before := s.paints
		if err := p.roundtrip(); err != nil {
			p.t.Fatalf("wltest: after painting: %v", err)
		}
		if s.paints == before {
			return
		}
	}
}

// Run hands the connection's events to a goroutine of their own, so
// surfaces are repainted when the compositor reconfigures them (an
// output turned or resized): call it once every surface is shown.
func (p *Painter) Run() {
	go func() {
		for p.ctx.Run() == nil {
		}
	}()
}

// HandleZwlrLayerSurfaceV1Configure paints the layer at its size.
func (s *painted) HandleZwlrLayerSurfaceV1Configure(ev wlr.ZwlrLayerSurfaceV1ConfigureEvent) {
	s.p.paintMu.Lock()
	defer s.p.paintMu.Unlock()
	if s.p.closed {
		return
	}
	s.p.fail(s.layer.AckConfigure(ev.Serial))
	w, errW := intconv.Exact[int32](ev.Width)
	h, errH := intconv.Exact[int32](ev.Height)
	if err := errors.Join(errW, errH); err != nil {
		s.p.fail(fmt.Errorf("wltest: layer configure %dx%d: %w", ev.Width, ev.Height, err))
		return
	}
	s.w, s.h = w, h
	s.p.fail(s.paint())
}

// HandleToplevelConfigure takes the size the compositor asks for.
func (s *painted) HandleToplevelConfigure(ev xdg.ToplevelConfigureEvent) {
	s.p.paintMu.Lock()
	defer s.p.paintMu.Unlock()
	s.configW, s.configH = ev.Width, ev.Height
}

// HandleSurfaceConfigure paints the toplevel at its configured size.
func (s *painted) HandleSurfaceConfigure(ev xdg.SurfaceConfigureEvent) {
	s.p.paintMu.Lock()
	defer s.p.paintMu.Unlock()
	if s.p.closed {
		return
	}
	s.p.fail(s.xs.AckConfigure(ev.Serial))
	if s.configW > 0 {
		s.w = s.configW
	}
	if s.configH > 0 {
		s.h = s.configH
	}
	s.p.fail(s.paint())
}

// paint attaches a fresh shm buffer of s's color at its size.
func (s *painted) paint() error {
	w, h := s.w, s.h
	if w <= 0 || h <= 0 {
		return fmt.Errorf("wltest: a surface configured to %dx%d", w, h)
	}
	stride, size := 4*w, 4*w*h
	fd, err := unix.MemfdCreate("lys-test", unix.MFD_CLOEXEC)
	if err != nil {
		return fmt.Errorf("wltest: memfd: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		return fmt.Errorf("wltest: size the %d-byte buffer: %w", size, err)
	}
	mem, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("wltest: map the buffer: %w", err)
	}
	px := uint32(s.color.A)<<24 | uint32(s.color.R)<<16 | uint32(s.color.G)<<8 | uint32(s.color.B)
	for i := 0; i < len(mem); i += 4 {
		binary.LittleEndian.PutUint32(mem[i:], px)
	}
	if err := unix.Munmap(mem); err != nil {
		return fmt.Errorf("wltest: unmap the buffer: %w", err)
	}
	pool, err := s.p.shm.CreatePool(uintptr(fd), size)
	if err != nil {
		return fmt.Errorf("wltest: shm pool: %w", err)
	}
	defer func() { _ = pool.Destroy() }()
	buf, err := pool.CreateBuffer(0, w, h, stride, wl.ShmFormatArgb8888)
	if err != nil {
		return fmt.Errorf("wltest: shm buffer: %w", err)
	}
	if s.buffer != nil {
		_ = s.buffer.Destroy()
	}
	s.buffer = buf
	if err := s.s.Attach(buf, 0, 0); err != nil {
		return err
	}
	if err := s.s.Damage(0, 0, w, h); err != nil {
		return err
	}
	s.shown = true
	s.paints++
	return s.s.Commit()
}
