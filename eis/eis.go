// Package eis is an EIS server: the receiving end of libei, which
// remote-desktop clients prefer over the portal's Notify* D-Bus calls.
// A portal's ConnectToEIS hands the app one end of a socket pair; Serve
// speaks the ei protocol on the other, advertises a named seat with a
// pointer, a keyboard and, over the regions it is given, an absolute
// pointer and a touchscreen, and replays what the client emulates onto
// a Handler.
package eis

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
)

// Handler receives the emulated input.
type Handler interface {
	Motion(dx, dy float64)
	Button(code uint32, pressed bool)
	// Scroll is smooth scrolling in logical pixels.
	Scroll(dx, dy float64)
	// ScrollDiscrete is wheel scrolling in 120ths of a detent.
	ScrollDiscrete(dx, dy int32)
	// ScrollStop ends the smooth scrolling on the axes set (the
	// fingers lifted); a cancel ends it the same way.
	ScrollStop(x, y bool)
	Key(code uint32, pressed bool)
	Keysym(sym uint32, pressed bool)
	// MotionAbsolute places the pointer at a point of the compositor's
	// logical layout, inside one of the regions.
	MotionAbsolute(x, y float64)
	// TouchDown, TouchMotion, TouchUp and TouchCancel drive the touch
	// point id, placed in the layout as MotionAbsolute is. A touch that
	// went down outside every region never reaches the Handler.
	TouchDown(id uint32, x, y float64)
	TouchMotion(id uint32, x, y float64)
	TouchUp(id uint32)
	TouchCancel(id uint32)
}

// Region is an area of the compositor's logical layout the absolute
// devices reach: its offset and size in logical pixels, the scale of the
// output it shows, and the mapping id that pairs it with the screen-cast
// stream of the same area (empty for none).
type Region struct {
	X, Y, Width, Height uint32
	Scale               float32
	MappingID           string
}

func (r Region) contains(x, y float32) bool {
	return x >= float32(r.X) && y >= float32(r.Y) && x < float32(r.X)+float32(r.Width) && y < float32(r.Y)+float32(r.Height)
}

func checkRegions(regions []Region) error {
	for i, r := range regions {
		if r.Width == 0 || r.Height == 0 || r.Scale <= 0 {
			return fmt.Errorf("eis: region %d is %dx%d at scale %g: a region needs an area and a positive scale", i, r.Width, r.Height, r.Scale)
		}
	}
	return nil
}

// The interfaces served, at the versions this server implements; a
// client's interface_version negotiates each down to the lower.
const (
	ifHandshake  = "ei_handshake"
	ifConnection = "ei_connection"
	ifCallback   = "ei_callback"
	ifPingpong   = "ei_pingpong"
	ifSeat       = "ei_seat"
	ifDevice     = "ei_device"
	ifPointer    = "ei_pointer"
	ifAbsolute   = "ei_pointer_absolute"
	ifTouch      = "ei_touchscreen"
	ifScroll     = "ei_scroll"
	ifButton     = "ei_button"
	ifKeyboard   = "ei_keyboard"
	ifText       = "ei_text"
)

var supported = map[string]uint32{
	ifConnection: 1, ifCallback: 1, ifPingpong: 1, ifSeat: 1, ifDevice: 2,
	ifPointer: 1, ifAbsolute: 1, ifTouch: 2, ifScroll: 1, ifButton: 1, ifKeyboard: 1, ifText: 1,
}

// Seat capability bits: the server picks them (reis's DeviceCapability
// values, which libei's EI_DEVICE_CAP_* mirror).
const (
	capPointer  uint64 = 1 << 0
	capAbsolute uint64 = 1 << 1
	capKeyboard uint64 = 1 << 2
	capTouch    uint64 = 1 << 3
	capScroll   uint64 = 1 << 4
	capButton   uint64 = 1 << 5
	capText     uint64 = 1 << 6
)

// capInterface lists each capability with its interface and whether
// only a seat with regions offers it.
var capInterface = []struct {
	bit     uint64
	iface   string
	regions bool
}{
	{capPointer, ifPointer, false},
	{capAbsolute, ifAbsolute, true},
	{capKeyboard, ifKeyboard, false},
	{capTouch, ifTouch, true},
	{capScroll, ifScroll, false},
	{capButton, ifButton, false},
	{capText, ifText, false},
}

