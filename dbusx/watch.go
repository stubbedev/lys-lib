package dbusx

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// ChangedProperties is what PropertiesChanged carries: the interface
// the properties belong to, those that changed with their new values,
// and those that changed without one.
type ChangedProperties struct {
	Interface   string                  `dbus:"interface_name"`
	Changed     map[string]dbus.Variant `dbus:"changed_properties"`
	Invalidated []string                `dbus:"invalidated_properties"`
}

// PropertiesChanged is the signal an object sends when properties of
// one of its interfaces change.
var PropertiesChanged = NewSignal[ChangedProperties](PropertiesInterface, "PropertiesChanged")

// ErrDisconnected is what Watch.Stop reports when the watch ended
// because its connection closed.
var ErrDisconnected = errors.New("the bus connection closed")

// watchTimeout bounds setting a watch up, and taking its match rules
// off the bus again once it has ended.
const watchTimeout = 5 * time.Second

const signalBuffer = 32

// Sender is the peer whose signals a watch delivers. A signal anyone
// else sends in its name is dropped.
type Sender struct {
	name  string
	owned bool
}

// FromBus is the message bus itself, which alone sends as
// org.freedesktop.DBus.
func FromBus() Sender { return Sender{name: bus} }

// FromOwner is the connection that owns the well-known name when the
// watch starts: the watch asks the bus who that is, and a name nobody
// owns fails it.
func FromOwner(name string) Sender { return Sender{name: name, owned: true} }

func (s Sender) resolve(ctx context.Context, conn *dbus.Conn) (string, error) {
	if !s.owned {
		return s.name, nil
	}
	return NameOwner(ctx, conn, s.name)
}

// Route is one signal of a watch: which signal, from which object, and
// the handler it is decoded for.
type Route struct {
	path    dbus.ObjectPath
	iface   string
	member  string
	name    string
	arg0    string
	byArg0  bool
	deliver func(*dbus.Signal) error
}

// On routes the signal s, sent from the object at path, to handle. An
// error handle returns is reported as the watch reports a signal it
// could not decode.
func On[T any](s Signal[T], path dbus.ObjectPath, handle func(T) error) Route {
	return Route{path: path, iface: s.iface, member: s.member, name: s.Name(), deliver: func(sig *dbus.Signal) error {
		v, err := s.Decode(sig)
		if err != nil {
			return errors.Join(err, sig.CloseFDs())
		}
		return handle(v)
	}}
}

// WithArg0 narrows the route to signals whose first argument is the
// string value: the name a NameOwnerChanged is about, the interface of
// a PropertiesChanged.
func (r Route) WithArg0(value string) Route {
	r.arg0, r.byArg0 = value, true
	return r
}

func (r *Route) takes(sig *dbus.Signal) bool {
	if sig.Path != r.path || sig.Name != r.name {
		return false
	}
	if !r.byArg0 || len(sig.Body) == 0 {
		return true
	}
	first, isString := sig.Body[0].(string)
	return !isString || first == r.arg0
}

func (r *Route) match(sender string, wholeInterface bool) []dbus.MatchOption {
	match := []dbus.MatchOption{dbus.WithMatchSender(sender), dbus.WithMatchObjectPath(r.path), dbus.WithMatchInterface(r.iface)}
	if wholeInterface {
		return match
	}
	match = append(match, dbus.WithMatchMember(r.member))
	if r.byArg0 {
		match = append(match, dbus.WithMatchArg(0, r.arg0))
	}
	return match
}

// matches are the match rules that bring routes' signals from sender:
// one per signal, except that the signals of one interface of one
// object share a rule for that whole interface.
func matches(sender string, routes []Route) [][]dbus.MatchOption {
	out := make([][]dbus.MatchOption, 0, len(routes))
	for i := range routes {
		r := &routes[i]
		sameInterface := func(o Route) bool { return o.path == r.path && o.iface == r.iface }
		if slices.IndexFunc(routes, sameInterface) != i {
			continue
		}
		shared := slices.ContainsFunc(routes[i+1:], sameInterface)
		out = append(out, r.match(sender, shared))
	}
	return out
}

