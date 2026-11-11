package dbus

import "sync"

// ownGoroutines counts the goroutines a connection has running its own
// code (lys-lib patch): the read loop, the watcher of its context, the
// watcher of each pending call's context, each method call being
// dispatched and replied to, and the loop of each ordered lane. Close
// waits for them, so that nothing of a connection still runs once Close
// has returned; upstream's Close returned with all of them winding
// down, and a program that counted its goroutines, or exited, raced
// them.
//
// A goroutine is not counted while it runs the application's code: an
// exported method, an interceptor, a signal handler that is not one of
// this package's. That code may take as long as it likes and may itself
// close the connection, which must neither wait for it nor for itself.
type ownGoroutines struct {
	mu      sync.Mutex
	stopped sync.Cond
	running int
}

func (g *ownGoroutines) enter() {
	g.mu.Lock()
	g.running++
	g.mu.Unlock()
}

func (g *ownGoroutines) leave() {
	g.mu.Lock()
	g.running--
	if g.running == 0 {
		g.stopped.Broadcast()
	}
	g.mu.Unlock()
}

// start runs f on a goroutine of the connection's own.
func (g *ownGoroutines) start(f func()) {
	g.enter()
	go func() {
		defer g.leave()
		f()
	}()
}

// outside runs f, the application's code, on a goroutine of the
// connection's own, which is not counted meanwhile.
func (g *ownGoroutines) outside(f func()) {
	g.leave()
	defer g.enter()
	f()
}

func (g *ownGoroutines) wait() {
	g.mu.Lock()
	for g.running > 0 {
		g.stopped.Wait()
	}
	g.mu.Unlock()
}

// Close closes the connection and waits for the goroutines it runs its
// own code on. Any blocked operations will return with errors and the
// channels passed to Eavesdrop and Signal are closed. An exported
// method still running is not waited for: its goroutine ends when the
// method returns, and it may call Close itself. This method must not be
// called on shared connections.
func (conn *Conn) Close() error {
	err := conn.close()
	conn.own.wait()
	return err
}

// deliverSignal hands a signal to the connection's signal handler, as
// the application's code when the handler is not one of this package's.
func (conn *Conn) deliverSignal(iface, member string, signal *Signal) {
	switch conn.signalHandler.(type) {
	case *defaultSignalHandler, *sequentialSignalHandler:
		conn.signalHandler.DeliverSignal(iface, member, signal)
	default:
		conn.own.outside(func() { conn.signalHandler.DeliverSignal(iface, member, signal) })
	}
}