const deviceTypeVirtual = 1

// serverIDBase is where server-created object ids start; client ids
// count up from 1.
const serverIDBase = 0xff00_0000_0000_0000

// Pair makes a socket pair, serves h on one end as seat over regions
// and returns the other for the client.
func Pair(h Handler, seat string, regions []Region) (*os.File, error) {
	if err := checkSeat(seat); err != nil {
		return nil, err
	}
	if err := checkRegions(regions); err != nil {
		return nil, err
	}
	conn, client, err := socketPair()
	if err != nil {
		return nil, err
	}
	go func() {
		defer func() { _ = conn.Close() }()
		_ = Serve(conn, h, seat, regions)
	}()
	return client, nil
}

// server is one connection's state.
type server struct {
	conn *net.UnixConn
	h    Handler
	// seatName names the seat, and its devices after it.
	seatName     string
	regions      []Region
	objects      map[uint64]string
	negotiated   map[string]uint32
	nextID       uint64
	serial       uint32
	connection   uint64
	seat         uint64
	bound        bool
	want, client contextType
	keymap       *keymap
	devices      []device
	// touches are the touch points down inside a region.
	touches map[uint32]struct{}
}

// Serve runs the protocol on conn until the client disconnects,
// advertising a seat called seat whose devices are seat-keyboard and
// seat-pointer, and with regions also seat-absolute and
// seat-touchscreen, which reach those regions. A clean disconnect is
// nil.
func Serve(conn *net.UnixConn, h Handler, seat string, regions []Region) error {
	if err := checkSeat(seat); err != nil {
		return err
	}
	if err := checkRegions(regions); err != nil {
		return err
	}
	s := &server{
		conn: conn, h: h, seatName: seat, regions: regions, objects: map[uint64]string{0: ifHandshake},
		negotiated: map[string]uint32{}, nextID: serverIDBase, touches: map[uint32]struct{}{}, want: contextSender, client: contextReceiver,
	}
	return s.run(s.dispatch)
}

func (s *server) run(dispatch func(message) (bool, error)) error {
	// The server opens with its handshake version.
	if err := s.send(newMessage(0, 0).u32(1)); err != nil {
		return err
	}
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	oob := make([]byte, syscall.CmsgSpace(4*16))
	for {
		n, oobn, _, _, err := s.conn.ReadMsgUnix(chunk, oob)
		closeFds(oob[:oobn]) // nothing this server receives carries one
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if n == 0 {
			return nil
		}
		buf = append(buf, chunk[:n]...)
		msgs, rest, err := split(buf)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			done, err := dispatch(m)
			if err != nil || done {
				return err
			}
		}
		buf = append(buf[:0], rest...)
	}
}

// checkSeat refuses an empty seat name: the client shows it, and the
// devices are named after it.
func checkSeat(seat string) error {
	if seat == "" {
		return errors.New("eis: the seat name is empty: the client lists the seat and its devices by it")
	}
	return nil
}

func closeFds(oob []byte) {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}
	for _, m := range msgs {
		if fds, err := syscall.ParseUnixRights(&m); err == nil {
			for _, fd := range fds {
				_ = syscall.Close(fd)
			}
		}
	}
}

func (s *server) send(w *writer) error {
	b := w.bytes()
	if w.err != nil {
		return w.err
	}
	if len(w.fds) > 0 {
		_, _, err := s.conn.WriteMsgUnix(b, syscall.UnixRights(w.fds...), nil)
		return err
	}
	_, err := s.conn.Write(b)
	return err
}

func (s *server) newObject(iface string) uint64 {
	id := s.nextID
	s.nextID++
	s.objects[id] = iface
	return id
}

func (s *server) nextSerial() uint32 {
	s.serial++
	return s.serial
}

