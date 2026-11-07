package eis

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"net"
	"os"
	"slices"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type contextType uint32

const (
	contextReceiver contextType = 1
	contextSender   contextType = 2
)

func (c contextType) String() string {
	switch c {
	case contextReceiver:
		return "receiver"
	case contextSender:
		return "sender"
	}
	return fmt.Sprintf("context type %d", uint32(c))
}

const keymapTypeXKB = 1

type device struct {
	id     uint64
	name   string
	ifaces map[string]uint64
}

type keymap struct {
	file *os.File
	size uint32
}

func newKeymap(text string) (*keymap, error) {
	if text == "" {
		return nil, errors.New("eis: the keymap is empty: a receiver needs the keymap its keys are pressed in")
	}
	if len(text) >= math.MaxUint32 {
		return nil, fmt.Errorf("eis: a keymap of %d bytes exceeds the wire's u32 size", len(text))
	}
	fd, err := unix.MemfdCreate("eis-keymap", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("eis: create the keymap memfd: %w", err)
	}
	f := os.NewFile(uintptr(fd), "eis-keymap")
	if _, err := f.WriteString(text + "\x00"); err != nil {
		return nil, errors.Join(fmt.Errorf("eis: write the keymap memfd: %w", err), f.Close())
	}
	if _, err := unix.FcntlInt(f.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE|unix.F_SEAL_SEAL); err != nil {
		return nil, errors.Join(fmt.Errorf("eis: seal the keymap memfd: %w", err), f.Close())
	}
	return &keymap{file: f, size: uint32(len(text) + 1)}, nil //nolint:gosec // bounded above
}

func (k *keymap) message(keyboard uint64) *writer {
	return newMessage(keyboard, 1).u32(keymapTypeXKB).u32(k.size).fd(int(k.file.Fd()))
}

func (k *keymap) close() error {
	return k.file.Close()
}

// Receiver serves a libei receiver context: the client binds a keyboard
// and a pointer, and the caller emulates the input it captured on them,
// between Start and Stop.
type Receiver struct {
	mu        sync.Mutex
	s         *server
	emulating bool
	sequence  uint32
	started   map[uint64]bool
	closed    bool
}

// NewReceiver serves a receiver on one end of a socket pair, with a seat
// called seat whose keyboard carries the xkb keymap keymapText, and
// returns the other end for the client.
func NewReceiver(seat, keymapText string) (*Receiver, *os.File, error) {
	if err := checkSeat(seat); err != nil {
		return nil, nil, err
	}
	km, err := newKeymap(keymapText)
	if err != nil {
		return nil, nil, err
	}
	conn, client, err := socketPair()
	if err != nil {
		return nil, nil, errors.Join(err, km.close())
	}
	r := &Receiver{
		s: &server{
			conn: conn, seatName: seat, objects: map[uint64]string{0: ifHandshake}, negotiated: map[string]uint32{},
			nextID: serverIDBase, touches: map[uint32]struct{}{}, want: contextReceiver, client: contextReceiver, keymap: km,
		},
		started: map[uint64]bool{},
	}
	go func() {
		_ = r.s.run(r.dispatch)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.closed = true
		_ = conn.Close()
		_ = r.s.keymap.close()
	}()
	return r, client, nil
}

func socketPair() (*net.UnixConn, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("eis: socketpair: %w", err)
	}
	server := os.NewFile(uintptr(fds[0]), "eis-server")
	conn, err := net.FileConn(server)
	_ = server.Close()
	if err != nil {
		_ = syscall.Close(fds[1])
		return nil, nil, fmt.Errorf("eis: %w", err)
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		_ = syscall.Close(fds[1])
		return nil, nil, errors.New("eis: the socket pair is not a unix connection")
	}
	return unixConn, os.NewFile(uintptr(fds[1]), "eis-client"), nil
}

func (r *Receiver) dispatch(m message) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch iface := r.s.objects[m.object]; iface {
	case ifDevice, ifPointer, ifAbsolute, ifTouch, ifScroll, ifButton, ifKeyboard, ifText:
		if m.opcode != 0 {
			return false, fmt.Errorf("eis: the receiver client sent %s request %d, which only a sender sends", iface, m.opcode)
		}
		return false, nil
	}
	done, err := r.s.dispatch(m)
	if err != nil || done {
		return done, err
	}
	return false, r.startNew()
}

func (r *Receiver) startNew() error {
	if !r.emulating {
		return nil
	}
	for _, d := range r.s.devices {
		if err := r.start(d); err != nil {
			return err
		}
	}
	return nil
}

func (r *Receiver) start(d device) error {
	if r.started[d.id] {
		return nil
	}
	r.started[d.id] = true
	return r.s.send(newMessage(d.id, 9).u32(r.s.nextSerial()).u32(r.sequence))
}

// Close disconnects the client.
func (r *Receiver) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.s.conn.Close()
}

// Start begins an emulation on every device, now and as the client binds
// more, numbered sequence (the portal's activation id).
func (r *Receiver) Start(sequence uint32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.emulating {
		return fmt.Errorf("eis: start emulating sequence %d while sequence %d still runs", sequence, r.sequence)
	}
	r.emulating, r.sequence = true, sequence
	clear(r.started)
	return r.live(r.startNew())
}

