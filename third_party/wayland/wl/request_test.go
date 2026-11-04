package wl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"testing"
	"time"
)

func requestContext(t testing.TB) (*Context, *os.File) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, sock := socketContext(t, fds)
	return ctx, os.NewFile(uintptr(dupForTest(t, sock)), "compositor")
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
	reaper := idReaper{ctx}
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
		reaper.HandleDisplayDeleteId(DisplayDeleteIdEvent{Id: uint32(cb.Id())})
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

// A flush larger than the socket takes at once keeps the rest for later
// flushes, not reported as a short write and never blocking.
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
		if err := s.Commit(); err != nil {
			sent <- err
			return
		}
		for {
			pending, err := ctx.Flush()
			if err != nil || !pending {
				sent <- err
				return
			}
			if err := ctx.WaitWritable(); err != nil {
				sent <- err
				return
			}
		}
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

func bufferedContext(t *testing.T) (*Context, *os.File) {
	t.Helper()
	ctx, peer := requestContext(t)
	t.Cleanup(func() { peer.Close() })
	ctx.SetBuffered(true)
	return ctx, peer
}

// Opening a popup takes a burst of requests of every kind, which go out in one
// write when the loop flushes.
func TestABurstOfRequestsIsOneWrite(t *testing.T) {
	ctx, peer := bufferedContext(t)
	s, r, dev := NewSurface(ctx), NewRegion(ctx), NewDataDevice(ctx)
	for _, p := range []Proxy{s, r, dev} {
		ctx.forgetUnsent(p.Id())
	}
	before := ctx.sends
	for i := range 8 {
		if err := r.Add(0, int32(i), 8, 8); err != nil {
			t.Fatal(err)
		}
		if err := dev.SetSelection(nil, uint32(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetInputRegion(r); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := ctx.sends - before; n != 0 {
		t.Fatalf("%d writes before the flush", n)
	}
	if _, _, err := syscall.Recvfrom(int(peer.Fd()), make([]byte, 1), syscall.MSG_DONTWAIT); !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("requests reached the socket before the flush: %v", err)
	}
	if pending, err := ctx.Flush(); pending || err != nil {
		t.Fatalf("Flush: pending %v, %v", pending, err)
	}
	if n := ctx.sends - before; n != 1 {
		t.Errorf("%d writes for the burst, want 1", n)
	}
	for range 16 {
		readMessage(t, peer)
	}
	if id, op, _ := readMessage(t, peer); id != uint32(s.Id()) || op != surfaceSetInputRegion {
		t.Errorf("object %d opcode %d out of order", id, op)
	}
}

// A fd's request may wait in the buffer after the caller closed its copy.
func TestABufferedFdOutlivesTheCallersCopy(t *testing.T) {
	ctx, peer := bufferedContext(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	dev := NewDataOffer(ctx)
	if err := dev.Receive("text/plain", w.Fd()); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if pending, err := ctx.Flush(); pending || err != nil {
		t.Fatalf("Flush: pending %v, %v", pending, err)
	}
	buf, oob := make([]byte, 64), make([]byte, syscall.CmsgSpace(4))
	_, oobn, _, _, err := syscall.Recvmsg(int(peer.Fd()), buf, oob, 0)
	if err != nil {
		t.Fatal(err)
	}
	scm, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(scm) != 1 {
		t.Fatalf("control messages %v, %v", scm, err)
	}
	fds, err := syscall.ParseUnixRights(&scm[0])
	if err != nil || len(fds) != 1 {
		t.Fatalf("fds %v, %v", fds, err)
	}
	got := os.NewFile(uintptr(fds[0]), "sent")
	defer got.Close()
	if _, err := got.Write([]byte{1}); err != nil {
		t.Errorf("the fd that arrived does not write the pipe: %v", err)
	}
	if len(ctx.outFDs) != 0 {
		t.Errorf("%d duplicates still open after they were sent", len(ctx.outFDs))
	}
}

// A compositor that stops reading never blocks a request: past the backlog's
// bound one fails, precisely, and Flush reports what waits.
func TestRequestsToAPeerThatNeverReadsAllReturn(t *testing.T) {
	ctx, peer := requestContext(t)
	defer peer.Close()
	s := NewSurface(ctx)
	returned := make(chan error, 1)
	go func() {
		var last error
		for range 200000 {
			if err := s.Commit(); err != nil {
				last = err
			}
		}
		returned <- last
	}()
	select {
	case err := <-returned:
		if !errors.Is(err, ErrContextBacklogFull) {
			t.Errorf("past the bound: %v, want ErrContextBacklogFull", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("a request blocked on a peer that does not read")
	}
	if _, err := ctx.Flush(); !errors.Is(err, ErrContextBacklogFull) {
		t.Errorf("Flush after the refusal: %v", err)
	}
	if n := ctx.Backlog(); n != 0 {
		t.Errorf("a connection that writes no more holds %d bytes", n)
	}
}

// A compositor's protocol error followed by its close is what a client must
// report, however many of its own writes meet the closed socket first.
func TestAProtocolErrorIsReadAfterAWriteHitTheClosedSocket(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, rawPeer := socketContext(t, fds)
	peer := os.NewFile(uintptr(dupForTest(t, rawPeer)), "compositor")
	display := ctx.newDisplay()
	var got DisplayErrorEvent
	display.AddErrorHandler(&errorHandler{func(ev DisplayErrorEvent) { got = ev }})
	s := NewSurface(ctx)
	ctx.forgetUnsent(s.Id())
	message := "bad"
	body := binary.NativeEndian.AppendUint32(nil, uint32(s.Id()))
	body = binary.NativeEndian.AppendUint32(body, 3)
	body = binary.NativeEndian.AppendUint32(body, uint32(len(message)+1))
	body = append(body, message...)
	body = append(body, 0, 0, 0, 0)[:12+(len(message)+4)&^3]
	event := binary.NativeEndian.AppendUint32(nil, uint32(display.Id()))
	event = binary.NativeEndian.AppendUint32(event, uint32(8+len(body))<<16)
	if _, err := peer.Write(append(event, body...)); err != nil {
		t.Fatal(err)
	}
	peer.Close()
	syscall.Close(rawPeer)
	if err := s.Commit(); err == nil {
		t.Fatal("a commit to a closed socket reported nothing")
	}
	if err := ctx.Run(); err != nil && !errors.Is(err, ErrContextRunProtocolError) {
		t.Fatalf("Run after the failed write: %v", err)
	}
	if got.Code != 3 || got.Message != message {
		t.Errorf("the error event read back as %+v", got)
	}
}

type errorHandler struct{ handle func(DisplayErrorEvent) }

func (h *errorHandler) HandleDisplayError(ev DisplayErrorEvent) { h.handle(ev) }

// A new-id request refused for the backlog leaves its id on no one's way: the
// next new-id request returns too.
func TestARefusedNewIdRequestBlocksNoLaterOne(t *testing.T) {
	ctx, peer := requestContext(t)
	defer peer.Close()
	surface, display := NewSurface(ctx), ctx.newDisplay()
	ctx.forgetUnsent(surface.Id())
	filler := make([]byte, 60000)
	for ctx.Backlog() < maxBacklog {
		if err := ctx.SendRequest(surface, 2, string(filler)); err != nil {
			t.Fatal(err)
		}
	}
	returned := make(chan error, 1)
	go func() {
		if _, err := surface.Frame(); !errors.Is(err, ErrContextBacklogFull) {
			returned <- fmt.Errorf("the refused frame request: %w", err)
			return
		}
		_, err := display.Sync()
		returned <- err
	}()
	select {
	case err := <-returned:
		if !errors.Is(err, ErrContextBacklogFull) {
			t.Errorf("the next new-id request: %v", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("a new-id request after a refusal blocked")
	}
}

// A real-wire frame allocates nothing, from the commit through the event back.
func TestARealWireFrameAllocatesNothing(t *testing.T) {
	ctx, peer := requestContext(t)
	defer peer.Close()
	surface, buffer, display := NewSurface(ctx), NewBuffer(ctx), ctx.newDisplay()
	ctx.forgetUnsent(surface.Id())
	ctx.forgetUnsent(buffer.Id())
	go func() { _, _ = io.Copy(io.Discard, peer) }()
	ctx.SetBuffered(true)
	var handled frameDone
	frame := func() {
		if err := surface.Attach(buffer, 0, 0); err != nil {
			t.Fatal(err)
		}
		cb, err := surface.Frame()
		if err != nil {
			t.Fatal(err)
		}
		cb.AddDoneHandler(&handled)
		if err := surface.Commit(); err != nil {
			t.Fatal(err)
		}
		if _, err := ctx.Flush(); err != nil {
			t.Fatal(err)
		}
		reply := handled.replies(uint32(cb.Id()), uint32(display.Id()))
		if _, err := syscall.Write(handled.sock(t, peer), reply); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := ctx.Run(); err != nil {
				t.Fatal(err)
			}
		}
	}
	for range 3 {
		frame()
	}
	if n := testing.AllocsPerRun(100, frame); n != 0 {
		t.Errorf("%v allocations a frame on the wire, want 0", n)
	}
	if handled.n == 0 {
		t.Error("the frame's done never reached its handler")
	}
}

type frameDone struct {
	n   int
	buf [24]byte
	fd  int
}

func (h *frameDone) HandleCallbackDone(CallbackDoneEvent) { h.n++ }

func (h *frameDone) sock(t *testing.T, peer *os.File) int {
	t.Helper()
	if h.fd == 0 {
		h.fd = int(peer.Fd())
	}
	return h.fd
}

func (h *frameDone) replies(cb, display uint32) []byte {
	b := h.buf[:0]
	b = binary.NativeEndian.AppendUint32(b, cb)
	b = binary.NativeEndian.AppendUint32(b, 12<<16)
	b = binary.NativeEndian.AppendUint32(b, 0)
	b = binary.NativeEndian.AppendUint32(b, display)
	b = binary.NativeEndian.AppendUint32(b, 12<<16|1)
	b = binary.NativeEndian.AppendUint32(b, cb)
	return b
}

const surfaceSetInputRegion uint32 = 5

func fillSocket(t *testing.T, ctx *Context) {
	t.Helper()
	s := NewSurface(ctx)
	for {
		if err := s.Commit(); err != nil {
			t.Fatal(err)
		}
		if ctx.Backlog() > 0 {
			return
		}
	}
}

// A request with an fd queued behind a full socket sends the file the caller
// passed, though the caller closed its copy and the number now names another.
func TestAQueuedFdIsTheOriginalFileHoweverItsNumberIsReused(t *testing.T) {
	ctx, peer := requestContext(t)
	defer peer.Close()
	fillSocket(t, ctx)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	number := w.Fd()
	if err := NewDataOffer(ctx).Receive("text/plain", number); err != nil {
		t.Fatal(err)
	}
	if ctx.Backlog() == 0 {
		t.Fatal("the fd's request was not queued behind the full socket")
	}
	w.Close()
	other, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.Fd() != number {
		t.Fatalf("the number %d was not reused: got %d", number, other.Fd())
	}
	var received []int
	deadline := time.Now().Add(time.Minute)
	buf, oob := make([]byte, 1<<16), make([]byte, syscall.CmsgSpace(4))
	for len(received) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the fd never arrived")
		}
		if _, err := ctx.Flush(); err != nil {
			t.Fatal(err)
		}
		_, oobn, _, _, err := syscall.Recvmsg(int(peer.Fd()), buf, oob, 0)
		if err != nil {
			t.Fatal(err)
		}
		if oobn == 0 {
			continue
		}
		scm, err := syscall.ParseSocketControlMessage(oob[:oobn])
		if err != nil {
			t.Fatal(err)
		}
		if received, err = syscall.ParseUnixRights(&scm[0]); err != nil {
			t.Fatal(err)
		}
	}
	got := os.NewFile(uintptr(received[0]), "sent")
	defer got.Close()
	if _, err := got.Write([]byte{7}); err != nil {
		t.Fatalf("the fd that arrived does not write the pipe: %v", err)
	}
	var b [1]byte
	if _, err := r.Read(b[:]); err != nil || b[0] != 7 {
		t.Errorf("the pipe read %d, %v, want the byte the peer wrote", b[0], err)
	}
}

func TestQueuedFdsAreClosedOnceSentAndWithTheConnection(t *testing.T) {
	send := func(t *testing.T, ctx *Context) {
		t.Helper()
		f, err := os.Open("/dev/null")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := NewDataOffer(ctx).Receive("text/plain", f.Fd()); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("sent", func(t *testing.T) {
		ctx, peer := requestContext(t)
		defer peer.Close()
		before := openFDs(t)
		send(t, ctx)
		if pending, err := ctx.Flush(); pending || err != nil {
			t.Fatalf("Flush: pending %v, %v", pending, err)
		}
		buf, oob := make([]byte, 64), make([]byte, syscall.CmsgSpace(4))
		_, oobn, _, _, err := syscall.Recvmsg(int(peer.Fd()), buf, oob, 0)
		if err != nil {
			t.Fatal(err)
		}
		scm, err := syscall.ParseSocketControlMessage(oob[:oobn])
		if err != nil {
			t.Fatal(err)
		}
		fds, err := syscall.ParseUnixRights(&scm[0])
		if err != nil {
			t.Fatal(err)
		}
		syscall.Close(fds[0])
		if n := openFDs(t); n != before {
			t.Errorf("%d open fds after the send, %d before", n, before)
		}
	})
	t.Run("queued when the connection closes", func(t *testing.T) {
		ctx, peer := requestContext(t)
		defer peer.Close()
		before := openFDs(t)
		fillSocket(t, ctx)
		send(t, ctx)
		if len(ctx.outFDs) != 1 {
			t.Fatalf("%d fds queued, want 1", len(ctx.outFDs))
		}
		if err := ctx.Close(); err != nil {
			t.Fatal(err)
		}
		if n := openFDs(t); n != before-1 {
			t.Errorf("%d open fds after the connection closed, want %d: its socket gone and nothing queued left", n, before-1)
		}
	})
}