// dispatch handles one request; done is a client disconnect.
func (s *server) dispatch(m message) (done bool, err error) {
	r := &reader{b: m.args}
	switch s.objects[m.object] {
	case ifHandshake:
		err = s.handshake(m.opcode, r)
	case ifConnection:
		switch m.opcode {
		case 0: // sync(callback new_id, version)
			id := r.u64()
			_ = r.u32()
			if r.err == nil {
				err = s.send(newMessage(id, 0).u64(0)) // ei_callback.done
			}
		case 1: // disconnect
			return true, nil
		}
	case ifSeat:
		if m.opcode == 1 { // bind(capabilities)
			caps := r.u64()
			if r.err == nil {
				err = s.bind(caps)
			}
		}
	case ifPointer:
		if m.opcode == 1 {
			x, y := r.f32(), r.f32()
			if r.err == nil {
				s.h.Motion(float64(x), float64(y))
			}
		}
	case ifAbsolute:
		if m.opcode == 1 {
			x, y := r.f32(), r.f32()
			if r.err == nil && s.inRegion(x, y) {
				s.h.MotionAbsolute(float64(x), float64(y))
			}
		}
	case ifTouch:
		s.touch(m.opcode, r)
	case ifScroll:
		switch m.opcode {
		case 1:
			x, y := r.f32(), r.f32()
			if r.err == nil {
				s.h.Scroll(float64(x), float64(y))
			}
		case 2:
			x, y := r.i32(), r.i32()
			if r.err == nil {
				s.h.ScrollDiscrete(x, y)
			}
		case 3: // scroll_stop(x, y, is_cancel)
			x, y, _ := r.u32(), r.u32(), r.u32()
			if r.err == nil {
				s.h.ScrollStop(x != 0, y != 0)
			}
		}
	case ifButton:
		if m.opcode == 1 {
			code, state := r.u32(), r.u32()
			if r.err == nil {
				s.h.Button(code, state != 0)
			}
		}
	case ifKeyboard:
		if m.opcode == 1 {
			code, state := r.u32(), r.u32()
			if r.err == nil {
				s.h.Key(code, state != 0)
			}
		}
	case ifText:
		if m.opcode == 1 {
			sym, state := r.u32(), r.u32()
			if r.err == nil {
				s.h.Keysym(sym, state != 0)
			}
		}
	}
	// Everything else (device emulation bracketing, frames, pongs,
	// releases, unknown objects) needs no answer here.
	if err == nil {
		err = r.err
	}
	return false, err
}

// touch replays a touchscreen request: a touch going down outside
// every region is dropped with everything after it under its id.
func (s *server) touch(opcode uint32, r *reader) {
	id := r.u32()
	if opcode == 1 || opcode == 2 {
		x, y := r.f32(), r.f32()
		if r.err != nil {
			return
		}
		_, down := s.touches[id]
		switch {
		case opcode == 1 && s.inRegion(x, y):
			s.touches[id] = struct{}{}
			s.h.TouchDown(id, float64(x), float64(y))
		case opcode == 2 && down && s.inRegion(x, y):
			s.h.TouchMotion(id, float64(x), float64(y))
		}
		return
	}
	if _, down := s.touches[id]; r.err != nil || !down {
		return
	}
	delete(s.touches, id)
	switch opcode {
	case 3:
		s.h.TouchUp(id)
	case 4:
		s.h.TouchCancel(id)
	}
}

func (s *server) inRegion(x, y float32) bool {
	for _, r := range s.regions {
		if r.contains(x, y) {
			return true
		}
	}
	return false
}

// handshake runs the setup the ei_handshake object (id 0) carries.
func (s *server) handshake(opcode uint32, r *reader) error {
	switch opcode {
	case 4: // interface_version(name, version)
		name, version := r.str(), r.u32()
		if ours, ok := supported[name]; ok && r.err == nil {
			s.negotiated[name] = min(version, ours)
		}
	case 2:
		s.client = contextType(r.u32())
	case 1: // finish
		if s.client != s.want {
			return fmt.Errorf("eis: a %s client connected where a %s was served", s.client, s.want)
		}
		for _, name := range []string{ifConnection, ifCallback, ifPingpong} {
			if _, ok := s.negotiated[name]; !ok {
				return fmt.Errorf("eis: the client did not negotiate %s", name)
			}
		}
		for name, v := range s.negotiated {
			if err := s.send(newMessage(0, 1).str(name).u32(v)); err != nil {
				return err
			}
		}
		s.connection = s.newObject(ifConnection)
		if err := s.send(newMessage(0, 2).u32(s.nextSerial()).u64(s.connection).u32(s.negotiated[ifConnection])); err != nil {
			return err
		}
		delete(s.objects, 0)
		return s.addSeat()
	}
	// handshake_version and name need no answer.
	return nil
}