// Stop ends the emulation Start began.
func (r *Receiver) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.emulating {
		return errors.New("eis: stop emulating while nothing emulates")
	}
	r.emulating = false
	var err error
	for _, d := range r.s.devices {
		if r.started[d.id] {
			err = errors.Join(err, r.s.send(newMessage(d.id, 10).u32(r.s.nextSerial())))
		}
	}
	clear(r.started)
	return r.live(err)
}

func (r *Receiver) live(err error) error {
	if r.closed && errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (r *Receiver) emit(iface string, build func(id uint64) *writer, frame bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.emulating {
		return fmt.Errorf("eis: an %s event outside an emulation", iface)
	}
	for _, d := range r.s.devices {
		id, ok := d.ifaces[iface]
		if !ok {
			continue
		}
		err := r.s.send(build(id))
		if err == nil && frame {
			err = r.frameDevice(d)
		}
		return r.live(err)
	}
	return nil
}

func (r *Receiver) frameDevice(d device) error {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return fmt.Errorf("eis: read the monotonic clock for a frame: %w", err)
	}
	us := uint64(time.Duration(ts.Nano()) / time.Microsecond) //nolint:gosec // the monotonic clock is positive
	return r.s.send(newMessage(d.id, 11).u32(r.s.nextSerial()).u64(us))
}

// Motion is relative pointer motion in logical pixels.
func (r *Receiver) Motion(dx, dy float64) error {
	return r.emit(ifPointer, func(id uint64) *writer { return newMessage(id, 1).f32(float32(dx)).f32(float32(dy)) }, false)
}

// Button presses or releases the evdev button code.
func (r *Receiver) Button(code uint32, pressed bool) error {
	return r.emit(ifButton, func(id uint64) *writer { return newMessage(id, 1).u32(code).u32(boolArg(pressed)) }, false)
}

// Scroll is smooth scrolling in logical pixels.
func (r *Receiver) Scroll(dx, dy float64) error {
	return r.emit(ifScroll, func(id uint64) *writer { return newMessage(id, 1).f32(float32(dx)).f32(float32(dy)) }, false)
}

// ScrollDiscrete is wheel scrolling in 120ths of a detent.
func (r *Receiver) ScrollDiscrete(dx, dy int32) error {
	return r.emit(ifScroll, func(id uint64) *writer { return newMessage(id, 2).i32(dx).i32(dy) }, false)
}

// ScrollStop ends smooth scrolling on the axes set.
func (r *Receiver) ScrollStop(x, y bool) error {
	return r.emit(ifScroll, func(id uint64) *writer { return newMessage(id, 3).u32(boolArg(x)).u32(boolArg(y)).u32(0) }, false)
}

// PointerFrame ends a group of pointer events.
func (r *Receiver) PointerFrame() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.emulating {
		return errors.New("eis: a pointer frame outside an emulation")
	}
	for _, d := range r.s.devices {
		if _, ok := d.ifaces[ifPointer]; ok {
			return r.live(r.frameDevice(d))
		}
	}
	return nil
}

// Key presses or releases the evdev key code, in a frame of its own.
func (r *Receiver) Key(code uint32, pressed bool) error {
	return r.emit(ifKeyboard, func(id uint64) *writer { return newMessage(id, 2).u32(code).u32(boolArg(pressed)) }, true)
}

// Modifiers is the keyboard's xkb modifier state and layout group.
func (r *Receiver) Modifiers(depressed, latched, locked, group uint32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.s.devices {
		if id, ok := d.ifaces[ifKeyboard]; ok {
			return r.live(r.s.send(newMessage(id, 3).u32(r.s.nextSerial()).u32(depressed).u32(locked).u32(latched).u32(group)))
		}
	}
	return nil
}

func boolArg(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// SetKeymap replaces the keyboard's keymap; a bound keyboard is removed
// and announced again with it, and started if an emulation runs.
func (r *Receiver) SetKeymap(text string) error {
	km, err := newKeymap(text)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return km.close()
	}
	old := r.s.keymap
	r.s.keymap = km
	err = old.close()
	i := slices.IndexFunc(r.s.devices, func(d device) bool { _, ok := d.ifaces[ifKeyboard]; return ok })
	if i < 0 {
		return err
	}
	d := r.s.devices[i]
	r.s.devices = slices.Delete(r.s.devices, i, i+1)
	ifaces := slices.Sorted(maps.Keys(d.ifaces))
	err = errors.Join(err, r.removeDevice(d, ifaces), r.s.addDevice(d.name, ifaces, false), r.startNew())
	return r.live(err)
}

func (r *Receiver) removeDevice(d device, ifaces []string) error {
	for _, iface := range ifaces {
		id := d.ifaces[iface]
		delete(r.s.objects, id)
		if err := r.s.send(newMessage(id, 0).u32(r.s.nextSerial())); err != nil {
			return err
		}
	}
	delete(r.s.objects, d.id)
	delete(r.started, d.id)
	return r.s.send(newMessage(d.id, 0).u32(r.s.nextSerial()))
}
