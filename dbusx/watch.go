package dbusx

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// ChangedProperties is what PropertiesChanged carries: the interface
// the properties belong to, those that changed with their new values,
// and those that changed without one. A route lends it to its handler:
// the descriptors in Changed are closed once the handler returns, so a
// handler that keeps one duplicates it.
type ChangedProperties struct {
	Interface   string                  `dbus:"interface_name"`
	Changed     map[string]dbus.Variant `dbus:"changed_properties"`
	Invalidated []string                `dbus:"invalidated_properties"`
}

func (c ChangedProperties) closeLent() error {
	return dbus.CloseFDs([]any{c.Changed})
}

type lent interface{ closeLent() error }

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

// FromOwner is whichever connection owns the well-known name as each
// signal arrives. The watch follows the name from one owner to the
// next, so a service that restarts keeps being heard, and a name nobody
// owns yet does not fail it: its signals are delivered from the moment
// it gains an owner.
func FromOwner(name string) Sender { return Sender{name: name, owned: true} }

// current is the sender's unique name now, "" for a name nobody owns,
// and the sequence of the bus's answer saying so: an owner change the
// bus announced before it is already in the answer.
func (s Sender) current(ctx context.Context, conn *dbus.Conn) (unique string, asOf dbus.Sequence, err error) {
	if !s.owned {
		return s.name, dbus.NoSequence, nil
	}
	unique, asOf, err = nameOwner(ctx, conn, s.name)
	if derr, ok := errors.AsType[dbus.Error](err); ok && derr.Name == errNameHasNoOwner {
		return "", asOf, nil
	}
	return unique, asOf, err
}

// Route is one signal of a watch: which signal, from which objects, and
// the handler it is decoded for. One made by OnOthers is every signal
// of an interface instead, and one made by OnNameOwnerChanged,
// OnOwnerChange or OnNameLost is the bus's whoever the watch's sender
// is.
type Route struct {
	objects objects
	iface   string
	member  string
	name    string
	arg0    arg0Filter
	fromBus bool
	deliver func(*dbus.Signal) error
}

type objects struct {
	path  dbus.ObjectPath
	below bool
}

func (o objects) has(path dbus.ObjectPath) bool {
	switch {
	case path == o.path:
		return true
	case !o.below:
		return false
	case o.path == "/":
		return true
	}
	return within(string(path), string(o.path), '/')
}

func within(name, namespace string, separator byte) bool {
	return len(name) > len(namespace) && name[len(namespace)] == separator && strings.HasPrefix(name, namespace)
}

func (o objects) match() dbus.MatchOption {
	if o.below {
		return dbus.WithMatchPathNamespace(o.path)
	}
	return dbus.WithMatchObjectPath(o.path)
}

type arg0Kind uint8

const (
	anyArg0 arg0Kind = iota
	arg0Is
	arg0Within
)

type arg0Filter struct {
	kind  arg0Kind
	value string
}

func (f arg0Filter) admits(arg string) bool {
	switch f.kind {
	case arg0Is:
		return arg == f.value
	case arg0Within:
		return arg == f.value || within(arg, f.value, '.')
	}
	return true
}

func (f arg0Filter) narrow(match []dbus.MatchOption) []dbus.MatchOption {
	switch f.kind {
	case arg0Is:
		return append(match, dbus.WithMatchArg(0, f.value))
	case arg0Within:
		return append(match, dbus.WithMatchArg0Namespace(f.value))
	}
	return match
}

// On routes the signal s, sent from the object at path, to handle,
// which also gets the signal's sequence on its connection: every
// message a connection reads has one, in the order it was read, so a
// signal sequenced before a call's reply (dbus.Call.ResponseSequence)
// was sent before the peer answered. An error handle returns is
// reported as the watch reports a signal it could not decode. The
// signal may carry arguments after those s declares (Signal.Decode);
// their descriptors are closed, as are those of a ChangedProperties
// once handle returns.
func On[T any](s Signal[T], path dbus.ObjectPath, handle func(T, dbus.Sequence) error) Route {
	return on(s, objects{path: path}, func(_ dbus.ObjectPath, v T, sent dbus.Sequence) error {
		return handle(v, sent)
	})
}

// OnUnder is On for every object in the path namespace: the object at
// namespace and every object below it, as the D-Bus match rule's
// path_namespace says. handle also gets the path of the object that
// sent the signal.
func OnUnder[T any](s Signal[T], namespace dbus.ObjectPath, handle func(dbus.ObjectPath, T, dbus.Sequence) error) Route {
	return on(s, objects{path: namespace, below: true}, handle)
}

func on[T any](s Signal[T], at objects, handle func(dbus.ObjectPath, T, dbus.Sequence) error) Route {
	return Route{objects: at, iface: s.iface, member: s.member, name: s.Name(), deliver: func(sig *dbus.Signal) error {
		v, err := s.Decode(sig)
		if err != nil {
			return errors.Join(err, sig.CloseFDs())
		}
		err = errors.Join(handle(sig.Path, v, sig.Sequence), s.closeUndeclared(sig))
		if l, ok := any(v).(lent); ok {
			err = errors.Join(err, l.closeLent())
		}
		return err
	}}
}

