package wltest

import (
	"encoding/binary"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/fourcc"
)

// FormatModifier is one entry of a linux-dmabuf format table.
type FormatModifier struct {
	Format   fourcc.Format
	Modifier fourcc.Modifier
}

// FeedbackTranche is one tranche a DmabufFeedback sends: its target
// device, its tranche_flags and its formats in preference order.
type FeedbackTranche struct {
	Device  uint64
	Flags   uint32
	Formats []FormatModifier
}

// DmabufFeedback is a fake compositor for one client, at the far end of
// a LoopbackDisplay: wl_compositor and zwp_linux_dmabuf_v1 at version 4,
// whose default and surface feedback objects it sends a feedback when
// they are made and again at each SetFeedback, as a compositor does when
// a surface gains or loses a scanout plane.
type DmabufFeedback struct {
	t    *testing.T
	conn *net.UnixConn

	mu       sync.Mutex
	ifaces   map[uint32]string
	device   uint64
	tranches []FeedbackTranche
	sent     int
}

const (
	feedbackGlobalCompositor = iota + 1
	feedbackGlobalDmabuf
)

// NewDmabufFeedback serves one client on a loopback display the process's
// WAYLAND_DISPLAY then names, sending feedback of main device and
// tranches until SetFeedback changes them.
func NewDmabufFeedback(t *testing.T, device uint64, tranches ...FeedbackTranche) *DmabufFeedback {
	t.Helper()
	ln, _ := LoopbackDisplay(t, "wayland-dmabuf-feedback")
	f := &DmabufFeedback{t: t, ifaces: map[uint32]string{1: "wl_display"}, device: device, tranches: tranches}
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = ln.Close()
		f.mu.Lock()
		if f.conn != nil {
			_ = f.conn.Close()
		}
		f.mu.Unlock()
		<-done
	})
	go func() {
		defer close(done)
		conn, err := ln.AcceptUnix()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = conn
		f.mu.Unlock()
		f.serve(conn)
	}()
	return f
}

// SetFeedback replaces the feedback and sends it whole to every
// feedback object the client holds.
func (f *DmabufFeedback) SetFeedback(device uint64, tranches ...FeedbackTranche) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.device, f.tranches = device, tranches
	for id, iface := range f.ifaces {
		if iface == "zwp_linux_dmabuf_feedback_v1" {
			f.feedback(id)
		}
	}
}

// Sent counts the feedbacks sent, each ending in a done.
func (f *DmabufFeedback) Sent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent
}

func (f *DmabufFeedback) serve(conn *net.UnixConn) {
	defer func() { _ = conn.Close() }()
	var buf []byte
	chunk := make([]byte, 4096)
	for {
		n, err := conn.Read(chunk)
		if err != nil || n == 0 {
			return
		}
		buf = append(buf, chunk[:n]...)
		for len(buf) >= 8 {
			size := int(binary.LittleEndian.Uint32(buf[4:]) >> 16)
			if size < 8 || len(buf) < size {
				break
			}
			f.mu.Lock()
			f.request(binary.LittleEndian.Uint32(buf), uint16(binary.LittleEndian.Uint32(buf[4:])), buf[8:size])
			f.mu.Unlock()
			buf = buf[size:]
		}
	}
}

func u32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func wireString(s string) []byte {
	n := len(s) + 1
	out := binary.LittleEndian.AppendUint32(nil, uint32(n)) //nolint:gosec // an interface name
	out = append(out, s...)
	return append(out, make([]byte, (n+3)&^3-len(s))...)
}

func wireArray(b []byte) []byte {
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(b))) //nolint:gosec // a few entries
	out = append(out, b...)
	return append(out, make([]byte, (4-len(b)%4)%4)...)
}

