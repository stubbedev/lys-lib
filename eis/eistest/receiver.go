package eistest

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/clib"
	"github.com/stubbedev/lys-lib/intconv"
)

type receiverLib struct {
	*Lib
	newReceiver   func(user unsafe.Pointer) uintptr
	sequence      func(ev uintptr) uint32
	dx, dy        func(ev uintptr) float64
	button        func(ev uintptr) uint32
	buttonPress   func(ev uintptr) bool
	scrollDX      func(ev uintptr) float64
	scrollDY      func(ev uintptr) float64
	stopX, stopY  func(ev uintptr) bool
	discreteDX    func(ev uintptr) int32
	discreteDY    func(ev uintptr) int32
	key           func(ev uintptr) uint32
	keyPress      func(ev uintptr) bool
	modsDepressed func(ev uintptr) uint32
	modsLatched   func(ev uintptr) uint32
	modsLocked    func(ev uintptr) uint32
	modsGroup     func(ev uintptr) uint32
	keymap        func(dev uintptr) uintptr
	keymapSize    func(km uintptr) uintptr
	keymapType    func(km uintptr) int32
	keymapFd      func(km uintptr) int32
}

var (
	receiverOnce sync.Once
	receiver     *receiverLib
	receiverErr  error
)

func loadReceiver(t testing.TB) *receiverLib {
	t.Helper()
	base := Load(t)
	receiverOnce.Do(func() { receiver, receiverErr = bindReceiver(base) })
	if receiverErr != nil {
		t.Fatalf("libei: %v", receiverErr)
	}
	return receiver
}

func bindReceiver(base *Lib) (*receiverLib, error) {
	h, err := clib.Open(clib.SystemCandidates("libei.so.1"))
	if err != nil {
		return nil, err
	}
	l := &receiverLib{Lib: base}
	return l, bindSymbols(h, []symbol{
		{&l.newReceiver, "ei_new_receiver"},
		{&l.sequence, "ei_event_emulating_get_sequence"},
		{&l.dx, "ei_event_pointer_get_dx"},
		{&l.dy, "ei_event_pointer_get_dy"},
		{&l.button, "ei_event_button_get_button"},
		{&l.buttonPress, "ei_event_button_get_is_press"},
		{&l.scrollDX, "ei_event_scroll_get_dx"},
		{&l.scrollDY, "ei_event_scroll_get_dy"},
		{&l.stopX, "ei_event_scroll_get_stop_x"},
		{&l.stopY, "ei_event_scroll_get_stop_y"},
		{&l.discreteDX, "ei_event_scroll_get_discrete_dx"},
		{&l.discreteDY, "ei_event_scroll_get_discrete_dy"},
		{&l.key, "ei_event_keyboard_get_key"},
		{&l.keyPress, "ei_event_keyboard_get_key_is_press"},
		{&l.modsDepressed, "ei_event_keyboard_get_xkb_mods_depressed"},
		{&l.modsLatched, "ei_event_keyboard_get_xkb_mods_latched"},
		{&l.modsLocked, "ei_event_keyboard_get_xkb_mods_locked"},
		{&l.modsGroup, "ei_event_keyboard_get_xkb_group"},
		{&l.keymap, "ei_device_keyboard_get_keymap"},
		{&l.keymapSize, "ei_keymap_get_size"},
		{&l.keymapType, "ei_keymap_get_type"},
		{&l.keymapFd, "ei_keymap_get_fd"},
	})
}

const (
	evDeviceRemoved  = 6
	evModifiers      = 9
	evFrame          = 100
	evStartEmulating = 200
	evStopEmulating  = 201
	evPointerMotion  = 300
	evButton         = 500
	evScrollDelta    = 600
	evScrollStop     = 601
	evScrollDiscrete = 603
	evKeyboardKey    = 700
	keymapTypeXKB    = 1
)

// Client is a libei context connected to an EIS server that writes
// what it receives to Log, one line per event: "connect", "disconnect",
// "seat <name>", "resumed <device>", "removed <device>",
// "start <device> <sequence>", "stop <device>", "frame <device>",
// "motion <dx> <dy>", "button <code> <pressed>", "scroll <dx> <dy>",
// "scroll-stop <x> <y>", "discrete <dx> <dy>", "key <code> <pressed>"
// and "modifiers <depressed> <latched> <locked> <group>". Devices are
// named without the seat's prefix.
type Client struct {
	t       testing.TB
	l       *receiverLib
	ei      uintptr
	fd      int32
	caps    []uintptr
	Log     []string
	Keymaps map[string]string
	names   map[uintptr]string
}

// Receive connects a libei receiver called name to fd (which it takes
// over) and binds caps on every seat once Until has dispatched it.
func Receive(t testing.TB, fd int, name string, caps ...uintptr) *Client {
	t.Helper()
	l := loadReceiver(t)
	return connect(t, l, l.newReceiver, fd, name, caps)
}

