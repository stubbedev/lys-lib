package wl

import (
	"encoding/binary"
	"io"
	"os"
	"syscall"
	"testing"
)

// A nil object argument goes out as id 0 instead of panicking with the
// send lock held.
func TestNilObjectArgument(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fds[0])
	ctx := &Context{sockFD: fds[0], objects: make(map[ProxyId]Proxy)}
	reader := os.NewFile(uintptr(fds[1]), "compositor")
	defer reader.Close()

	dev := &DataDevice{}
	dev.SetContext(ctx)
	dev.SetId(5)
	if err := dev.SetSelection(nil, 42); err != nil {
		t.Fatal(err)
	}
	msg := make([]byte, 16) // object, size<<16|opcode, source, serial
	if _, err := io.ReadFull(reader, msg); err != nil {
		t.Fatal(err)
	}
	if src, serial := binary.LittleEndian.Uint32(msg[8:]), binary.LittleEndian.Uint32(msg[12:]); src != 0 || serial != 42 {
		t.Fatalf("set_selection(nil, 42) sent source %d serial %d", src, serial)
	}
}
