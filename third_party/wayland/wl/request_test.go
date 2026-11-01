package wl

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
)

func requestContext(t testing.TB) (*Context, *os.File) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, sock := socketContext(t, fds)
	return ctx, os.NewFile(uintptr(sock), "compositor")
}

// BenchmarkSurfaceFrame sends a window's frame: attach, damage, a frame
// callback and the commit that applies them.
func BenchmarkSurfaceFrame(b *testing.B) {
	ctx, peer := requestContext(b)
	surface, buffer := NewSurface(ctx), NewBuffer(ctx)
	ctx.forgetUnsent(surface.Id())
	ctx.forgetUnsent(buffer.Id())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(io.Discard, peer)
	}()
	b.ReportAllocs()
	for b.Loop() {
		if err := surface.Attach(buffer, 0, 0); err != nil {
			b.Fatal(err)
		}
		if err := surface.DamageBuffer(0, 0, 1920, 1080); err != nil {
			b.Fatal(err)
		}
		cb, err := surface.Frame()
		if err != nil {
			b.Fatal(err)
		}
		if err := surface.Commit(); err != nil {
			b.Fatal(err)
		}
		ctx.Unregister(cb.Id())
	}
	_ = ctx.conn.CloseWrite()
	<-done
}

func readMessage(t *testing.T, r io.Reader) (id, opcode uint32, body []byte) {
	t.Helper()
	head := make([]byte, 8)
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatal(err)
	}
	id = binary.NativeEndian.Uint32(head)
	word := binary.NativeEndian.Uint32(head[4:])
	body = make([]byte, word>>16-8)
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatal(err)
	}
	return id, word & 0xffff, body
}

// Double-buffered surface state wakes the compositor only when the
// commit that applies it does, and arrives whole and in order.
func TestSurfaceStateWaitsForItsCommit(t *testing.T) {
	ctx, peer := requestContext(t)
	defer peer.Close()
	s := NewSurface(ctx)
	if err := s.Attach(nil, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.DamageBuffer(0, 0, 640, 480); err != nil {
		t.Fatal(err)
	}
	if _, _, err := syscall.Recvfrom(int(peer.Fd()), make([]byte, 1), syscall.MSG_DONTWAIT); !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("surface state reached the socket before its commit: %v", err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint32{surfaceAttach, surfaceDamageBuffer, surfaceCommit} {
		id, op, _ := readMessage(t, peer)
		if id != uint32(s.Id()) || op != want {
			t.Fatalf("got object %d opcode %d, want %d opcode %d", id, op, s.Id(), want)
		}
	}
}

// Any other request carries the waiting surface state ahead of itself,
// so the compositor sees requests in the order they were made.
func TestOtherRequestsKeepTheOrder(t *testing.T) {
	ctx, peer := requestContext(t)
	defer peer.Close()
	s, dev := NewSurface(ctx), NewDataDevice(ctx)
	if err := s.Damage(0, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := dev.SetSelection(nil, 7); err != nil {
		t.Fatal(err)
	}
	if id, op, _ := readMessage(t, peer); id != uint32(s.Id()) || op != surfaceDamage {
		t.Fatalf("first message: object %d opcode %d", id, op)
	}
	if id, _, body := readMessage(t, peer); id != uint32(dev.Id()) || binary.NativeEndian.Uint32(body[4:]) != 7 {
		t.Fatalf("second message: object %d body %v", id, body)
	}
}

// A flush larger than the socket takes at once is finished, not
// reported as a short write: the fds go with the first part.
func TestAFlushLargerThanTheSocketBufferFinishes(t *testing.T) {
	ctx, peer := requestContext(t)
	defer peer.Close()
	raw, err := ctx.conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Control(func(fd uintptr) {
		if err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, 2048); err != nil {
			t.Error(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	s := NewSurface(ctx)
	const rounds = 2000
	sent := make(chan error, 1)
	go func() {
		for range rounds {
			if err := s.Damage(0, 0, 1, 1); err != nil {
				sent <- err
				return
			}
		}
		sent <- s.Commit()
	}()
	for range rounds {
		if _, op, _ := readMessage(t, peer); op != surfaceDamage {
			t.Fatalf("opcode %d in the middle of the damage", op)
		}
	}
	if _, op, _ := readMessage(t, peer); op != surfaceCommit {
		t.Fatalf("opcode %d, want the commit", op)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
}

func TestStringAndArrayWireForm(t *testing.T) {
	ctx, peer := requestContext(t)
	defer peer.Close()
	src := NewDataSource(ctx)
	if err := ctx.SendRequest(src, 0, "abc", []int32{5, 6}, uint32(9)); err != nil {
		t.Fatal(err)
	}
	_, _, body := readMessage(t, peer)
	want := []byte{}
	want = binary.NativeEndian.AppendUint32(want, 4)
	want = append(want, 'a', 'b', 'c', 0)
	want = binary.NativeEndian.AppendUint32(want, 8)
	want = binary.NativeEndian.AppendUint32(want, 5)
	want = binary.NativeEndian.AppendUint32(want, 6)
	want = binary.NativeEndian.AppendUint32(want, 9)
	if string(body) != string(want) {
		t.Fatalf("body %v, want %v", body, want)
	}
}

func TestABadArgumentSendsNothing(t *testing.T) {
	ctx, peer := requestContext(t)
	defer peer.Close()
	s := NewSurface(ctx)
	if err := ctx.SendRequest(s, 2, 3.5); err == nil {
		t.Fatal("a float64 argument was accepted")
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, op, _ := readMessage(t, peer); op != surfaceCommit {
		t.Fatalf("opcode %d, want the commit alone", op)
	}
}

const (
	surfaceAttach       uint32 = 1
	surfaceDamage       uint32 = 2
	surfaceDamageBuffer uint32 = 9
)