func (f *DmabufFeedback) send(id uint32, opcode uint16, fd int, args ...[]byte) {
	body := slices.Concat(args...)
	msg := binary.LittleEndian.AppendUint32(nil, id)
	msg = binary.LittleEndian.AppendUint32(msg, uint32(len(body)+8)<<16|uint32(opcode)) //nolint:gosec // a small event
	msg = append(msg, body...)
	var oob []byte
	if fd >= 0 {
		oob = unix.UnixRights(fd)
	}
	if _, _, err := f.conn.WriteMsgUnix(msg, oob, nil); err != nil && !errors.Is(err, unix.EPIPE) && !errors.Is(err, net.ErrClosed) {
		f.t.Errorf("wltest: send event %d of %d: %v", opcode, id, err)
	}
}

func (f *DmabufFeedback) gone(id uint32) {
	delete(f.ifaces, id)
	f.send(1, 1, -1, u32(id))
}

func (f *DmabufFeedback) request(id uint32, opcode uint16, body []byte) {
	word := func(i int) uint32 { return binary.LittleEndian.Uint32(body[4*i:]) }
	switch f.ifaces[id] {
	case "wl_display":
		switch opcode {
		case 0:
			f.send(word(0), 0, -1, u32(0))
			f.send(1, 1, -1, u32(word(0)))
		case 1:
			reg := word(0)
			f.ifaces[reg] = "wl_registry"
			f.send(reg, 0, -1, u32(feedbackGlobalCompositor), wireString("wl_compositor"), u32(4))
			f.send(reg, 0, -1, u32(feedbackGlobalDmabuf), wireString("zwp_linux_dmabuf_v1"), u32(4))
		}
	case "wl_registry":
		n := int(word(1))
		at := 8 + (n+3)&^3
		f.ifaces[binary.LittleEndian.Uint32(body[at+4:])] = string(body[8 : 8+n-1])
	case "wl_compositor":
		if opcode == 0 {
			f.ifaces[word(0)] = "wl_surface"
		}
	case "wl_surface", "zwp_linux_dmabuf_feedback_v1":
		if opcode == 0 {
			f.gone(id)
		}
	case "zwp_linux_dmabuf_v1":
		switch opcode {
		case 0:
			f.gone(id)
		case 2, 3:
			f.ifaces[word(0)] = "zwp_linux_dmabuf_feedback_v1"
			f.feedback(word(0))
		}
	}
}

func devT(dev uint64) []byte { return wireArray(binary.NativeEndian.AppendUint64(nil, dev)) }

// feedback sends the feedback whole to feedback object id: the format
// table, the main device, each tranche and the done.
func (f *DmabufFeedback) feedback(id uint32) {
	var table []FormatModifier
	for _, tr := range f.tranches {
		for _, e := range tr.Formats {
			if !slices.Contains(table, e) {
				table = append(table, e)
			}
		}
	}
	fd, err := unix.MemfdCreate("wltest-format-table", unix.MFD_CLOEXEC)
	if err != nil {
		f.t.Errorf("wltest: format table: %v", err)
		return
	}
	defer func() { _ = unix.Close(fd) }()
	var raw []byte
	for _, e := range table {
		raw = binary.LittleEndian.AppendUint32(raw, uint32(e.Format))
		raw = binary.LittleEndian.AppendUint32(raw, 0)
		raw = binary.LittleEndian.AppendUint64(raw, uint64(e.Modifier))
	}
	if _, err := unix.Write(fd, raw); err != nil {
		f.t.Errorf("wltest: format table: %v", err)
		return
	}
	f.send(id, 1, fd, u32(uint32(len(raw)))) //nolint:gosec // a few entries
	f.send(id, 2, -1, devT(f.device))
	for _, tr := range f.tranches {
		f.send(id, 4, -1, devT(tr.Device))
		var indices []byte
		for _, e := range tr.Formats {
			indices = binary.LittleEndian.AppendUint16(indices, uint16(slices.Index(table, e))) //nolint:gosec // a few entries
		}
		f.send(id, 5, -1, wireArray(indices))
		f.send(id, 6, -1, u32(tr.Flags))
		f.send(id, 3, -1)
	}
	f.send(id, 0, -1)
	f.sent++
}
