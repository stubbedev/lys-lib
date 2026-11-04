package wl

import (
	"errors"
	"fmt"
	"io"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

type socketIO struct {
	raw      syscall.RawConn
	sendFn   func(uintptr)
	recvFn   func(uintptr) bool
	sendHdr  unix.Msghdr
	sendIov  unix.Iovec
	sendN    int
	sendErr  error
	recvHdr  unix.Msghdr
	recvIov  unix.Iovec
	recvN    int
	recvOOB  int
	recvFlag int
	recvErr  error
}

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
	ctx.sock.sendFn = ctx.sendOnce
	ctx.sock.recvFn = ctx.recvOnce
	return raw, nil
}

func (ctx *Context) sendOnce(fd uintptr) {
	s := &ctx.sock
	s.sendHdr = unix.Msghdr{Iov: &s.sendIov, Iovlen: 1}
	s.sendIov.Base = unsafe.SliceData(ctx.out)
	s.sendIov.SetLen(len(ctx.out))
	if len(ctx.outOOB) > 0 {
		s.sendHdr.Control = unsafe.SliceData(ctx.outOOB)
		s.sendHdr.SetControllen(len(ctx.outOOB))
	}
	for {
		n, _, errno := unix.Syscall(unix.SYS_SENDMSG, fd, uintptr(unsafe.Pointer(&s.sendHdr)), unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		if errno == unix.EINTR {
			continue
		}
		s.sendN, s.sendErr = int(n), nil
		if errno != 0 {
			s.sendN, s.sendErr = 0, errno
		}
		return
	}
}

// socketSend makes one non-blocking try at writing out and outOOB: how many
// bytes went, the fds with the first of them. A full socket takes none.
func (ctx *Context) socketSend() (int, error) {
	raw, err := ctx.rawConn()
	if err != nil {
		return 0, err
	}
	if err := raw.Control(ctx.sock.sendFn); err != nil {
		return 0, fmt.Errorf("wl: sendmsg of %d bytes: %w", len(ctx.out), err)
	}
	if errors.Is(ctx.sock.sendErr, unix.EAGAIN) {
		return 0, nil
	}
	if ctx.sock.sendErr != nil {
		return 0, fmt.Errorf("wl: sendmsg of %d bytes: %w", len(ctx.out), ctx.sock.sendErr)
	}
	return ctx.sock.sendN, nil
}

func (ctx *Context) recvOnce(fd uintptr) bool {
	s := &ctx.sock
	for {
		n, _, errno := unix.Syscall(unix.SYS_RECVMSG, fd, uintptr(unsafe.Pointer(&s.recvHdr)), unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
		switch errno {
		case unix.EINTR:
			continue
		case unix.EAGAIN:
			return false
		case 0:
			s.recvN, s.recvOOB, s.recvFlag, s.recvErr = int(n), int(s.recvHdr.Controllen), int(s.recvHdr.Flags), nil
			return true
		}
		s.recvN, s.recvOOB, s.recvFlag, s.recvErr = 0, 0, 0, errno
		return true
	}
}

// socketRecv reads into b and control, waiting in Go's poller under the read
// deadline, without the address net.UnixConn's ReadMsgUnix allocates.
func (ctx *Context) socketRecv(b, control []byte) (n, oobn, flags int, err error) {
	raw, err := ctx.rawConn()
	if err != nil {
		return 0, 0, 0, err
	}
	s := &ctx.sock
	s.recvHdr = unix.Msghdr{Iov: &s.recvIov, Iovlen: 1, Control: unsafe.SliceData(control)}
	s.recvHdr.SetControllen(len(control))
	s.recvIov.Base = unsafe.SliceData(b)
	s.recvIov.SetLen(len(b))
	if err := raw.Read(s.recvFn); err != nil {
		return 0, 0, 0, err
	}
	if s.recvErr != nil {
		return 0, 0, 0, s.recvErr
	}
	if s.recvN == 0 && s.recvOOB == 0 {
		return 0, 0, 0, io.EOF
	}
	return s.recvN, s.recvOOB, s.recvFlag, nil
}
