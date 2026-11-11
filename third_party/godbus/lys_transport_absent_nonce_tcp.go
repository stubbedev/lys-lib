//go:build windows || godbus_no_tcp

package dbus

// lys-lib patch (conn.go, transportFor): this build has no nonce-tcp
// transport.
var newNonceTcpTransport func(string) (transport, error)
