// Package vinput injects pointer and keyboard input into the
// compositor through zwlr_virtual_pointer_v1 and
// zwp_virtual_keyboard_v1, on a private Wayland connection: the input
// half of a remote-desktop bridge, and of gelm's own headless test
// harness. The seat's keymap is relayed to the virtual keyboard, so
// key codes mean what they mean on the real keyboard.
package vinput

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/neurlang/wayland/wl"
	"github.com/neurlang/wayland/wlclient"

	"github.com/stubbedev/gelm/internal/keymapfd"
	"github.com/stubbedev/gelm/wlr"
)

// Options tunes Dial.
type Options struct {
	// Keyboard arms the virtual keyboard too; a pointer-only device
	// is the shape of a seat whose input devices are pointers only.
	Keyboard bool
	// FallbackKeymap is xkb_v1 text uploaded when the seat announces
	// none (a virtual-only seat, a headless compositor): the protocol
	// refuses key events before any keymap is set.
	FallbackKeymap []byte
}

// Device is one virtual pointer and, optionally, keyboard. Its methods
// are safe to call from any goroutine.
type Device struct {
	mu  sync.Mutex
	d   *wl.Display
	ctx *wl.Context

	seat  *wl.Seat
	vpMgr *wlr.ZwlrVirtualPointerManagerV1
	vp    *wlr.ZwlrVirtualPointerV1
	vk    *wlr.ZwpKeyboardV1

	start        time.Time
	outW, outH   int32
	keymap       []byte
	keymapArmed  bool
	keymapErr    error
	protoErr     error
	capabilities uint32
}

// capKeyboard is wl_seat.capabilities' keyboard bit.
const capKeyboard = 2

