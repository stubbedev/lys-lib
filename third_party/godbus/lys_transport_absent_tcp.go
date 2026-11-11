//go:build godbus_no_tcp

package dbus

// lys-lib patch (conn.go, transportFor): this build has no tcp
// transport.
var newTcpTransport func(string) (transport, error)
