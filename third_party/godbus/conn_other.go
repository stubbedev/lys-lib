//go:build !darwin

package dbus

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path"
	"strings"
)

// ErrNoSessionBus reports that no session bus was found. Upstream runs
// dbus-launch here, which forks a daemon nothing ever stops and joins the
// caller to a bus no other program is on.
var ErrNoSessionBus = errors.New("dbus: no session bus: DBUS_SESSION_BUS_ADDRESS is unset and none was found in the runtime directory")

func getSessionBusPlatformAddress() (string, error) {
	return "", ErrNoSessionBus
}

// tryDiscoverDbusSessionBusAddress tries to discover an existing dbus session
// and return the value of its DBUS_SESSION_BUS_ADDRESS.
// It tries different techniques employed by different operating systems,
// returning the first valid address it finds, or an empty string.
//
//   - /run/user/<uid>/bus           if this exists, it *is* the bus socket. present on
//     Ubuntu 18.04
//   - /run/user/<uid>/dbus-session: if this exists, it can be parsed for the bus
//     address. present on Ubuntu 16.04
//
// See https://dbus.freedesktop.org/doc/dbus-launch.1.html
func tryDiscoverDbusSessionBusAddress() string {
	if runtimeDirectory, err := getRuntimeDirectory(); err == nil {

		if runUserBusFile := path.Join(runtimeDirectory, "bus"); fileExists(runUserBusFile) {
			// if /run/user/<uid>/bus exists, that file itself
			// *is* the unix socket, so return its path
			return fmt.Sprintf("unix:path=%s", EscapeBusAddressValue(runUserBusFile))
		}
		if runUserSessionDbusFile := path.Join(runtimeDirectory, "dbus-session"); fileExists(runUserSessionDbusFile) {
			// if /run/user/<uid>/dbus-session exists, it's a
			// text file // containing the address of the socket, e.g.:
			// DBUS_SESSION_BUS_ADDRESS=unix:abstract=/tmp/dbus-E1c73yNqrG

			if f, err := os.ReadFile(runUserSessionDbusFile); err == nil {
				if addr, ok := strings.CutPrefix(string(f), "DBUS_SESSION_BUS_ADDRESS="); ok {
					return strings.TrimRight(addr, "\n\r")
				}
			}
		}
	}
	return ""
}

// getRuntimeDirectory is $XDG_RUNTIME_DIR when it names an absolute
// path (lys-lib patch), as libdbus and sd-bus look for the session bus
// at $XDG_RUNTIME_DIR/bus; upstream's /run/user/<uid> is only the guess
// for a process whose environment names none.
func getRuntimeDirectory() (string, error) {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); path.IsAbs(dir) {
		return dir, nil
	}
	if currentUser, err := user.Current(); err != nil {
		return "", err
	} else {
		return fmt.Sprintf("/run/user/%s", currentUser.Uid), nil
	}
}

func fileExists(filename string) bool {
	_, err := os.Stat(filename)
	return !os.IsNotExist(err)
}