// addSeat advertises the one seat, with each capability whose
// interface the client negotiated.
func (s *server) addSeat() error {
	v, ok := s.negotiated[ifSeat]
	if !ok {
		return nil
	}
	s.seat = s.newObject(ifSeat)
	if err := s.send(newMessage(s.connection, 1).u64(s.seat).u32(v)); err != nil {
		return err
	}
	if err := s.send(newMessage(s.seat, 1).str(s.seatName)); err != nil {
		return err
	}
	for _, c := range capInterface {
		if c.regions && len(s.regions) == 0 {
			continue
		}
		if _, ok := s.negotiated[c.iface]; ok {
			if err := s.send(newMessage(s.seat, 2).u64(c.bit).str(c.iface)); err != nil {
				return err
			}
		}
	}
	return s.send(newMessage(s.seat, 3))
}

// bind adds the devices for the bound capabilities, once: a keyboard
// (with text for keysyms), a pointer with buttons and scrolling, an
// absolute pointer with the same and a touchscreen, the last two over
// the seat's regions. Buttons or scrolling bound with an absolute
// pointer and no relative one come on the absolute pointer alone. Each
// device is resumed straight away, since a paused device cannot emulate.
func (s *server) bind(caps uint64) error {
	if s.bound {
		return nil
	}
	s.bound = true
	if caps&(capKeyboard|capText) != 0 {
		var ifaces []string
		if caps&capKeyboard != 0 {
			ifaces = append(ifaces, ifKeyboard)
		}
		if caps&capText != 0 {
			ifaces = append(ifaces, ifText)
		}
		if err := s.addDevice(s.seatName+"-keyboard", ifaces, false); err != nil {
			return err
		}
	}
	absolute := caps&capAbsolute != 0 && len(s.regions) > 0
	if caps&capPointer != 0 || !absolute && caps&(capButton|capScroll) != 0 {
		if err := s.addDevice(s.seatName+"-pointer", []string{ifPointer, ifButton, ifScroll}, false); err != nil {
			return err
		}
	}
	if absolute {
		if err := s.addDevice(s.seatName+"-absolute", []string{ifAbsolute, ifButton, ifScroll}, true); err != nil {
			return err
		}
	}
	if caps&capTouch != 0 && len(s.regions) > 0 {
		return s.addDevice(s.seatName+"-touchscreen", []string{ifTouch}, true)
	}
	return nil
}

// addDevice announces a device with the negotiated ones of ifaces and,
// for an absolute device, the seat's regions, each after its mapping id
// when the client's ei_device has region_mapping_id (version 2).
func (s *server) addDevice(name string, ifaces []string, absolute bool) error {
	v, ok := s.negotiated[ifDevice]
	if !ok {
		return nil
	}
	dev := device{id: s.newObject(ifDevice), name: name, ifaces: map[string]uint64{}}
	msgs := []*writer{
		newMessage(s.seat, 4).u64(dev.id).u32(v),
		newMessage(dev.id, 1).str(name),
		newMessage(dev.id, 2).u32(deviceTypeVirtual),
	}
	for _, r := range s.regions {
		if !absolute {
			break
		}
		if r.MappingID != "" && v >= 2 {
			msgs = append(msgs, newMessage(dev.id, 12).str(r.MappingID))
		}
		msgs = append(msgs, newMessage(dev.id, 4).u32(r.X).u32(r.Y).u32(r.Width).u32(r.Height).f32(r.Scale))
	}
	for _, iface := range ifaces {
		if iv, ok := s.negotiated[iface]; ok {
			id := s.newObject(iface)
			dev.ifaces[iface] = id
			msgs = append(msgs, newMessage(dev.id, 5).u64(id).str(iface).u32(iv))
			if iface == ifKeyboard && s.keymap != nil {
				msgs = append(msgs, s.keymap.message(id))
			}
		}
	}
	msgs = append(msgs, newMessage(dev.id, 6), newMessage(dev.id, 7).u32(s.nextSerial()))
	s.devices = append(s.devices, dev)
	for _, m := range msgs {
		if err := s.send(m); err != nil {
			return err
		}
	}
	return nil
}
