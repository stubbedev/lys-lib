//go:build !windows && !solaris && !js

package dbus

import (
	"errors"
	"fmt"
	"syscall"
)

// ReadMessage reads one message and settles the descriptors that came
// with it (lys-lib patch): those the body names are the receiver's,
// and every other one is closed, as are all of them when the message
// is not delivered. Upstream closed none of these, so a peer could fill
// the receiver's descriptor table with messages that named no
// descriptor, were malformed, or declared none.
func (t *unixTransport) ReadMessage() (*Message, error) {
	msg, err := t.readMessage()
	if err != nil {
		closeReceivedFDs(t.rdr.oob)
		return nil, err
	}
	return msg, nil
}

// receivedFDs are the descriptors in oob, the control data read with a
// message whose header declares declared of them. A message that came
// with another number is invalid: upstream indexed what it had received
// by what the header promised, and panicked when there were fewer.
func receivedFDs(oob []byte, declared uint32) ([]int, error) {
	if len(oob) == 0 && declared == 0 {
		return nil, nil
	}
	scms, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, err
	}
	if len(scms) > 1 {
		return nil, errors.New("dbus: received more than one socket control message")
	}
	var fds []int
	if len(scms) == 1 {
		if fds, err = syscall.ParseUnixRights(&scms[0]); err != nil {
			return nil, err
		}
	}
	if uint64(len(fds)) != uint64(declared) {
		return nil, InvalidMessageError(fmt.Sprintf("message declares %d unix fds and came with %d", declared, len(fds)))
	}
	return fds, nil
}

// closeReceivedFDs closes every descriptor in oob, the control data
// read with a message that is not delivered.
func closeReceivedFDs(oob []byte) {
	scms, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}
	for i := range scms {
		fds, err := syscall.ParseUnixRights(&scms[i])
		if err != nil {
			continue
		}
		for _, fd := range fds {
			_ = syscall.Close(fd)
		}
	}
}