// Dispatch hands sig to the route it belongs to, decoded, when sender
// sent it. It takes over the descriptors sig carries, which are the
// caller's own (each signal channel of a connection is handed
// duplicates of its own): a handler that gets the signal owns them,
// and a signal no handler gets, because it is of no route, another
// peer sent it or it does not decode, has them closed. The error is
// the handler's, or says why the signal was not decoded or a
// descriptor not closed.
func Dispatch(sig *dbus.Signal, sender string, routes []Route) error {
	if sig.Sender != sender {
		return sig.CloseFDs()
	}
	for i := range routes {
		if r := &routes[i]; r.takes(sig) {
			return r.deliver(sig)
		}
	}
	return sig.CloseFDs()
}

// closeUnread closes the descriptors of the signals a watch that has
// ended was sent and never read. Nothing is sent on signals any more.
func closeUnread(signals <-chan *dbus.Signal) error {
	var errs []error
	for {
		select {
		case sig, open := <-signals:
			if !open {
				return errors.Join(errs...)
			}
			errs = append(errs, sig.CloseFDs())
		default:
			return errors.Join(errs...)
		}
	}
}

// Watch follows signals on a connection until its context ends, it is
// stopped, or the connection closes.
type Watch struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// WatchSignals runs each route's handler, on the watch's goroutine and
// in arrival order, for the signals the routes name, when from sent
// them. failed is told, on the same goroutine, of each such signal a
// handler did not accept or that did not decode. A signal carrying
// descriptors reaches each watch of a connection with descriptors of
// its own: a handler that gets the signal owns them, and the watch
// closes those of every signal no handler gets and of the signals
// still unread when it ends. The watch holds match rules and a signal
// channel on conn until it ends, and then removes them, leaving a
// connection shared with others as it was.
func WatchSignals(ctx context.Context, conn *dbus.Conn, from Sender, failed func(error), routes ...Route) (*Watch, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("watch %s: no signal to watch", from.name)
	}
	setup, cancelSetup := context.WithTimeout(ctx, watchTimeout)
	defer cancelSetup()
	sender, err := from.resolve(setup, conn)
	if err != nil {
		return nil, fmt.Errorf("watch %s: %w", from.name, err)
	}
	rules := matches(from.name, routes)
	for i, rule := range rules {
		if err := conn.AddMatchSignalContext(setup, rule...); err != nil {
			return nil, errors.Join(fmt.Errorf("watch %s: %w", from.name, err), removeMatches(ctx, conn, rules[:i]))
		}
	}
	signals := make(chan *dbus.Signal, signalBuffer)
	conn.Signal(signals)
	watching, cancel := context.WithCancel(ctx)
	w := &Watch{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer func() {
			conn.RemoveSignal(signals)
			if err := errors.Join(closeUnread(signals), removeMatches(ctx, conn, rules)); err != nil {
				w.err = errors.Join(w.err, fmt.Errorf("stop watching %s: %w", from.name, err))
			}
		}()
		for {
			select {
			case <-watching.Done():
				return
			case sig, ok := <-signals:
				if !ok {
					w.err = fmt.Errorf("watch %s: %w", from.name, ErrDisconnected)
					return
				}
				if err := Dispatch(sig, sender, routes); err != nil {
					failed(err)
				}
			}
		}
	}()
	return w, nil
}

func removeMatches(ctx context.Context, conn *dbus.Conn, rules [][]dbus.MatchOption) error {
	removing, cancel := context.WithTimeout(context.WithoutCancel(ctx), watchTimeout)
	defer cancel()
	var errs []error
	for _, rule := range rules {
		if err := conn.RemoveMatchSignalContext(removing, rule...); err != nil && !errors.Is(err, dbus.ErrClosed) && conn.Connected() {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Done is closed once the watch has ended and removed what it held.
func (w *Watch) Done() <-chan struct{} { return w.done }

// Stop ends the watch and returns once it has removed what it held:
// with ErrDisconnected when its connection had closed, with the error
// removing its match rules met, or nil. Calling it again, or after the
// watch's context ended, waits for the same end and reports the same
// error. Stop must not be called from a handler of the watch.
func (w *Watch) Stop() error {
	w.cancel()
	<-w.done
	return w.err
}
