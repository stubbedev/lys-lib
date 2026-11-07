// Package eistest drives an EIS server with libei, the reference C
// client, loaded through purego: the oracle an EIS server, and a portal
// serving one, are held to in tests.
package eistest

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/clib"
	"github.com/stubbedev/lys-lib/intconv"
)

// Lib is libei's sender API.
type Lib struct {
	newSender      func(user unsafe.Pointer) uintptr
	configureName  func(ei uintptr, name string)
	setupFd        func(ei uintptr, fd int32) int32
	getFd          func(ei uintptr) int32
	dispatch       func(ei uintptr)
	getEvent       func(ei uintptr) uintptr
	eventType      func(ev uintptr) int32
	eventSeat      func(ev uintptr) uintptr
	eventDevice    func(ev uintptr) uintptr
	seatName       func(seat uintptr) string
	deviceName     func(dev uintptr) string
	eventUnref     func(ev uintptr) uintptr
	unref          func(ei uintptr) uintptr
	now            func(ei uintptr) uint64
	hasCap         func(dev uintptr, c uint32) bool
	startEmulating func(dev uintptr, seq uint32)
	stopEmulating  func(dev uintptr)
	frame          func(dev uintptr, t uint64)
	motion         func(dev uintptr, x, y float64)
	button         func(dev uintptr, code uint32, press bool)
	scrollDelta    func(dev uintptr, x, y float64)
	scrollDiscrete func(dev uintptr, x, y int32)
	scrollStop     func(dev uintptr, x, y bool)
	key            func(dev uintptr, code uint32, press bool)
	keysym         func(dev uintptr, sym uint32, press bool)
	motionAbsolute func(dev uintptr, x, y float64)
	touchNew       func(dev uintptr) uintptr
	touchDown      func(touch uintptr, x, y float64)
	touchMotion    func(touch uintptr, x, y float64)
	touchUp        func(touch uintptr)
	touchUnref     func(touch uintptr) uintptr
	getRegion      func(dev uintptr, index uintptr) uintptr
	regionX        func(region uintptr) uint32
	regionY        func(region uintptr) uint32
	regionWidth    func(region uintptr) uint32
	regionHeight   func(region uintptr) uint32
	regionMapping  func(region uintptr) string
	bindCaps       uintptr
}

var (
	libOnce sync.Once
	lib     *Lib
	libErr  error
)

// Load binds libei once per process; a missing library fails the test.
func Load(t testing.TB) *Lib {
	t.Helper()
	libOnce.Do(func() { lib, libErr = bind() })
	if libErr != nil {
		t.Fatalf("libei: %v", libErr)
	}
	return lib
}

func bind() (*Lib, error) {
	h, err := clib.Open(clib.SystemCandidates("libei.so.1"))
	if err != nil {
		return nil, err
	}
	l := &Lib{}
	for _, f := range []struct {
		fn   any
		name string
	}{
		{&l.newSender, "ei_new_sender"},
		{&l.configureName, "ei_configure_name"},
		{&l.setupFd, "ei_setup_backend_fd"},
		{&l.getFd, "ei_get_fd"},
		{&l.dispatch, "ei_dispatch"},
		{&l.getEvent, "ei_get_event"},
		{&l.eventType, "ei_event_get_type"},
		{&l.eventSeat, "ei_event_get_seat"},
		{&l.eventDevice, "ei_event_get_device"},
		{&l.seatName, "ei_seat_get_name"},
		{&l.deviceName, "ei_device_get_name"},
		{&l.eventUnref, "ei_event_unref"},
		{&l.unref, "ei_unref"},
		{&l.now, "ei_now"},
		{&l.hasCap, "ei_device_has_capability"},
		{&l.startEmulating, "ei_device_start_emulating"},
		{&l.stopEmulating, "ei_device_stop_emulating"},
		{&l.frame, "ei_device_frame"},
		{&l.motion, "ei_device_pointer_motion"},
		{&l.button, "ei_device_button_button"},
		{&l.scrollDelta, "ei_device_scroll_delta"},
		{&l.scrollDiscrete, "ei_device_scroll_discrete"},
		{&l.scrollStop, "ei_device_scroll_stop"},
		{&l.key, "ei_device_keyboard_key"},
		{&l.keysym, "ei_device_text_keysym"},
		{&l.motionAbsolute, "ei_device_pointer_motion_absolute"},
		{&l.touchNew, "ei_device_touch_new"},
		{&l.touchDown, "ei_touch_down"},
		{&l.touchMotion, "ei_touch_motion"},
		{&l.touchUp, "ei_touch_up"},
		{&l.touchUnref, "ei_touch_unref"},
		{&l.getRegion, "ei_device_get_region"},
		{&l.regionX, "ei_region_get_x"},
		{&l.regionY, "ei_region_get_y"},
		{&l.regionWidth, "ei_region_get_width"},
		{&l.regionHeight, "ei_region_get_height"},
		{&l.regionMapping, "ei_region_get_mapping_id"},
	} {
		sym, err := purego.Dlsym(h, f.name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.name, err)
		}
		purego.RegisterFunc(f.fn, sym)
	}
	if l.bindCaps, err = purego.Dlsym(h, "ei_seat_bind_capabilities"); err != nil {
		return nil, err
	}
	return l, nil
}

// libei's event types (libei.h).
const (
	evConnect       = 1
	evDisconnect    = 2
	evSeatAdded     = 3
	evDeviceResumed = 8
)

