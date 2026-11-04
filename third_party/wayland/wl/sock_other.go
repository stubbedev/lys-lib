//go:build !linux

package wl

import (
	"errors"
	"fmt"
	"syscall"
)

type socketIO struct{ raw syscall.RawConn }

func (ctx *Context) rawConn() (syscall.RawConn, error) {
	if ctx.sock.raw != nil {
		return ctx.sock.raw, nil
	}
	if ctx.conn == nil {
		return nil, ErrContextConnNil
	}
	raw, err := ctx.conn.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("wl: the socket's raw connection: %w", err)
	}
	ctx.sock.raw = raw
	return raw, nil
}

func (ctx *Context) socketSend() (int, error) {
	if ctx.conn == nil {
		return 0, ErrContextConnNil
	}
	n, _, err := ctx.conn.WriteMsgUnix(ctx.out, ctx.outOOB, nil)
	if err != nil {
		return n, fmt.Errorf("wl: sendmsg of %d bytes: %w", len(ctx.out), err)
	}
	return n, nil
}

func (ctx *Context) socketRecv(b, control []byte) (n, oobn, flags int, err error) {
	if ctx.conn == nil {
		return 0, 0, 0, errors.New("wl: read of a closed socket")
	}
	n, oobn, flags, _, err = ctx.conn.ReadMsgUnix(b, control)
	return n, oobn, flags, err
}
