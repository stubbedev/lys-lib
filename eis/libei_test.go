package eis

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/clib"
)

// libei is the reference C client, the oracle the server is held to.
type libei struct {
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
	bindCaps       uintptr // variadic, NULL-terminated
}

var (
	libeiOnce sync.Once
	libeiLib  *libei
	libeiErr  error
)

func loadLibei(t *testing.T) *libei {
	t.Helper()
	libeiOnce.Do(func() {
		h, err := clib.Open(clib.SystemCandidates("libei.so.1"))
		if err != nil {
			libeiErr = err
			return
		}
		l := &libei{}
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
				libeiErr = fmt.Errorf("%s: %w", f.name, err)
				return
			}
			purego.RegisterFunc(f.fn, sym)
		}
		if l.bindCaps, err = purego.Dlsym(h, "ei_seat_bind_capabilities"); err != nil {
			libeiErr = err
			return
		}
		libeiLib = l
	})
	if libeiErr != nil {
		t.Fatalf("libei: %v", libeiErr)
	}
	return libeiLib
}

// libei's event types and capabilities (libei.h).
const (
	evConnect       = 1
	evDisconnect    = 2
	evSeatAdded     = 3
	evDeviceResumed = 8

	eiCapPointer  = 1 << 0
	eiCapAbsolute = 1 << 1
	eiCapKeyboard = 1 << 2
	eiCapTouch    = 1 << 3
	eiCapScroll   = 1 << 4
	eiCapButton   = 1 << 5
	eiCapText     = 1 << 6
)

// recorder is the Handler under test.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, s)
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func (r *recorder) Motion(dx, dy float64)       { r.add(fmt.Sprint("motion ", dx, " ", dy)) }
func (r *recorder) Button(c uint32, p bool)     { r.add(fmt.Sprint("button ", c, " ", p)) }
func (r *recorder) Scroll(dx, dy float64)       { r.add(fmt.Sprint("scroll ", dx, " ", dy)) }
func (r *recorder) ScrollDiscrete(dx, dy int32) { r.add(fmt.Sprint("discrete ", dx, " ", dy)) }
func (r *recorder) ScrollStop(x, y bool)        { r.add(fmt.Sprint("stop ", x, " ", y)) }
func (r *recorder) Key(c uint32, p bool)        { r.add(fmt.Sprint("key ", c, " ", p)) }
func (r *recorder) Keysym(sym uint32, p bool)   { r.add(fmt.Sprint("keysym ", sym, " ", p)) }
func (r *recorder) MotionAbsolute(x, y float64) { r.add(fmt.Sprint("absolute ", x, " ", y)) }
func (r *recorder) TouchDown(id uint32, x, y float64) {
	r.add(fmt.Sprint("down ", id, " ", x, " ", y))
}

func (r *recorder) TouchMotion(id uint32, x, y float64) {
	r.add(fmt.Sprint("touch ", id, " ", x, " ", y))
}
func (r *recorder) TouchUp(id uint32)     { r.add(fmt.Sprint("up ", id)) }
func (r *recorder) TouchCancel(id uint32) { r.add(fmt.Sprint("cancel ", id)) }

// testSeat is the seat the tests' servers advertise.
const testSeat = "lys-lib-test"

// session is what a libei sender saw of the server.
type session struct {
	connected bool
	// seats and devices are the names of the seats added and the
	// devices resumed, in order.
	seats, devices []string
	// regions are the resumed devices' regions, as
	// "device x,y wxh mapping".
	regions []string
}

