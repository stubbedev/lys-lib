package capture

import (
	"encoding/binary"
	"errors"
	"image"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/wltest"
)

// fakeCompositor is the far end of a capture client's connection: only as
// much of the Wayland wire as the capture calls under test speak. A real
// compositor offers linux-dmabuf only rendering on a DRM device, which CI
// runners do not have, so the dmabuf paths' client side is tested here.
type fakeCompositor struct {
	t    *testing.T
	conn *net.UnixConn
	// ifaces names each live object by its id.
	ifaces map[uint32]string
	next   uint32

	mu sync.Mutex
	// frames counts the session frames captured; only the first lands, as
	// a still window copies only a session's first frame, unless live.
	frames int
	// live is how many of a session's first frames land, each with
	// fakeDamage, when not zero.
	live int
	// buffers names each frame's attached buffer, and captured are the
	// frames captured, each with the buffer and the damage_buffer it
	// carried.
	buffers  map[uint32]*fakeCapture
	captured []fakeCapture
	// regions are the regions the screencopy frames asked for.
	regions []image.Rectangle
	copies  int
	// session is the capture session opened last.
	session uint32
	// oneshot are the frames and sessions declared one-shot, frameSession
	// each session frame's session, and declared whether each copy's frame
	// or session was declared one-shot when it copied.
	oneshot      map[uint32]bool
	frameSession map[uint32]uint32
	declared     []bool
}

const (
	fakeGlobalDmabuf = iota + 1
	fakeGlobalCopyCapture
	fakeGlobalToplevelSource
	fakeGlobalToplevelList
	fakeGlobalShm
	fakeGlobalScreencopy
	fakeGlobalOutput
	fakeGlobalOutputSource
	fakeGlobalCaptureHint
)

// fakeCapture is one session frame as the client asked for it.
type fakeCapture struct {
	buffer uint32
	damage []image.Rectangle
}

// fakeSessionSize is the buffer size a fake capture session takes.
var fakeSessionSize = image.Pt(320, 200)

// fakeDamage is what the fake reports changed in live frame n: all of it
// for a session's first frame, as the protocol has it.
func fakeDamage(n int) image.Rectangle {
	if n == 0 {
		return image.Rectangle{Max: fakeSessionSize}
	}
	n %= fakeDamageCycle
	return image.Rect(n*10, n*5, n*10+10, n*5+5)
}

// fakeDamageCycle is how many live frames' damage differs before it
// repeats, each inside fakeSessionSize.
const fakeDamageCycle = 16

const fakeDevice = uint64(226<<8 | 128)

var (
	fakeModifier = fourcc.Modifier(0)
	fakeFormat   = fourcc.XRGB8888
)

// startFakeCompositor serves one client on a display the process's
// WAYLAND_DISPLAY then names.
func startFakeCompositor(t *testing.T) *fakeCompositor {
	t.Helper()
	ln, _ := wltest.LoopbackDisplay(t, "wayland-capture-fake")
	f := newFakeCompositor(t)
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	go func() {
		defer close(done)
		f.accept(ln)
	}()
	return f
}

func newFakeCompositor(t *testing.T) *fakeCompositor {
	return &fakeCompositor{t: t, ifaces: map[uint32]string{1: "wl_display"}, next: 0xff000000, buffers: map[uint32]*fakeCapture{}, oneshot: map[uint32]bool{}, frameSession: map[uint32]uint32{}}
}

// accept serves the first client ln takes until it hangs up.
func (f *fakeCompositor) accept(ln *net.UnixListener) {
	conn, err := ln.AcceptUnix()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	f.conn = conn
	f.serve()
}

