package dbus

// ExportedObjects counts the paths conn's default handler holds an
// object at, for the tests outside the package.
func ExportedObjects(conn *Conn) int {
	h := conn.handler.(*defaultHandler)
	h.RLock()
	defer h.RUnlock()
	return len(h.objects)
}
