package wl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"syscall"
	"unsafe"

	"github.com/stubbedev/lys-lib/third_party/wayland/os"
)

// ErrContextSendRequestNotPossible is raised in case sending wl request could not be done
var ErrContextSendRequestNotPossible = errors.New("no write request means")

// ErrContextSendRequestUnix is raised in case sending wl request fails
var ErrContextSendRequestUnix = errors.New("unable to send request using unix")

// ErrContextSendRequestConn is raised in case sending wl request fails
var ErrContextSendRequestConn = errors.New("unable to send request using conn")

// ErrContextSendRequestUnixLength is raised in case sending wl request fails
var ErrContextSendRequestUnixLength = errors.New("unable to send request using unix, WriteMsgUnix length check failed")

// maxBufferedRequests is how many bytes of requests wait for a flush
// before one goes out regardless, and maxBufferedFDs how many fds: libwayland
// takes no more with one read.
const (
	maxBufferedRequests = 4096
	maxBufferedFDs      = 28
)

// maxBacklog bounds the requests a compositor that stopped reading can pile
// up in the client. Past it SendRequest fails with ErrContextBacklogFull.
const maxBacklog = 1 << 20

// ErrContextBacklogFull reports a request refused because maxBacklog bytes
// already wait for a compositor that is not reading. A protocol stream with a
// request missing means nothing, so the connection writes no more: this request
// and every later one fail with it, while reads go on to what the compositor sent.
var ErrContextBacklogFull = errors.New("wl: the compositor is not reading: request backlog full")

// CommitDeferred is implemented by a proxy with requests whose only
// effect is on state a later request applies: nothing can wait on them,
// so they stay in the context's buffer and travel in one write with the
// request that applies them instead of waking the compositor for each.
type CommitDeferred interface {
	DeferredRequest(opcode uint32) bool
}

const (
	surfaceDestroy uint32 = 0
	surfaceCommit  uint32 = 6
)

// DeferredRequest is true of all a wl_surface's requests but commit, which
// applies their double-buffered state, and destroy.
func (p *Surface) DeferredRequest(opcode uint32) bool {
	return opcode != surfaceCommit && opcode != surfaceDestroy
}

// SendRequest (Context SendRequest) sends a specific request with arguments to the compositor
func (ctx *Context) SendRequest(proxy Proxy, opcode uint32, args ...interface{}) error {
	ctx.sendMu.Lock()
	defer ctx.sendMu.Unlock()
	err := ctx.sendLocked(proxy, opcode, args)
	if err != nil {
		for _, arg := range args {
			if p, ok := arg.(Proxy); ok && !isNil(p) {
				ctx.forgetUnsentLocked(p.Id())
			}
		}
	}
	return err
}

func (ctx *Context) sendLocked(proxy Proxy, opcode uint32, args []interface{}) error {
	if ctx.conn == nil {
		return ErrContextSendRequestNotPossible
	}
	if ctx.sendErr != nil {
		return ctx.sendErr
	}
	if len(ctx.out) >= maxBacklog {
		if _, err := ctx.flushLocked(); err != nil {
			return err
		}
		if len(ctx.out) >= maxBacklog {
			ctx.failSendLocked(fmt.Errorf("%w: %d bytes to object %d's opcode %d", ErrContextBacklogFull, len(ctx.out), proxy.Id(), opcode))
			return ctx.sendErr
		}
	}
	// A new id may only go out once every smaller id is on the wire; see
	// the comment on Context.sendMu. Requests without a new id never wait.
	if len(ctx.unsent) > 0 {
		for _, arg := range args {
			p, ok := arg.(Proxy)
			if !ok || isNil(p) {
				continue
			}
			id := p.Id()
			if _, pending := ctx.unsent[id]; pending {
				for ctx.sendErr == nil && ctx.smallerUnsentLocked(id) {
					ctx.sendCond.Wait()
				}
				if ctx.sendErr != nil {
					return ctx.sendErr
				}
			}
		}
	}
	if err := ctx.encodeRequest(proxy.Id(), opcode, args); err != nil {
		return err
	}
	if len(ctx.unsent) > 0 {
		for _, arg := range args {
			if p, ok := arg.(Proxy); ok && !isNil(p) {
				ctx.forgetUnsentLocked(p.Id())
			}
		}
	}
	if len(ctx.out) < maxBufferedRequests && len(ctx.outFDs) < maxBufferedFDs {
		if ctx.buffered {
			return nil
		}
		if d, ok := proxy.(CommitDeferred); ok && d.DeferredRequest(opcode) {
			return nil
		}
	}
	_, err := ctx.flushLocked()
	return err
}

// encodeRequest appends the request to the buffer, or nothing on a bad
// argument.
func (ctx *Context) encodeRequest(id ProxyId, opcode uint32, args []interface{}) error {
	start, oob, fds := len(ctx.out), len(ctx.outOOB), len(ctx.outFDs)
	undo := func() {
		ctx.out, ctx.outOOB = ctx.out[:start], ctx.outOOB[:oob]
		for _, fd := range ctx.outFDs[fds:] {
			_ = syscall.Close(fd)
		}
		ctx.outFDs = ctx.outFDs[:fds]
	}
	ctx.out = binary.NativeEndian.AppendUint32(ctx.out, uint32(id))
	ctx.out = binary.NativeEndian.AppendUint32(ctx.out, 0)
	for _, arg := range args {
		if err := ctx.encodeArgument(arg); err != nil {
			undo()
			return err
		}
	}
	size := len(ctx.out) - start
	if size > math.MaxUint16 {
		undo()
		return fmt.Errorf("wl: a request of %d bytes to object %d exceeds the wire's 16-bit size", size, id)
	}
	binary.NativeEndian.PutUint32(ctx.out[start+4:], uint32(size)<<16|opcode&0xffff)
	return nil
}