// drive connects a libei sender to fd and emulates input on every
// device the server resumes, until resumed devices reach want or the
// timeout passes.
func drive(t *testing.T, l *libei, fd int, caps []uintptr, want int, timeout time.Duration) session {
	t.Helper()
	var got session
	ei := l.newSender(nil)
	defer l.unref(ei)
	l.configureName(ei, "lys-lib-test")
	if rc := l.setupFd(ei, int32(fd)); rc != 0 {
		t.Fatalf("ei_setup_backend_fd = %d", rc)
	}
	efd := int(l.getFd(ei))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && len(got.devices) < want {
		pfd := []unix.PollFd{{Fd: int32(efd), Events: unix.POLLIN}}
		if _, err := unix.Poll(pfd, 100); err != nil && !errors.Is(err, unix.EINTR) {
			t.Fatalf("poll the libei fd: %v", err)
		}
		l.dispatch(ei)
		for ev := l.getEvent(ei); ev != 0; ev = l.getEvent(ei) {
			switch l.eventType(ev) {
			case evConnect:
				got.connected = true
			case evDisconnect:
				t.Error("the server disconnected")
			case evSeatAdded:
				seat := l.eventSeat(ev)
				got.seats = append(got.seats, l.seatName(seat))
				args := append([]uintptr{seat}, caps...)
				purego.SyscallN(l.bindCaps, append(args, 0)...)
			case evDeviceResumed:
				dev := l.eventDevice(ev)
				got.devices = append(got.devices, l.deviceName(dev))
				for i := uintptr(0); ; i++ {
					r := l.getRegion(dev, i)
					if r == 0 {
						break
					}
					got.regions = append(got.regions, fmt.Sprintf("%s %d,%d %dx%d %s", l.deviceName(dev),
						l.regionX(r), l.regionY(r), l.regionWidth(r), l.regionHeight(r), l.regionMapping(r)))
				}
				emulate(l, ei, dev)
			}
			l.eventUnref(ev)
		}
	}
	// Let the last requests go out.
	for range 3 {
		l.dispatch(ei)
		time.Sleep(20 * time.Millisecond)
	}
	return got
}

func emulate(l *libei, ei, dev uintptr) {
	l.startEmulating(dev, 1)
	if l.hasCap(dev, eiCapPointer) {
		l.motion(dev, 1.5, -2)
		l.button(dev, 0x110, true)
		l.scrollDelta(dev, 0, 3)
		l.scrollStop(dev, false, true)
		l.scrollDiscrete(dev, 0, 240)
	}
	if l.hasCap(dev, eiCapKeyboard) {
		l.key(dev, 30, true)
		l.key(dev, 30, false)
	}
	if l.hasCap(dev, eiCapText) {
		l.keysym(dev, 0x41, true)
	}
	if l.hasCap(dev, eiCapAbsolute) {
		r := l.getRegion(dev, 1)
		l.motionAbsolute(dev, float64(l.regionX(r))+float64(l.regionWidth(r))/2, float64(l.regionY(r))+float64(l.regionHeight(r))/2)
	}
	if l.hasCap(dev, eiCapTouch) {
		touch := l.touchNew(dev)
		l.touchDown(touch, 10, 20)
		l.frame(dev, l.now(ei))
		l.touchMotion(touch, 30, 40)
		l.frame(dev, l.now(ei))
		l.touchUp(touch)
		l.touchUnref(touch)
	}
	l.frame(dev, l.now(ei))
	l.stopEmulating(dev)
}