// The device capabilities a client binds (libei.h's EI_DEVICE_CAP_*).
const (
	CapPointer  = 1 << 0
	CapAbsolute = 1 << 1
	CapKeyboard = 1 << 2
	CapTouch    = 1 << 3
	CapScroll   = 1 << 4
	CapButton   = 1 << 5
	CapText     = 1 << 6
)

// Region is a device region as the client sees it.
type Region struct {
	X, Y, Width, Height uint32
	MappingID           string
}

// Session is what a libei sender saw of the server.
type Session struct {
	Connected bool
	// Seats and Devices are the names of the seats added and the
	// devices resumed, in order.
	Seats, Devices []string
	// Regions are each resumed device's regions, by device name.
	Regions map[string][]Region
}

// Device is a resumed device, for the emulate callback of Drive.
type Device struct {
	l       *Lib
	ei, dev uintptr
}

// Name is the device's name.
func (d Device) Name() string { return d.l.deviceName(d.dev) }

// Has reports whether the device has capability c.
func (d Device) Has(c uint32) bool { return d.l.hasCap(d.dev, c) }

// Start begins an emulation sequence.
func (d Device) Start() { d.l.startEmulating(d.dev, 1) }

// Frame ends a frame of events.
func (d Device) Frame() { d.l.frame(d.dev, d.l.now(d.ei)) }

// Stop ends the emulation sequence.
func (d Device) Stop() { d.l.stopEmulating(d.dev) }

// Motion moves the pointer relatively.
func (d Device) Motion(x, y float64) { d.l.motion(d.dev, x, y) }

// MotionAbsolute moves the pointer to a point of the regions.
func (d Device) MotionAbsolute(x, y float64) { d.l.motionAbsolute(d.dev, x, y) }

// Button presses or releases a button.
func (d Device) Button(code uint32, press bool) { d.l.button(d.dev, code, press) }

// ScrollDelta scrolls smoothly.
func (d Device) ScrollDelta(x, y float64) { d.l.scrollDelta(d.dev, x, y) }

// ScrollStop ends a smooth scroll on the axes set.
func (d Device) ScrollStop(x, y bool) { d.l.scrollStop(d.dev, x, y) }

// ScrollDiscrete scrolls in 120ths of a detent.
func (d Device) ScrollDiscrete(x, y int32) { d.l.scrollDiscrete(d.dev, x, y) }

// Key presses or releases a key.
func (d Device) Key(code uint32, press bool) { d.l.key(d.dev, code, press) }

// Keysym presses or releases a keysym.
func (d Device) Keysym(sym uint32, press bool) { d.l.keysym(d.dev, sym, press) }

// Touch puts a touch point down at (x, y), moves it to (mx, my) in the
// next frame, and lifts it in the frame after.
func (d Device) Touch(x, y, mx, my float64) {
	touch := d.l.touchNew(d.dev)
	d.l.touchDown(touch, x, y)
	d.Frame()
	d.l.touchMotion(touch, mx, my)
	d.Frame()
	d.l.touchUp(touch)
	d.l.touchUnref(touch)
}

func (d Device) regions() []Region {
	var out []Region
	for i := uintptr(0); ; i++ {
		r := d.l.getRegion(d.dev, i)
		if r == 0 {
			return out
		}
		out = append(out, Region{d.l.regionX(r), d.l.regionY(r), d.l.regionWidth(r), d.l.regionHeight(r), d.l.regionMapping(r)})
	}
}

// Drive connects a libei sender called name to fd (which it takes
// over), binds caps on every seat, and runs emulate on every device the
// server resumes, until resumed devices reach want or the timeout
// passes.
func (l *Lib) Drive(t testing.TB, fd int, name string, caps []uintptr, want int, timeout time.Duration, emulate func(Device)) Session {
	t.Helper()
	got := Session{Regions: map[string][]Region{}}
	ei := l.newSender(nil)
	defer l.unref(ei)
	l.configureName(ei, name)
	cfd, err := intconv.Exact[int32](fd)
	if err != nil {
		t.Fatalf("libei fd %d: %v", fd, err)
	}
	if rc := l.setupFd(ei, cfd); rc != 0 {
		t.Fatalf("ei_setup_backend_fd = %d", rc)
	}
	efd := l.getFd(ei)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && len(got.Devices) < want {
		pfd := []unix.PollFd{{Fd: efd, Events: unix.POLLIN}}
		if _, err := unix.Poll(pfd, 100); err != nil && !errors.Is(err, unix.EINTR) {
			t.Fatalf("poll the libei fd: %v", err)
		}
		l.dispatch(ei)
		for ev := l.getEvent(ei); ev != 0; ev = l.getEvent(ei) {
			switch l.eventType(ev) {
			case evConnect:
				got.Connected = true
			case evDisconnect:
				t.Error("the server disconnected")
			case evSeatAdded:
				seat := l.eventSeat(ev)
				got.Seats = append(got.Seats, l.seatName(seat))
				args := append([]uintptr{seat}, caps...)
				purego.SyscallN(l.bindCaps, append(args, 0)...)
			case evDeviceResumed:
				d := Device{l: l, ei: ei, dev: l.eventDevice(ev)}
				got.Devices = append(got.Devices, d.Name())
				if regions := d.regions(); len(regions) > 0 {
					got.Regions[d.Name()] = regions
				}
				emulate(d)
			}
			l.eventUnref(ev)
		}
	}
	for range 3 {
		l.dispatch(ei)
		time.Sleep(20 * time.Millisecond)
	}
	return got
}
