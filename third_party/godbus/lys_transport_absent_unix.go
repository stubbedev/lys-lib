//go:build windows || solaris || js

package dbus

// lys-lib patch (conn.go, transportFor): this build has no unix
// transport.
var newUnixTransport func(string) (transport, error)