func TestLibeiSenderEmulatesThroughTheServer(t *testing.T) {
	l := loadLibei(t)
	rec := &recorder{}
	client, err := Pair(rec, testSeat, nil)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(client.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	got := drive(t, l, fd, []uintptr{eiCapPointer, eiCapKeyboard, eiCapButton, eiCapScroll, eiCapText}, 2, 5*time.Second)
	if !got.connected || len(got.devices) != 2 {
		t.Fatalf("connected %v, devices %q resumed; want a keyboard and a pointer", got.connected, got.devices)
	}
	if want := []string{testSeat}; !slices.Equal(got.seats, want) {
		t.Errorf("seats %q, want %q", got.seats, want)
	}
	if want := []string{testSeat + "-keyboard", testSeat + "-pointer"}; !slices.Equal(got.devices, want) {
		t.Errorf("devices %q, want %q", got.devices, want)
	}
	deadline := time.Now().Add(2 * time.Second)
	want := []string{"motion 1.5 -2", "button 272 true", "scroll 0 3", "stop false true", "discrete 0 240", "key 30 true", "key 30 false", "keysym 65 true"}
	for time.Now().Before(deadline) && len(rec.list()) < len(want) {
		time.Sleep(10 * time.Millisecond)
	}
	handled := rec.list()
	slices.Sort(handled)
	sorted := slices.Clone(want)
	slices.Sort(sorted)
	if !slices.Equal(handled, sorted) {
		t.Errorf("handled %q, want %q", rec.list(), want)
	}
}

func TestLibeiPointerOnlyBindGetsNoKeyboard(t *testing.T) {
	l := loadLibei(t)
	rec := &recorder{}
	client, err := Pair(rec, testSeat, nil)
	if err != nil {
		t.Fatal(err)
	}
	fd, _ := syscall.Dup(int(client.Fd()))
	_ = client.Close()
	got := drive(t, l, fd, []uintptr{eiCapPointer, eiCapButton, eiCapScroll}, 2, 500*time.Millisecond)
	if want := []string{testSeat + "-pointer"}; !slices.Equal(got.devices, want) {
		t.Fatalf("devices %q resumed, want %q alone", got.devices, want)
	}
	for _, e := range rec.list() {
		if e[:3] == "key" {
			t.Errorf("a keyboard event without a keyboard bind: %s", e)
		}
	}
}

// A seat with regions gives an absolute pointer and a touchscreen over
// each of them, every region announced with its mapping id; absolute
// motion to a region's centre and a touch inside the first reach the
// Handler in layout coordinates.
func TestLibeiAbsoluteDevicesReachTheRegions(t *testing.T) {
	l := loadLibei(t)
	rec := &recorder{}
	regions := []Region{
		{X: 0, Y: 0, Width: 1920, Height: 1080, Scale: 1, MappingID: "41"},
		{X: 1920, Y: 0, Width: 1280, Height: 1024, Scale: 1.5, MappingID: "57"},
	}
	client, err := Pair(rec, testSeat, regions)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(client.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	got := drive(t, l, fd, []uintptr{eiCapAbsolute, eiCapTouch, eiCapButton}, 2, 5*time.Second)
	if want := []string{testSeat + "-absolute", testSeat + "-touchscreen"}; !slices.Equal(got.devices, want) {
		t.Fatalf("devices %q resumed, want %q", got.devices, want)
	}
	want := []string{
		testSeat + "-absolute 0,0 1920x1080 41", testSeat + "-absolute 1920,0 1280x1024 57",
		testSeat + "-touchscreen 0,0 1920x1080 41", testSeat + "-touchscreen 1920,0 1280x1024 57",
	}
	if !slices.Equal(got.regions, want) {
		t.Errorf("regions %q, want %q", got.regions, want)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(rec.list()) < 4 {
		time.Sleep(10 * time.Millisecond)
	}
	events := rec.list()
	var id uint32
	if len(events) > 1 {
		_, _ = fmt.Sscanf(events[1], "down %d", &id)
	}
	handled := []string{"absolute 2560 512", fmt.Sprint("down ", id, " 10 20"), fmt.Sprint("touch ", id, " 30 40"), fmt.Sprint("up ", id)}
	if got := events; id == 0 || !slices.Equal(got, handled) {
		t.Errorf("handled %q, want %q", got, handled)
	}
}

// A seat without regions offers neither an absolute pointer nor a
// touchscreen: binding them alone gets no device.
func TestLibeiNoRegionsNoAbsoluteDevices(t *testing.T) {
	l := loadLibei(t)
	rec := &recorder{}
	client, err := Pair(rec, testSeat, nil)
	if err != nil {
		t.Fatal(err)
	}
	fd, _ := syscall.Dup(int(client.Fd()))
	_ = client.Close()
	got := drive(t, l, fd, []uintptr{eiCapAbsolute, eiCapTouch}, 1, 500*time.Millisecond)
	if len(got.devices) != 0 || len(rec.list()) != 0 {
		t.Errorf("devices %q and events %q without regions", got.devices, rec.list())
	}
}
