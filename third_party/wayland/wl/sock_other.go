//go:build !linux

package wl

import (
	"fmt"
	"syscall"
	"time"
)

type socketIO struct{ raw syscall.RawConn }

func (ctx *Context) openSocket() error {
	raw, err := ctx.conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("wl: the socket's raw connection: %w", err)
	}
	ctx.sock.raw = raw
	return nil
}

func (ctx *Context) setReadDeadline(t time.Time) error { return ctx.conn.SetReadDeadline(t) }

func (ctx *Context) closeSocket() error { return nil }

func (ctx *Context) socketSend() (int, error) {
	n, _, err := ctx.conn.WriteMsgUnix(ctx.out, ctx.outOOB, nil)
	if err != nil {
		return n, fmt.Errorf("wl: sendmsg of %d bytes: %w", len(ctx.out), ctx.closedOr(err))
	}
	return n, nil
}

func (ctx *Context) socketRecv(b, control []byte) (n, oobn, flags int, err error) {
	n, oobn, flags, _, err = ctx.conn.ReadMsgUnix(b, control)
	return n, oobn, flags, ctx.closedOr(err)
}