func (f *fakeCompositor) serve() {
	var buf []byte
	chunk := make([]byte, 4096)
	oob := make([]byte, unix.CmsgSpace(16*4))
	for {
		n, oobn, _, _, err := f.conn.ReadMsgUnix(chunk, oob)
		if err != nil || n == 0 {
			return
		}
		closeFds(oob[:oobn])
		buf = append(buf, chunk[:n]...)
		for len(buf) >= 8 {
			size := int(binary.LittleEndian.Uint32(buf[4:]) >> 16)
			if size < 8 || len(buf) < size {
				break
			}
			f.request(binary.LittleEndian.Uint32(buf), uint16(binary.LittleEndian.Uint32(buf[4:])), buf[8:size])
			buf = buf[size:]
		}
	}
}

// closeFds closes the descriptors a request carried.
func closeFds(oob []byte) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}
	for i := range msgs {
		fds, err := unix.ParseUnixRights(&msgs[i])
		if err != nil {
			continue
		}
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}
}

func wireU32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func wireString(s string) []byte {
	n := len(s) + 1
	out := binary.LittleEndian.AppendUint32(nil, uint32(n))
	out = append(out, s...)
	return append(out, make([]byte, (n+3)&^3-len(s))...)
}

func wireArray(b []byte) []byte {
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(b)))
	out = append(out, b...)
	return append(out, make([]byte, (4-len(b)%4)%4)...)
}

// send writes one event, its argument words joined.
func (f *fakeCompositor) send(id uint32, opcode uint16, args ...[]byte) {
	f.sendFd(id, opcode, -1, args...)
}

// sendFd is send with a descriptor alongside.
func (f *fakeCompositor) sendFd(id uint32, opcode uint16, fd int, args ...[]byte) {
	var body []byte
	for _, a := range args {
		body = append(body, a...)
	}
	msg := binary.LittleEndian.AppendUint32(nil, id)
	msg = binary.LittleEndian.AppendUint32(msg, uint32(len(body)+8)<<16|uint32(opcode))
	msg = append(msg, body...)
	var oob []byte
	if fd >= 0 {
		oob = unix.UnixRights(fd)
	}
	if _, _, err := f.conn.WriteMsgUnix(msg, oob, nil); err != nil && !errors.Is(err, unix.EPIPE) {
		f.t.Errorf("fake compositor: send event %d of %d: %v", opcode, id, err)
	}
}

// gone answers a destructor request.
func (f *fakeCompositor) gone(id uint32) {
	delete(f.ifaces, id)
	f.send(1, 1, wireU32(id))
}

func u32At(b []byte, word int) uint32 { return binary.LittleEndian.Uint32(b[word*4:]) }