func (ctx *Context) encodeArgument(arg interface{}) error {
	switch t := arg.(type) {
	case Proxy:
		var id uint32
		if !isNil(t) {
			id = uint32(t.Id())
		}
		ctx.out = binary.NativeEndian.AppendUint32(ctx.out, id)
	case uint32:
		ctx.out = binary.NativeEndian.AppendUint32(ctx.out, t)
	case int32:
		ctx.out = binary.NativeEndian.AppendUint32(ctx.out, uint32(t))
	case float32:
		ctx.out = binary.NativeEndian.AppendUint32(ctx.out, uint32(FloatToFixed(float64(t))))
	case string:
		ctx.out = binary.NativeEndian.AppendUint32(ctx.out, uint32(len(t)+1))
		ctx.out = append(ctx.out, t...)
		ctx.out = append(ctx.out, make([]byte, 4-len(t)&3)...)
	case []int32:
		ctx.out = binary.NativeEndian.AppendUint32(ctx.out, uint32(len(t)*4))
		for _, e := range t {
			ctx.out = binary.NativeEndian.AppendUint32(ctx.out, uint32(e))
		}
	case uintptr:
		fd, _, errno := syscall.Syscall(syscall.SYS_FCNTL, t, syscall.F_DUPFD_CLOEXEC, 0)
		if errno != 0 {
			return fmt.Errorf("wl: hold fd %d until it is sent: %w", t, errno)
		}
		ctx.outFDs = append(ctx.outFDs, int(fd))
		ctx.outOOB = append(ctx.outOOB, os.UnixRights(int(fd))...)
	default:
		return fmt.Errorf("invalid Wayland request parameter type %T", arg)
	}
	return nil
}

// isNil reports a nil proxy, typed or not. Every proxy is a pointer, so
// the interface's data word says; the generated requests pass nil
// objects as typed pointers.
func isNil(p Proxy) bool {
	return (*[2]unsafe.Pointer)(unsafe.Pointer(&p))[1] == nil
}

// Flush makes one non-blocking try at writing the buffered requests and
// reports whether some are left: the socket is full because the compositor
// has not read it, and WaitWritable says when to try again. It never blocks.
func (ctx *Context) Flush() (pending bool, err error) {
	ctx.sendMu.Lock()
	defer ctx.sendMu.Unlock()
	return ctx.flushLocked()
}

// flushBeforeRead flushes what a read may be waiting on the answer to. A write
// that fails is kept for the next request to report: what the compositor sent
// before it went away, a protocol error above all, is still there to read.
func (ctx *Context) flushBeforeRead() {
	ctx.sendMu.Lock()
	defer ctx.sendMu.Unlock()
	_, _ = ctx.flushLocked()
}

// failSendLocked ends writing for good: the first error stays, nothing queued
// will ever go, and a writer waiting on id order is let go.
func (ctx *Context) failSendLocked(err error) {
	ctx.sendErr = err
	ctx.closeSentFDs()
	ctx.out = ctx.out[:0]
	if ctx.sendCond != nil {
		ctx.sendCond.Broadcast()
	}
}

func (ctx *Context) flushLocked() (bool, error) {
	if ctx.sendErr != nil {
		return false, ctx.sendErr
	}
	if len(ctx.out) == 0 {
		return false, nil
	}
	n, err := ctx.socketSend()
	ctx.sends++
	if err != nil {
		ctx.failSendLocked(combinedError{ErrContextSendRequestConn, err})
		return false, ctx.sendErr
	}
	if n == 0 {
		return true, nil
	}
	ctx.closeSentFDs()
	ctx.out = ctx.out[:copy(ctx.out, ctx.out[n:])]
	return len(ctx.out) > 0, nil
}

func (ctx *Context) closeSentFDs() {
	for _, fd := range ctx.outFDs {
		_ = syscall.Close(fd)
	}
	ctx.outFDs = ctx.outFDs[:0]
	ctx.outOOB = ctx.outOOB[:0]
}

// Backlog is how many bytes of requests wait for the socket. A frame loop
// that finds some left after Flush skips its frame instead of queueing more
// behind a compositor that is not reading.
func (ctx *Context) Backlog() int {
	ctx.sendMu.Lock()
	defer ctx.sendMu.Unlock()
	return len(ctx.out)
}

// SetBuffered makes every request wait in the buffer for Flush, a read that
// may block or the buffer filling, so a burst of requests costs the compositor
// one wakeup. A caller setting it flushes before it parks anywhere else than
// Run, ReadReady or WaitReadable, which flush themselves.
func (ctx *Context) SetBuffered(buffered bool) {
	ctx.sendMu.Lock()
	ctx.buffered = buffered
	ctx.sendMu.Unlock()
}

// WaitWritable blocks until the socket may take more requests or is closed;
// a wakeup with no room is possible.
func (ctx *Context) WaitWritable() error {
	raw, err := ctx.rawConn()
	if err != nil {
		return err
	}
	polled := false
	err = raw.Write(func(uintptr) bool {
		done := polled
		polled = true
		return done
	})
	if err != nil {
		return fmt.Errorf("wl: wait for room in the socket: %w", err)
	}
	return nil
}