// Dial connects to display ("" is WAYLAND_DISPLAY) and creates the
// devices.
func Dial(display string, opts Options) (*Device, error) {
	d, err := wl.Connect(display)
	if err != nil {
		return nil, err
	}
	v := &Device{d: d, start: time.Now()}
	fail := func(err error) (*Device, error) {
		_ = d.Context().Close()
		return nil, err
	}
	reg, err := d.GetRegistry()
	if err != nil {
		return fail(err)
	}
	v.ctx, _ = wl.GetUserData[wl.Context](reg)
	d.AddErrorHandler(v)
	g := &listener{v: v}
	wlclient.RegistryAddListener(reg, g)
	if err := v.roundtrip(); err != nil {
		return fail(fmt.Errorf("vinput: registry roundtrip: %w", err))
	}
	if g.seat == 0 || g.vp == 0 || (opts.Keyboard && g.vkm == 0) {
		return fail(errors.New("vinput: compositor lacks wl_seat, zwlr_virtual_pointer_manager_v1 or zwp_virtual_keyboard_manager_v1"))
	}
	v.seat = wlclient.RegistryBindSeatInterface(reg, g.seat, 5)
	wlclient.SeatAddListener(v.seat, g)
	if g.out != 0 {
		out := wlclient.RegistryBindOutputInterface(reg, g.out, 2)
		wlclient.OutputAddListener(out, g)
	}
	v.vpMgr = wlr.NewZwlrVirtualPointerManagerV1(v.ctx)
	if err := reg.Bind(g.vp, "zwlr_virtual_pointer_manager_v1", 1, v.vpMgr); err != nil {
		return fail(err)
	}
	if v.vp, err = v.vpMgr.CreateVirtualPointer(v.seat); err != nil {
		return fail(err)
	}
	if !opts.Keyboard {
		if err := v.roundtrip(); err != nil {
			return fail(err)
		}
		return v, nil
	}
	vkMgr := wlr.NewZwpKeyboardManagerV1(v.ctx)
	if err := reg.Bind(g.vkm, "zwp_virtual_keyboard_manager_v1", 1, vkMgr); err != nil {
		return fail(err)
	}
	if v.vk, err = vkMgr.CreateKeyboard(v.seat); err != nil {
		return fail(err)
	}
	// The keyboard capability exists once a keyboard device does and
	// propagates asynchronously; get_keyboard before the seat reports it
	// is a missing_capability protocol error that kills the connection.
	deadline := time.Now().Add(5 * time.Second)
	for v.capabilities&capKeyboard == 0 {
		if err := v.roundtrip(); err != nil {
			return fail(fmt.Errorf("vinput: capability roundtrip: %w (protocol error: %w)", err, v.protoErr))
		}
		if time.Now().After(deadline) {
			return fail(errors.New("vinput: the seat never reported the keyboard capability"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	kb, err := v.seat.GetKeyboard()
	if err != nil {
		return fail(err)
	}
	wlclient.KeyboardAddListener(kb, g)
	// A few dispatches for the seat's keymap to arrive.
	for range 5 {
		if err := v.roundtrip(); err != nil {
			return fail(fmt.Errorf("vinput: keymap roundtrip: %w (protocol error: %w)", err, v.protoErr))
		}
		if v.keymapArmed || v.keymapErr != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !v.keymapArmed && v.keymapErr == nil && len(opts.FallbackKeymap) > 0 {
		v.keymapErr = v.upload(opts.FallbackKeymap)
		if v.keymapErr == nil {
			v.keymap = append([]byte(nil), opts.FallbackKeymap...)
			v.keymapArmed = true
			if err := v.roundtrip(); err != nil {
				return fail(fmt.Errorf("vinput: keymap install roundtrip: %w (protocol error: %w)", err, v.protoErr))
			}
		}
	}
	if v.keymapErr != nil {
		return fail(fmt.Errorf("vinput: arm the virtual keyboard keymap: %w", v.keymapErr))
	}
	return v, nil
}

// upload arms the virtual keyboard with keymap text through a temp
// file (format 1, xkb_v1).
func (v *Device) upload(keymap []byte) error {
	f, err := os.CreateTemp("", "gelm-vinput-keymap-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(f.Name())
		_ = f.Close()
	}()
	if _, err := f.Write(keymap); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	return v.vk.Keymap(1, f.Fd(), uint32(len(keymap)))
}

// Keymap is the xkb_v1 text the virtual keyboard carries (the seat's,
// or the fallback), nil without a keyboard.
func (v *Device) Keymap() []byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.keymap
}

// OutputSize is the first output's current mode, 0x0 without one.
func (v *Device) OutputSize() (width, height int32) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.outW, v.outH
}

func (v *Device) now() uint32 { return uint32(time.Since(v.start).Milliseconds()) }

// pointer runs one pointer request and ends the frame.
func (v *Device) pointer(req func() error) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := req(); err != nil {
		return err
	}
	return v.vp.Frame()
}

// Motion moves the pointer by (dx, dy).
func (v *Device) Motion(dx, dy float64) error {
	return v.pointer(func() error { return v.vp.Motion(v.now(), float32(dx), float32(dy)) })
}

// MotionAbsolute moves the pointer to (x, y) within an extent of
// width x height (the compositor maps it onto its layout).
func (v *Device) MotionAbsolute(x, y, width, height uint32) error {
	return v.pointer(func() error { return v.vp.MotionAbsolute(v.now(), x, y, max(width, 1), max(height, 1)) })
}

// Button presses or releases an evdev button code.
func (v *Device) Button(code uint32, pressed bool) error {
	return v.pointer(func() error { return v.vp.Button(v.now(), code, btoi(pressed)) })
}

// Axis scrolls: axis 0 vertical, 1 horizontal, value in wl_pointer
// axis units.
func (v *Device) Axis(axis uint32, value float64) error {
	return v.pointer(func() error { return v.vp.Axis(v.now(), axis, float32(value)) })
}

// AxisDiscrete scrolls whole steps (a wheel), value the axis units
// they amount to.
func (v *Device) AxisDiscrete(axis uint32, value float64, steps int32) error {
	return v.pointer(func() error { return v.vp.AxisDiscrete(v.now(), axis, float32(value), steps) })
}

// Key presses or releases an evdev key code.
func (v *Device) Key(code uint32, pressed bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vk == nil {
		return errors.New("vinput: no virtual keyboard")
	}
	return v.vk.Key(v.now(), code, btoi(pressed))
}

// Modifiers declares the modifier state explicitly (xkb masks), for a
// seat that cannot derive it from the keys.
func (v *Device) Modifiers(depressed, latched, locked, group uint32) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vk == nil {
		return errors.New("vinput: no virtual keyboard")
	}
	return v.vk.Modifiers(depressed, latched, locked, group)
}

// Roundtrip waits until the compositor processed every request sent.
func (v *Device) Roundtrip() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.roundtrip()
}

