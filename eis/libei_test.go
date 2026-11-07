package eis

import (
	"fmt"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/eis/eistest"
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

// drive connects libei to fd and emulates input on every device the
// server resumes.
func drive(t *testing.T, fd int, caps []uintptr, want int, timeout time.Duration) eistest.Session {
	t.Helper()
	return eistest.Load(t).Drive(t, fd, testSeat, caps, want, timeout, emulate)
}

func emulate(d eistest.Device) {
	d.Start()
	if d.Has(eistest.CapPointer) {
		d.Motion(1.5, -2)
		d.Button(0x110, true)
		d.ScrollDelta(0, 3)
		d.ScrollStop(false, true)
		d.ScrollDiscrete(0, 240)
	}
	if d.Has(eistest.CapKeyboard) {
		d.Key(30, true)
		d.Key(30, false)
	}
	if d.Has(eistest.CapText) {
		d.Keysym(0x41, true)
	}
	if d.Has(eistest.CapAbsolute) {
		d.MotionAbsolute(1920+640, 512)
	}
	if d.Has(eistest.CapTouch) {
		d.Touch(10, 20, 30, 40)
	}
	d.Frame()
	d.Stop()
}

func TestLibeiSenderEmulatesThroughTheServer(t *testing.T) {
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
	got := drive(t, fd, []uintptr{eistest.CapPointer, eistest.CapKeyboard, eistest.CapButton, eistest.CapScroll, eistest.CapText}, 2, 5*time.Second)
	if !got.Connected || len(got.Devices) != 2 {
		t.Fatalf("connected %v, devices %q resumed; want a keyboard and a pointer", got.Connected, got.Devices)
	}
	if want := []string{testSeat}; !slices.Equal(got.Seats, want) {
		t.Errorf("seats %q, want %q", got.Seats, want)
	}
	if want := []string{testSeat + "-keyboard", testSeat + "-pointer"}; !slices.Equal(got.Devices, want) {
		t.Errorf("devices %q, want %q", got.Devices, want)
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
	rec := &recorder{}
	client, err := Pair(rec, testSeat, nil)
	if err != nil {
		t.Fatal(err)
	}
	fd, _ := syscall.Dup(int(client.Fd()))
	_ = client.Close()
	got := drive(t, fd, []uintptr{eistest.CapPointer, eistest.CapButton, eistest.CapScroll}, 2, 500*time.Millisecond)
	if want := []string{testSeat + "-pointer"}; !slices.Equal(got.Devices, want) {
		t.Fatalf("devices %q resumed, want %q alone", got.Devices, want)
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
	got := drive(t, fd, []uintptr{eistest.CapAbsolute, eistest.CapTouch, eistest.CapButton}, 2, 5*time.Second)
	if want := []string{testSeat + "-absolute", testSeat + "-touchscreen"}; !slices.Equal(got.Devices, want) {
		t.Fatalf("devices %q resumed, want %q", got.Devices, want)
	}
	want := []eistest.Region{{X: 0, Y: 0, Width: 1920, Height: 1080, MappingID: "41"}, {X: 1920, Y: 0, Width: 1280, Height: 1024, MappingID: "57"}}
	for _, dev := range got.Devices {
		if !slices.Equal(got.Regions[dev], want) {
			t.Errorf("%s regions %+v, want %+v", dev, got.Regions[dev], want)
		}
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
	rec := &recorder{}
	client, err := Pair(rec, testSeat, nil)
	if err != nil {
		t.Fatal(err)
	}
	fd, _ := syscall.Dup(int(client.Fd()))
	_ = client.Close()
	got := drive(t, fd, []uintptr{eistest.CapAbsolute, eistest.CapTouch}, 1, 500*time.Millisecond)
	if len(got.Devices) != 0 || len(rec.list()) != 0 {
		t.Errorf("devices %q and events %q without regions", got.Devices, rec.list())
	}
}
