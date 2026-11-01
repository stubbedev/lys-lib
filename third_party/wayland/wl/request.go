package wl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
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
// before one goes out regardless.
const maxBufferedRequests = 4096

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
	if ctx.conn == nil && ctx.sockFD == -1 {
		return ErrContextSendRequestNotPossible
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
				for ctx.smallerUnsentLocked(id) {
					ctx.sendCond.Wait()
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
	if d, ok := proxy.(CommitDeferred); ok && d.DeferredRequest(opcode) && len(ctx.out) < maxBufferedRequests {
		return nil
	}
	return ctx.flushRequests()
}

// encodeRequest appends the request to the buffer, or nothing on a bad
// argument.
func (ctx *Context) encodeRequest(id ProxyId, opcode uint32, args []interface{}) error {
	start, oob := len(ctx.out), len(ctx.outOOB)
	ctx.out = binary.NativeEndian.AppendUint32(ctx.out, uint32(id))
	ctx.out = binary.NativeEndian.AppendUint32(ctx.out, 0)
	for _, arg := range args {
		if err := ctx.encodeArgument(arg); err != nil {
			ctx.out, ctx.outOOB = ctx.out[:start], ctx.outOOB[:oob]
			return err
		}
	}
	size := len(ctx.out) - start
	if size > math.MaxUint16 {
		ctx.out, ctx.outOOB = ctx.out[:start], ctx.outOOB[:oob]
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
		ctx.outOOB = append(ctx.outOOB, os.UnixRights(int(t))...)
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

// flushRequests writes out the buffered requests. The socket may take
// fewer bytes than a whole buffer at once; the fds went with the first.
func (ctx *Context) flushRequests() error {
	out, oob := ctx.out, ctx.outOOB
	ctx.out, ctx.outOOB = out[:0], oob[:0]
	if ctx.conn == nil {
		if err := os.Sendmsg(ctx.sockFD, out, oob, nil, 0); err != nil {
			return combinedError{ErrContextSendRequestUnix, err}
		}
		return nil
	}
	d, c, err := ctx.conn.WriteMsgUnix(out, oob, nil)
	if err != nil {
		return combinedError{ErrContextSendRequestConn, err}
	}
	if c != len(oob) {
		return ErrContextSendRequestUnixLength
	}
	if d < len(out) {
		if _, err := ctx.conn.Write(out[d:]); err != nil {
			return combinedError{ErrContextSendRequestConn, err}
		}
	}
	return nil
}
