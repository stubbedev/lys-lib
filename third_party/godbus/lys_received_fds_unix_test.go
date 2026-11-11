//go:build linux

package dbus

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"reflect"
	"slices"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// transportPair is a unix transport and the other end of its socket.
func transportPair(t *testing.T) (*unixTransport, *net.UnixConn) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var conns [2]*net.UnixConn
	for i, fd := range pair {
		f := os.NewFile(uintptr(fd), "pair")
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		conns[i] = c.(*net.UnixConn)
		t.Cleanup(func() { _ = conns[i].Close() })
	}
	return newUnixTransportFromConn(conns[0]).(*unixTransport), conns[1]
}

// frame is a signal with the given body on the wire, declaring declared
// descriptors: the body names them by index, as a peer writes them.
func frame(t *testing.T, declared uint32, sig string, body ...any) []byte {
	t.Helper()
	msg := &Message{Type: TypeSignal, Headers: map[HeaderField]Variant{
		FieldPath:      MakeVariant(ObjectPath("/x")),
		FieldInterface: MakeVariant("a.b"),
		FieldMember:    MakeVariant("M"),
	}, Body: body}
	if sig != "" {
		msg.Headers[FieldSignature] = MakeVariant(Signature{sig})
	}
	if declared != 0 {
		msg.Headers[FieldUnixFDs] = MakeVariant(declared)
	}
	var buf bytes.Buffer
	if err := msg.EncodeTo(&buf, binary.LittleEndian); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pipes(t *testing.T, n int) (write []int) {
	t.Helper()
	for range n {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = r.Close()
			_ = w.Close()
		})
		write = append(write, int(w.Fd()))
	}
	return write
}

// TestTheDescriptorsOfAReadMessageAreTheBodysOrClosed is the other half
// of the received-descriptors patch: upstream closed none of the
// descriptors a message came with, so those its body did not name, and
// all of them when it failed to decode, stayed open for good.
func TestTheDescriptorsOfAReadMessageAreTheBodysOrClosed(t *testing.T) {
	type invalid = InvalidMessageError
	for _, tc := range []struct {
		name     string
		attached int
		frame    func(t *testing.T) []byte
		taken    []int
		failure  any
	}{
		{name: "a body naming one of three", attached: 3,
			frame: func(t *testing.T) []byte { return frame(t, 3, "h", UnixFDIndex(1)) }, taken: []int{1}},
		{name: "a body naming none", attached: 2,
			frame: func(t *testing.T) []byte { return frame(t, 2, "s", "no descriptor") }},
		{name: "no body", attached: 2,
			frame: func(t *testing.T) []byte { return frame(t, 2, "") }},
		{name: "a dict of descriptors", attached: 2,
			frame: func(t *testing.T) []byte { return frame(t, 2, "a{sh}", map[string]UnixFDIndex{"a": 0, "b": 1}) },
			taken: []int{0, 1}},
		{name: "one descriptor named twice", attached: 1,
			frame: func(t *testing.T) []byte { return frame(t, 1, "ah", []UnixFDIndex{0, 0}) }, taken: []int{0, 0}},
		{name: "an index past the descriptors", attached: 2,
			frame: func(t *testing.T) []byte { return frame(t, 2, "hh", UnixFDIndex(0), UnixFDIndex(7)) }, failure: invalid("")},
		{name: "a duplicate made before the body failed", attached: 1,
			frame: func(t *testing.T) []byte { return frame(t, 1, "ah", []UnixFDIndex{0, 0, 9}) }, failure: invalid("")},
		{name: "a malformed body", attached: 2,
			frame: func(t *testing.T) []byte { return frame(t, 2, "hb", UnixFDIndex(0), uint32(2)) }, failure: FormatError("")},
		{name: "descriptors the header does not declare", attached: 2,
			frame: func(t *testing.T) []byte { return frame(t, 0, "s", "x") }, failure: invalid("")},
		{name: "fewer descriptors than the header declares", attached: 1,
			frame: func(t *testing.T) []byte { return frame(t, 3, "h", UnixFDIndex(2)) }, failure: invalid("")},
		{name: "an invalid header", attached: 2,
			frame: func(t *testing.T) []byte {
				f := frame(t, 2, "h", UnixFDIndex(0))
				f[1] = 0
				return f
			}, failure: invalid("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport, peer := transportPair(t)
			write := pipes(t, tc.attached)
			before := openDescriptors(t)
			data := tc.frame(t)
			if n, oobn, err := peer.WriteMsgUnix(data, syscall.UnixRights(write...), nil); err != nil || n != len(data) || oobn == 0 {
				t.Fatalf("send: %d of %d bytes, %d of control data, %v", n, len(data), oobn, err)
			}
			msg, err := transport.ReadMessage()
			if tc.failure != nil {
				if err == nil || reflect.TypeOf(err) != reflect.TypeOf(tc.failure) {
					t.Fatalf("ReadMessage = %#v, %#v; want a %T", msg, err, tc.failure)
				}
				if got := openDescriptors(t); got != before {
					t.Errorf("%d descriptors open after a message that was not delivered, want the %d before", got, before)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []int
			for _, v := range msg.Body {
				mapFDs(v, func(fd UnixFD) UnixFD {
					got = append(got, int(fd))
					return fd
				})
			}
			if len(got) != len(tc.taken) {
				t.Fatalf("the body %#v holds %d descriptors, want %d", msg.Body, len(got), len(tc.taken))
			}
			if open := openDescriptors(t); open != before+len(tc.taken) {
				t.Errorf("%d descriptors open, want the %d before and the %d the body holds", open, before, len(tc.taken))
			}
			pipeOf := func(fd int) int {
				for i, w := range write {
					if samePipe(t, fd, w) {
						return i
					}
				}
				return -1
			}
			held := make([]int, len(got))
			for i, fd := range got {
				held[i] = pipeOf(fd)
			}
			seen := map[int]bool{}
			for _, fd := range got {
				if seen[fd] {
					t.Errorf("the body holds descriptor %d twice", fd)
				}
				seen[fd] = true
			}
			slices.Sort(held)
			if !slices.Equal(held, tc.taken) {
				t.Errorf("the body holds the descriptors of pipes %v, want %v", held, tc.taken)
			}
			if err := CloseFDs(msg.Body); err != nil {
				t.Error(err)
			}
			if open := openDescriptors(t); open != before {
				t.Errorf("%d descriptors open once the body's are closed, want the %d before", open, before)
			}
		})
	}
}

// A header length near 2^32 passed the size limit and had the transport
// read 4 GiB of header into memory.
func TestTheTransportRefusesLengthsNearTheWrap(t *testing.T) {
	transport, peer := transportPair(t)
	head := []byte{'l', byte(TypeSignal), 0, protoVersion, 0x20, 0, 0, 0, 1, 0, 0, 0, 0xf1, 0xff, 0xff, 0xff}
	if _, err := peer.Write(head); err != nil {
		t.Fatal(err)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.ReadMessage(); err != InvalidMessageError("message is too long") {
		t.Errorf("ReadMessage = %v, want the message refused as too long", err)
	}
}
