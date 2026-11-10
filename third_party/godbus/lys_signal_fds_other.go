//go:build !unix

package dbus

import "errors"

var errNoFDPassing = errors.New("dbus: descriptors are not passed on this platform")

// dupFD is -1: no transport of this platform passes descriptors, so a
// body holds none to duplicate.
func dupFD(UnixFD) UnixFD { return -1 }

func closeFD(UnixFD) error { return errNoFDPassing }