// Send connects a libei sender instead, for a test that a receiver's
// server turns it away.
func Send(t testing.TB, fd int, name string, caps ...uintptr) *Client {
	t.Helper()
	l := loadReceiver(t)
	return connect(t, l, l.newSender, fd, name, caps)
}

func connect(t testing.TB, l *receiverLib, newContext func(unsafe.Pointer) uintptr, fd int, name string, caps []uintptr) *Client {
	t.Helper()
	cfd, err := intconv.Exact[int32](fd)
	if err != nil {
		t.Fatalf("libei fd %d: %v", fd, err)
	}
	ei := newContext(nil)
	t.Cleanup(func() { l.unref(ei) })
	l.configureName(ei, name)
	if rc := l.setupFd(ei, cfd); rc != 0 {
		t.Fatalf("ei_setup_backend_fd = %d", rc)
	}
	return &Client{t: t, l: l, ei: ei, fd: l.getFd(ei), caps: caps, Keymaps: map[string]string{}, names: map[uintptr]string{}}
}

// Until dispatches libei until done holds, failing the test with the
// log after five seconds.
func (c *Client) Until(what string, done func() bool) {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			c.t.Fatalf("no %s; libei saw %q", what, c.Log)
		}
		pfd := []unix.PollFd{{Fd: c.fd, Events: unix.POLLIN}}
		if _, err := unix.Poll(pfd, 50); err != nil && !errors.Is(err, unix.EINTR) {
			c.t.Fatalf("poll the libei fd: %v", err)
		}
		c.l.dispatch(c.ei)
		for ev := c.l.getEvent(c.ei); ev != 0; ev = c.l.getEvent(c.ei) {
			c.record(ev)
			c.l.eventUnref(ev)
		}
	}
}

// Saw holds once the log has every entry.
func (c *Client) Saw(entries ...string) func() bool {
	return func() bool {
		for _, e := range entries {
			if !slices.Contains(c.Log, e) {
				return false
			}
		}
		return true
	}
}

// Since is the log from the first mark on, nil without one.
func (c *Client) Since(mark string) []string {
	i := slices.Index(c.Log, mark)
	if i < 0 {
		return nil
	}
	return c.Log[i:]
}

func (c *Client) record(ev uintptr) {
	l := c.l
	device := func() string {
		name := c.names[l.eventDevice(ev)]
		if _, short, ok := strings.CutLast(name, "-"); ok {
			return short
		}
		return name
	}
	note := func(parts ...any) { c.Log = append(c.Log, strings.TrimSuffix(fmt.Sprintln(parts...), "\n")) }
	switch l.eventType(ev) {
	case evConnect:
		note("connect")
	case evDisconnect:
		note("disconnect")
	case evSeatAdded:
		seat := l.eventSeat(ev)
		note("seat", l.seatName(seat))
		purego.SyscallN(l.bindCaps, append(append([]uintptr{seat}, c.caps...), 0)...)
	case evDeviceResumed:
		dev := l.eventDevice(ev)
		c.names[dev] = l.deviceName(dev)
		note("resumed", device())
		if km := l.keymap(dev); km != 0 {
			c.Keymaps[device()] = c.readKeymap(km)
		}
	case evDeviceRemoved:
		note("removed", device())
	case evStartEmulating:
		note("start", device(), l.sequence(ev))
	case evStopEmulating:
		note("stop", device())
	case evFrame:
		note("frame", device())
	case evPointerMotion:
		note("motion", l.dx(ev), l.dy(ev))
	case evButton:
		note("button", l.button(ev), l.buttonPress(ev))
	case evScrollDelta:
		note("scroll", l.scrollDX(ev), l.scrollDY(ev))
	case evScrollStop:
		note("scroll-stop", l.stopX(ev), l.stopY(ev))
	case evScrollDiscrete:
		note("discrete", l.discreteDX(ev), l.discreteDY(ev))
	case evKeyboardKey:
		note("key", l.key(ev), l.keyPress(ev))
	case evModifiers:
		note("modifiers", l.modsDepressed(ev), l.modsLatched(ev), l.modsLocked(ev), l.modsGroup(ev))
	}
}

func (c *Client) readKeymap(km uintptr) string {
	c.t.Helper()
	if typ := c.l.keymapType(km); typ != keymapTypeXKB {
		c.t.Errorf("keymap type %d, want xkb", typ)
	}
	b := make([]byte, c.l.keymapSize(km))
	if _, err := unix.Pread(int(c.l.keymapFd(km)), b, 0); err != nil {
		c.t.Fatalf("read the keymap: %v", err)
	}
	return string(b)
}
