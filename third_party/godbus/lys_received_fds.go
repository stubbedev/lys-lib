package dbus

import (
	"fmt"
	"io"
	"reflect"
	"runtime"
)

// setFDs hands the decoder the descriptors that came with the message
// it is about to decode, none of them referenced yet (lys-lib patch).
// With descriptors every 'h' decodes to the UnixFD it indexes, at any
// depth; without any it stays the UnixFDIndex the wire carries.
//
// Upstream resolved an 'h' as it met one and built the containers
// around it for UnixFDIndex, so a dict or a nested array of
// descriptors (a{sh}, a{sah}, aah) panicked in reflect, which killed
// the read loop and the process: any peer could send one.
func (dec *decoder) setFDs(fds []int) {
	dec.fds = fds
	dec.taken = append(dec.taken[:0], make([]bool, len(fds))...)
	dec.dups = dec.dups[:0]
}

// typeFor is the type a value of signature s decodes to.
func (dec *decoder) typeFor(s string) reflect.Type {
	if len(dec.fds) == 0 {
		return typeFor(s)
	}
	return typeWithFDs(s, reflect.TypeFor[UnixFD]())
}

// takeFD is the descriptor an 'h' of value idx stands for. The first
// reference to a descriptor takes the one received and each later one
// a duplicate of it, so that every UnixFD of a decoded body is closed
// once: a body may name one index twice, which upstream decoded to one
// descriptor held twice, and its receiver closed whatever had been
// opened under that number in between. A duplicate the process has no
// room for is -1. An index past the descriptors is an invalid message.
func (dec *decoder) takeFD(idx uint32) UnixFD {
	if uint64(idx) >= uint64(len(dec.fds)) {
		panic(InvalidMessageError("invalid index for unix fd"))
	}
	fd := UnixFD(dec.fds[idx])
	if !dec.taken[idx] {
		dec.taken[idx] = true
		return fd
	}
	dup := dupFD(fd)
	if dup >= 0 {
		dec.dups = append(dec.dups, dup)
	}
	return dup
}

// closeDups closes the duplicates made for a body that then failed to
// decode: nothing holds them. The descriptors that were received stay
// with whoever handed them to the decoder.
func (dec *decoder) closeDups() {
	for _, fd := range dec.dups {
		_ = closeFD(fd)
	}
	dec.dups = dec.dups[:0]
}

// closeUnreferencedFDs closes the received descriptors the decoded
// body did not take, for a decoder that owns them (the unix
// transport's): upstream left them open, so a peer attaching
// descriptors its message never named filled the receiver's table.
func (dec *decoder) closeUnreferencedFDs() {
	for i, fd := range dec.fds {
		if !dec.taken[i] {
			_ = closeFD(UnixFD(fd))
		}
	}
}

// decodeFailure is the error Decode returns for v, the panic that
// ended it on a value of signature sig. The decoder's own errors and
// those of its reader pass as they are. Anything else is a defect of
// the decoder met on a message a peer chose: an InvalidMessageError,
// for which the read loop drops the message and keeps the connection,
// where upstream let the panic take the process down.
func decodeFailure(sig Signature, v any) error {
	err, isError := v.(error)
	if !isError {
		return InvalidMessageError(fmt.Sprintf("decoding %q panicked: %v", sig.str, v))
	}
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return FormatError("unexpected EOF")
	}
	switch err.(type) {
	case runtime.Error, *reflect.ValueError:
		return InvalidMessageError(fmt.Sprintf("decoding %q panicked: %v", sig.str, err))
	}
	return err
}

// headerError is err, the failure to decode a message's header, as one
// the read loop ends the connection on rather than skips the message
// for: the body is unread, so the message boundary is lost.
func headerError(err error) error {
	if invalid, ok := err.(InvalidMessageError); ok {
		return FormatError("header: " + string(invalid))
	}
	return err
}

// firstString is the string a message body starts with.
func firstString(body []any) (string, bool) {
	if len(body) == 0 {
		return "", false
	}
	s, ok := body[0].(string)
	return s, ok
}

// closeFDs closes the descriptors of a received message nothing takes:
// a reply nobody waits for, a call no method is run for, a message for
// a name the connection does not own.
func (msg *Message) closeFDs() {
	if n, _ := msg.Headers[FieldUnixFDs].value.(uint32); n == 0 {
		return
	}
	_ = CloseFDs(msg.Body)
}