// roundtrip dispatches until a sync callback completes, retrying past
// proxies destroyed mid-queue.
func (v *Device) roundtrip() error {
	cb, err := v.d.Sync()
	if err != nil {
		return err
	}
	defer wlclient.CallbackDestroy(cb)
	for {
		err = v.d.Context().RunTill(cb)
		if err == nil {
			return nil
		}
		if err.Error() != "proxy nil" {
			return err
		}
	}
}

// Close destroys the devices and disconnects.
func (v *Device) Close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vk != nil {
		_ = v.vk.Destroy()
	}
	if v.vp != nil {
		_ = v.vp.Destroy()
	}
	if v.vpMgr != nil {
		_ = v.vpMgr.Destroy()
	}
	_ = v.d.Context().Close()
}

// HandleDisplayError implements wl.DisplayErrorHandler: the
// compositor's fatal protocol error, for Dial's failure message.
func (v *Device) HandleDisplayError(ev wl.DisplayErrorEvent) {
	v.protoErr = fmt.Errorf("object %T: %s", ev.ObjectId, ev.Message)
}

func btoi(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// listener collects the globals and carries the seat, output and
// keyboard listeners.
type listener struct {
	v                  *Device
	seat, out, vp, vkm uint32
}

func (g *listener) HandleRegistryGlobal(ev wl.RegistryGlobalEvent) {
	switch ev.Interface {
	case "wl_seat":
		g.seat = ev.Name
	case "wl_output":
		if g.out == 0 {
			g.out = ev.Name
		}
	case "zwlr_virtual_pointer_manager_v1":
		g.vp = ev.Name
	case "zwp_virtual_keyboard_manager_v1":
		g.vkm = ev.Name
	}
}

func (g *listener) HandleRegistryGlobalRemove(wl.RegistryGlobalRemoveEvent) {}

func (g *listener) HandleSeatCapabilities(ev wl.SeatCapabilitiesEvent) {
	g.v.capabilities = ev.Capabilities
}

func (g *listener) HandleSeatName(wl.SeatNameEvent) {}

func (g *listener) HandleOutputGeometry(wl.OutputGeometryEvent) {}

// HandleOutputMode tracks the current mode (flag bit 0).
func (g *listener) HandleOutputMode(ev wl.OutputModeEvent) {
	if ev.Flags&1 != 0 {
		g.v.outW, g.v.outH = ev.Width, ev.Height
	}
}

func (g *listener) HandleOutputDone(wl.OutputDoneEvent)   {}
func (g *listener) HandleOutputScale(wl.OutputScaleEvent) {}

// HandleKeyboardKeymap relays the seat's keymap to the virtual
// keyboard and keeps its text. The received fd belongs to this
// connection's loop, so it is dup'ed for the re-send (the wire layer
// closes what it sends). Format 0 is NO_KEYMAP, a virtual-only seat's
// stub: relaying it mmaps zero bytes compositor side and kills the
// connection, so it is skipped.
func (g *listener) HandleKeyboardKeymap(ev wl.KeyboardKeymapEvent) {
	v := g.v
	if ev.Format == 0 || ev.Size == 0 || ev.FdError != nil || ev.Fd == 0 || v.vk == nil {
		return
	}
	text, err := keymapfd.Read(ev.Fd, ev.Size)
	if err != nil {
		v.keymapErr = err
		return
	}
	dup, err := syscall.Dup(int(ev.Fd))
	if err != nil {
		v.keymapErr = err
		return
	}
	if err := v.vk.Keymap(ev.Format, uintptr(dup), ev.Size); err != nil {
		v.keymapErr = err
		return
	}
	v.keymap, v.keymapArmed = text, true
}

func (g *listener) HandleKeyboardEnter(wl.KeyboardEnterEvent)           {}
func (g *listener) HandleKeyboardLeave(wl.KeyboardLeaveEvent)           {}
func (g *listener) HandleKeyboardKey(wl.KeyboardKeyEvent)               {}
func (g *listener) HandleKeyboardModifiers(wl.KeyboardModifiersEvent)   {}
func (g *listener) HandleKeyboardRepeatInfo(wl.KeyboardRepeatInfoEvent) {}