// request answers one request. Opcodes and events are the protocols' order.
func (f *fakeCompositor) request(id uint32, opcode uint16, body []byte) {
	switch iface := f.ifaces[id]; iface {
	case "wl_display":
		switch opcode {
		case 0: // sync
			f.send(u32At(body, 0), 0, wireU32(0))
			f.send(1, 1, wireU32(u32At(body, 0)))
		case 1: // get_registry
			reg := u32At(body, 0)
			f.ifaces[reg] = "wl_registry"
			for _, g := range []struct {
				name    uint32
				iface   string
				version uint32
			}{
				{fakeGlobalDmabuf, ifaceDmabuf, 4},
				{fakeGlobalCopyCapture, ifaceCopyCapture, 1},
				{fakeGlobalToplevelSource, ifaceToplevelSrc, 1},
				{fakeGlobalToplevelList, ifaceToplevelList, 1},
				{fakeGlobalShm, ifaceShm, 1},
				{fakeGlobalScreencopy, ifaceScreencopy, 3},
				{fakeGlobalOutput, ifaceOutput, 4},
				{fakeGlobalOutputSource, ifaceOutputSource, 1},
				{fakeGlobalCaptureHint, ifaceCaptureHint, 1},
			} {
				f.send(reg, 0, wireU32(g.name), wireString(g.iface), wireU32(g.version))
			}
		}
	case "wl_registry": // bind: name, interface, version, new id
		n := binary.LittleEndian.Uint32(body[4:])
		at := 8 + int(n+3)&^3
		name, newID := string(body[8:8+n-1]), binary.LittleEndian.Uint32(body[at+4:])
		f.ifaces[newID] = name
		switch name {
		case ifaceToplevelList:
			f.announceToplevel(newID)
		case ifaceOutput:
			f.send(newID, 1, wireU32(1), wireU32(1280), wireU32(800), wireU32(60000))
			f.send(newID, 4, wireString("FAKE-1"))
			f.send(newID, 2)
		}
	case ifaceToplevelSrc, ifaceOutputSource:
		if opcode == 0 {
			f.ifaces[u32At(body, 0)] = "source"
		}
	case ifaceShm:
		if opcode == 0 {
			f.ifaces[u32At(body, 0)] = "pool"
		}
	case "pool":
		switch opcode {
		case 0:
			f.ifaces[u32At(body, 0)] = "wl_buffer"
		case 1:
			f.gone(id)
		}
	case ifaceCopyCapture:
		if opcode == 0 {
			session := u32At(body, 0)
			f.ifaces[session] = "session"
			f.mu.Lock()
			f.session = session
			f.mu.Unlock()
			f.send(session, 0, wireU32(uint32(fakeSessionSize.X)), wireU32(uint32(fakeSessionSize.Y)))
			f.send(session, 1, wireU32(uint32(FormatXRGB8888)))
			f.send(session, 2, wireArray(binary.LittleEndian.AppendUint64(nil, fakeDevice)))
			f.send(session, 3, wireU32(uint32(fakeFormat)), wireArray(binary.LittleEndian.AppendUint64(nil, uint64(fakeModifier))))
			f.send(session, 4)
		}
	case ifaceCaptureHint:
		if opcode == 1 || opcode == 2 {
			f.mu.Lock()
			f.oneshot[u32At(body, 0)] = true
			f.mu.Unlock()
		}
	case "session":
		switch opcode {
		case 0:
			f.ifaces[u32At(body, 0)] = "frame"
			f.mu.Lock()
			f.frameSession[u32At(body, 0)] = id
			f.mu.Unlock()
		case 1:
			f.gone(id)
		}
	case "frame":
		switch opcode {
		case 0:
			f.mu.Lock()
			delete(f.buffers, id)
			f.mu.Unlock()
			f.gone(id)
		case 1:
			f.mu.Lock()
			f.buffers[id] = &fakeCapture{buffer: u32At(body, 0)}
			f.mu.Unlock()
		case 2:
			x, y, w, h := int(int32(u32At(body, 0))), int(int32(u32At(body, 1))), int(int32(u32At(body, 2))), int(int32(u32At(body, 3)))
			f.mu.Lock()
			if c := f.buffers[id]; c != nil {
				c.damage = append(c.damage, image.Rect(x, y, x+w, y+h))
			}
			f.mu.Unlock()
		case 3: // capture
			f.mu.Lock()
			n := f.frames
			f.frames++
			f.declared = append(f.declared, f.oneshot[f.frameSession[id]])
			if c := f.buffers[id]; c != nil {
				f.captured = append(f.captured, *c)
			}
			live := f.live
			f.mu.Unlock()
			if n < live {
				d := fakeDamage(n)
				f.send(id, 1, wireU32(uint32(d.Min.X)), wireU32(uint32(d.Min.Y)), wireU32(uint32(d.Dx())), wireU32(uint32(d.Dy())))
			}
			if n == 0 || n < live {
				f.send(id, 3) // ready
			}
		}
	case "source", "wl_buffer", "params":
		if opcode == 0 {
			f.gone(id)
			return
		}
		if iface == "params" && opcode == 2 { // create
			f.next++
			f.ifaces[f.next] = "wl_buffer"
			f.send(id, 0, wireU32(f.next))
		}
	case ifaceDmabuf:
		switch opcode {
		case 1:
			f.ifaces[u32At(body, 0)] = "params"
		case 2:
			f.feedback(u32At(body, 0))
		}
	case "feedback":
		if opcode == 0 {
			f.gone(id)
		}
	case ifaceScreencopy:
		f.screencopyRequest(opcode, body)
	case "screencopy":
		f.screencopyFrame(id, opcode)
	}
}