// OnOthers routes every signal of iface, sent from the object at path,
// that no route before it takes to handle, as it arrived: handle owns
// the descriptors it carries. A watch that must understand everything
// its peer says about an object lists it last and fails on what
// reaches it.
func OnOthers(iface string, path dbus.ObjectPath, handle func(*dbus.Signal) error) Route {
	return Route{objects: objects{path: path}, iface: iface, deliver: handle}
}

// WithArg0 narrows the route to signals whose first argument is the
// string value: the name a NameOwnerChanged is about, the interface of
// a PropertiesChanged.
func (r Route) WithArg0(value string) Route {
	r.arg0 = arg0Filter{kind: arg0Is, value: value}
	return r
}

// WithArg0Namespace narrows the route to signals whose first argument
// is the string namespace or a name in it, as the D-Bus match rule's
// arg0namespace says: org.mpris.MediaPlayer2 takes
// org.mpris.MediaPlayer2 and org.mpris.MediaPlayer2.vlc, not
// org.mpris.MediaPlayer2x.
func (r Route) WithArg0Namespace(namespace string) Route {
	r.arg0 = arg0Filter{kind: arg0Within, value: namespace}
	return r
}

func (r *Route) sentBy(sender string) string {
	if r.fromBus {
		return bus
	}
	return sender
}

func (r *Route) takes(sig *dbus.Signal) bool {
	if !r.objects.has(sig.Path) {
		return false
	}
	if r.member != "" {
		if sig.Name != r.name {
			return false
		}
	} else if iface, _, ok := strings.CutLast(sig.Name, "."); !ok || iface != r.iface {
		return false
	}
	if len(sig.Body) == 0 {
		return true
	}
	first, isString := sig.Body[0].(string)
	return !isString || r.arg0.admits(first)
}

func matches(sender string, routes []Route) [][]dbus.MatchOption {
	out := make([][]dbus.MatchOption, 0, len(routes))
	for i := range routes {
		r := &routes[i]
		sameInterface := func(o Route) bool { return o.fromBus == r.fromBus && o.objects == r.objects && o.iface == r.iface }
		if slices.IndexFunc(routes, sameInterface) != i {
			continue
		}
		member, arg0 := r.member, r.arg0
		for _, o := range routes[i+1:] {
			if !sameInterface(o) {
				continue
			}
			if o.member != member {
				member = ""
			}
			if o.arg0 != arg0 {
				arg0 = arg0Filter{}
			}
		}
		match := []dbus.MatchOption{dbus.WithMatchSender(r.sentBy(sender)), r.objects.match(), dbus.WithMatchInterface(r.iface)}
		if member != "" {
			match = append(match, dbus.WithMatchMember(member))
		}
		out = append(out, arg0.narrow(match))
	}
	return out
}

// Dispatch hands sig to the route it belongs to, decoded, when sender,
// a unique name or the bus's, sent it; no signal is from the sender "".
// A route for a name's owner changing takes the bus's signal whoever
// sender is.
// It takes over the descriptors sig carries, which are the
// caller's own (each signal channel of a connection is handed
// duplicates of its own): a handler that gets the signal owns them,
// and a signal no handler gets, because it is of no route, another
// peer sent it or it does not decode, has them closed. The error is
// the handler's, or says why the signal was not decoded or a
// descriptor not closed.
func Dispatch(sig *dbus.Signal, sender string, routes []Route) error {
	for i := range routes {
		r := &routes[i]
		if from := r.sentBy(sender); from != "" && sig.Sender == from && r.takes(sig) {
			return r.deliver(sig)
		}
	}
	return sig.CloseFDs()
}

// RemoveSignal takes signals off conn, as conn.RemoveSignal does, and
// closes the descriptors of the signals it was sent and nobody read:
// every signal channel owns those of what it is handed. Nothing is sent
// on signals once it returns.
func RemoveSignal(conn *dbus.Conn, signals chan *dbus.Signal) error {
	conn.RemoveSignal(signals)
	var errs []error
	for {
		select {
		case sig, open := <-signals:
			if !open {
				return errors.Join(errs...)
			}
			if err := sig.CloseFDs(); err != nil {
				errs = append(errs, fmt.Errorf("discard %s from %s: %w", sig.Name, sig.Sender, err))
			}
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
	rules := matches(from.name, routes)
	ownerRouted := func(r Route) bool { return r.fromBus && r.arg0.admits(from.name) }
	if from.owned && !slices.ContainsFunc(routes, ownerRouted) {
		rules = append(rules, OwnerMatch(from.name))
	}
	signals := make(chan *dbus.Signal, signalBuffer)
	conn.Signal(signals)
	abandon := func(err error, added [][]dbus.MatchOption) error {
		return errors.Join(fmt.Errorf("watch %s: %w", from.name, err), RemoveSignal(conn, signals), removeMatches(ctx, conn, added))
	}
	for i, rule := range rules {
		if err := conn.AddMatchSignalContext(setup, rule...); err != nil {
			return nil, abandon(err, rules[:i])
		}
	}
	sender, asOf, err := from.current(setup, conn)
	if err != nil {
		return nil, abandon(err, rules)
	}
	watching, cancel := context.WithCancel(ctx)
	w := &Watch{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer func() {
			if err := errors.Join(RemoveSignal(conn, signals), removeMatches(ctx, conn, rules)); err != nil {
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
				if from.owned {
					if _, owner, changed := NameOwnerChange(sig, from.name); changed {
						if sig.Sequence <= asOf {
							continue
						}
						sender = owner
					}
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