func (f *fakeCompositor) announceToplevel(list uint32) {
	f.next++
	h := f.next
	f.ifaces[h] = "handle"
	f.send(list, 0, wireU32(h))
	f.send(h, 2, wireString("still"))
	f.send(h, 3, wireString("app"))
	f.send(h, 4, wireString("id-1"))
	f.send(h, 1)
}

// feedback sends a default feedback of fakeFormat at fakeModifier.
func (f *fakeCompositor) feedback(id uint32) {
	f.ifaces[id] = "feedback"
	fd, err := unix.MemfdCreate("fake-table", unix.MFD_CLOEXEC)
	if err != nil {
		f.t.Errorf("fake compositor: format table: %v", err)
		return
	}
	defer func() { _ = unix.Close(fd) }()
	entry := binary.LittleEndian.AppendUint32(nil, uint32(fakeFormat))
	entry = binary.LittleEndian.AppendUint32(entry, 0)
	entry = binary.LittleEndian.AppendUint64(entry, uint64(fakeModifier))
	if _, err := unix.Write(fd, entry); err != nil {
		f.t.Errorf("fake compositor: format table: %v", err)
		return
	}
	f.sendFd(id, 1, fd, wireU32(uint32(len(entry))))
	f.send(id, 2, wireArray(binary.LittleEndian.AppendUint64(nil, fakeDevice)))
	f.send(id, 5, wireArray([]byte{0, 0}))
	f.send(id, 3)
	f.send(id, 0)
}

func (f *fakeCompositor) screencopyRequest(opcode uint16, body []byte) {
	frame := u32At(body, 0)
	f.ifaces[frame] = "screencopy"
	var size image.Point
	f.mu.Lock()
	switch opcode {
	case 0: // capture_output
		size = image.Pt(1280, 800)
		f.regions = append(f.regions, image.Rectangle{})
	case 1: // capture_output_region
		x, y, w, h := int(int32(u32At(body, 3))), int(int32(u32At(body, 4))), int(int32(u32At(body, 5))), int(int32(u32At(body, 6)))
		size = image.Pt(w, h)
		f.regions = append(f.regions, image.Rect(x, y, x+w, y+h))
	}
	f.mu.Unlock()
	f.send(frame, 0, wireU32(uint32(FormatXRGB8888)), wireU32(uint32(size.X)), wireU32(uint32(size.Y)), wireU32(uint32(size.X*4)))
	f.send(frame, 5, wireU32(uint32(fakeFormat)), wireU32(uint32(size.X)), wireU32(uint32(size.Y)))
	f.send(frame, 6)
}

func (f *fakeCompositor) screencopyFrame(id uint32, opcode uint16) {
	switch opcode {
	case 0, 2: // copy, copy_with_damage
		f.mu.Lock()
		f.copies++
		f.declared = append(f.declared, f.oneshot[id])
		f.mu.Unlock()
		f.send(id, 1, wireU32(0))
		f.send(id, 2, wireU32(0), wireU32(0), wireU32(0))
	case 1:
		f.gone(id)
	}
}

// fakeDmabuf is a one-plane description over a real descriptor, which the
// fake takes and closes.
func fakeDmabuf(t *testing.T, width, height int) dmabuf.Buffer {
	t.Helper()
	fd, err := unix.MemfdCreate("fake-dmabuf", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return dmabuf.Buffer{Width: width, Height: height, Format: fakeFormat, Modifier: fakeModifier, Planes: []dmabuf.Plane{{FD: fd, Stride: uint32(width * 4)}}}
}

// A Capture waiting on a still window holds up nothing else on its Client:
// a stream drops and allocates buffers around that wait, and a still window
// may never end it. The fake answers a session's first frame only, as a still
// window does.
func TestADmabufSessionWaitLeavesTheClientFree(t *testing.T) {
	startFakeCompositor(t)
	c, err := Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	tops, err := c.Toplevels()
	if err != nil || len(tops) != 1 {
		t.Fatalf("toplevels %v, %v", tops, err)
	}
	s, err := c.OpenDmabufSession(tops[0], false)
	if err != nil {
		t.Fatal(err)
	}
	formats, err := s.Formats()
	if err != nil {
		t.Fatal(err)
	}
	if len(formats) != 1 || formats[0].Width != 320 || formats[0].Height != 200 {
		t.Fatalf("formats %+v", formats)
	}
	buf, err := c.ImportDmabuf(fakeDmabuf(t, 320, 200))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Capture(buf); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	dropped, err := c.ImportDmabuf(fakeDmabuf(t, 320, 200))
	if err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() {
		_, err := s.Capture(buf)
		waited <- err
	}()
	select {
	case err := <-waited:
		t.Fatalf("a Capture of the still window returned %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	worked := make(chan error, 1)
	go func() {
		dropped.Destroy()
		imported, err := c.ImportDmabuf(fakeDmabuf(t, 320, 200))
		if err != nil {
			worked <- err
			return
		}
		imported.Destroy()
		_, err = s.Formats()
		worked <- err
	}()
	select {
	case err := <-worked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		_ = s.Close()
		t.Fatal("the Client's calls waited on the Capture")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waited:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Capture during Close = %v, want ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not wake the Capture")
	}
}

// A buffer the source's resize left behind does not hide the source going
// away: a caller told of the resize and trying again learns the session ended,
// though no frame reads the connection in between.
func TestAResizedBufferStillLearnsTheSessionStopped(t *testing.T) {
	f := startFakeCompositor(t)
	c, err := Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	tops, err := c.Toplevels()
	if err != nil || len(tops) != 1 {
		t.Fatalf("toplevels %v, %v", tops, err)
	}
	s, err := c.OpenDmabufSession(tops[0], false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	buf, err := c.ImportDmabuf(fakeDmabuf(t, 320, 200))
	if err != nil {
		t.Fatal(err)
	}
	f.send(f.sessionID(), 0, wireU32(400), wireU32(300))
	f.send(f.sessionID(), 4)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if err := c.Refresh(); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Capture(buf); errors.Is(err, ErrReconfigured) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the resize was never reported")
		}
	}
	f.send(f.sessionID(), 5)
	for range 2 {
		if _, err = s.Capture(buf); errors.Is(err, ErrSessionStopped) {
			return
		}
	}
	t.Fatalf("Capture after the source went away = %v, want ErrSessionStopped", err)
}

// sessionID is the open capture session's object id.
func (f *fakeCompositor) sessionID() uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.session
}

// A region's dmabuf layout is asked of the compositor for that region, and
// its frames are copied from that region, separately from the whole output's.
func TestARegionsDmabufFramesAreItsOwn(t *testing.T) {
	f := startFakeCompositor(t)
	c, err := Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	o, ok := c.OutputByName("FAKE-1")
	if !ok {
		t.Fatalf("outputs %+v", c.Outputs())
	}
	region := image.Rect(10, 20, 110, 70)
	got, err := c.DmabufRegionFormat(o, region, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 100 || got.Height != 50 || got.Fourcc != fakeFormat || got.Device != fakeDevice || len(got.Modifiers) != 1 {
		t.Fatalf("region format %+v", got)
	}
	whole, err := c.DmabufFormat(o, false)
	if err != nil {
		t.Fatal(err)
	}
	if whole.Width != 1280 || whole.Height != 800 {
		t.Fatalf("the whole output's format %+v after the region's", whole)
	}
	if again, err := c.DmabufRegionFormat(o, region, false); err != nil || again.Width != 100 {
		t.Fatalf("the region's format again = %+v, %v", again, err)
	}
	buf, err := c.ImportDmabuf(fakeDmabuf(t, 100, 50))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CopyOutputRegionDmabuf(o, region, false, buf); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if want := []image.Rectangle{region, {}, region}; len(f.regions) != len(want) || f.regions[0] != want[0] || f.regions[1] != want[1] || f.regions[2] != want[2] {
		t.Errorf("screencopy frames of %v, want %v: one probe each, then the copy", f.regions, want)
	}
	if f.copies != 1 {
		t.Errorf("%d copies, want 1", f.copies)
	}
	if _, err := c.DmabufRegionFormat(o, image.Rectangle{}, false); err == nil {
		t.Error("an empty region has a format")
	}
	if err := c.CopyOutputRegionDmabuf(o, image.Rectangle{}, false, buf); err == nil {
		t.Error("an empty region copied")
	}
}

// A stream's reused buffer asks the compositor only for what changed since
// it was last filled; a buffer's first frame asks for all of it.
func TestAStreamAsksOnlyForWhatChangedSinceABufferWasFilled(t *testing.T) {
	f := startFakeCompositor(t)
	const live = 7
	f.mu.Lock()
	f.live = live
	f.mu.Unlock()
	s, err := OpenOutputStream("FAKE-1", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	var captured []fakeCapture
	for deadline := time.Now().Add(5 * time.Second); len(captured) <= live; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d frames captured, want %d", len(captured), live+1)
		}
		f.mu.Lock()
		captured = slices.Clone(f.captured)
		f.mu.Unlock()
	}
	var got StreamFrame
	if !s.Latest(&got) || got.Seq != live-1 || !slices.Equal(got.Damage, []Rect{{X: 60, Y: 30, Width: 10, Height: 5}}) {
		t.Errorf("the latest frame is %+v", got)
	}
	whole := []image.Rectangle{{Max: fakeSessionSize}}
	for n, c := range captured {
		want := whole
		if n >= streamRing {
			want = []image.Rectangle{fakeDamage(n - 2), fakeDamage(n - 1)}
			if c.buffer != captured[n-streamRing].buffer {
				t.Errorf("frame %d went into buffer %d, not the ring's %d", n, c.buffer, captured[n-streamRing].buffer)
			}
		}
		if !slices.Equal(c.damage, want) {
			t.Errorf("frame %d damaged %v, want %v", n, c.damage, want)
		}
	}
}

// A screenshot declares its screencopy frame or capture session one-shot
// before it copies, and a screencast declares nothing, so a compositor
// keeping surfaces out of screen sharing masks them only in the cast.
func TestOnlyAScreenshotIsDeclaredOneShot(t *testing.T) {
	f := startFakeCompositor(t)
	f.mu.Lock()
	f.live = 2
	f.mu.Unlock()
	c, err := Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	o, ok := c.OutputByName("FAKE-1")
	if !ok {
		t.Fatalf("outputs %+v", c.Outputs())
	}
	tls, err := c.Toplevels()
	if err != nil || len(tls) != 1 {
		t.Fatalf("toplevels %+v, %v", tls, err)
	}
	for _, capture := range []func(Options) (*Frame, error){
		func(opts Options) (*Frame, error) { return c.CaptureOutput(o, opts) },
		func(opts Options) (*Frame, error) { return c.CaptureOutputRegion(o, image.Rect(0, 0, 10, 10), opts) },
		func(opts Options) (*Frame, error) { return c.CaptureToplevel(tls[0], opts) },
	} {
		for _, intent := range []Intent{Screenshot, Screencast} {
			if _, err := capture(Options{Intent: intent}); err != nil {
				t.Fatalf("intent %d: %v", intent, err)
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if want := []bool{true, false, true, false, true, false}; !slices.Equal(f.declared, want) {
		t.Errorf("copies declared one-shot %v, want %v", f.declared, want)
	}
}
