package dbusx

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/third_party/godbus/introspect"
)

// Signal is one signal of a D-Bus interface, carrying a T: a struct
// whose exported fields, except those tagged `dbus:"-"`, are the
// signal's arguments in order. A field's dbus tag names its argument
// in the introspection data; an untagged field's argument is unnamed.
// The same Signal declares it on an Interface, emits it and decodes
// it, so its name and signature exist once.
type Signal[T any] struct{ iface, member string }

// NewSignal declares the signal member of iface carrying a T. The
// declaration costs nothing, so a package-level one runs no code when
// its package is imported; the arguments are read off T when the
// signal is first declared on an Interface, emitted or decoded, which
// panics when T is not a struct or a field has no D-Bus type, as a
// declaration that cannot exist.
func NewSignal[T any](iface, member string) Signal[T] {
	return Signal[T]{iface: iface, member: member}
}

// arguments are a signal's arguments as its T declares them: the
// fields that carry them, what the introspection data says of each,
// and their signature.
type arguments struct {
	fields    []int
	args      []introspect.Arg
	signature string
}

var argumentsOf sync.Map

func (s Signal[T]) arguments() *arguments {
	t := reflect.TypeFor[T]()
	if cached, ok := argumentsOf.Load(t); ok {
		if a, ok := cached.(*arguments); ok {
			return a
		}
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("dbusx: signal %s carries %s, which is not a struct", s.Name(), t))
	}
	a := &arguments{}
	var signature strings.Builder
	for i := range t.NumField() {
		f := t.Field(i)
		name := f.Tag.Get("dbus")
		if !f.IsExported() || name == "-" {
			continue
		}
		argType := dbus.SignatureOfType(f.Type).String()
		a.fields = append(a.fields, i)
		a.args = append(a.args, introspect.Arg{Name: name, Type: argType})
		signature.WriteString(argType)
	}
	a.signature = signature.String()
	argumentsOf.Store(t, a)
	return a
}

// Name is the signal's full name, interface and member, as a received
// dbus.Signal carries it.
func (s Signal[T]) Name() string { return s.iface + "." + s.member }

func (s Signal[T]) named(name string) bool {
	iface, member, ok := strings.CutLast(name, ".")
	return ok && iface == s.iface && member == s.member
}

// Emit sends the signal from the object at path on conn, carrying v.
func (s Signal[T]) Emit(conn *dbus.Conn, path dbus.ObjectPath, v T) error {
	fields := s.arguments().fields
	rv := reflect.ValueOf(v)
	body := make([]any, len(fields))
	for i, f := range fields {
		body[i] = rv.Field(f).Interface()
	}
	if err := conn.Emit(path, s.Name(), body...); err != nil {
		return fmt.Errorf("emit %s at %s: %w", s.Name(), path, err)
	}
	return nil
}

// Decode reads a received signal as this one, refusing another signal
// and a body that does not start with its arguments. Arguments after
// them are not read: an interface grows by appending arguments, and a
// newer peer's signal is still the one declared, so a T without
// arguments decodes whatever the signal carries. The descriptors in
// those further arguments stay the signal's (dbus.Signal.CloseFDs); a
// watch's On route closes them. Arguments holding a struct arrive
// untyped, so within them Decode accepts what dbus.Store converts.
func (s Signal[T]) Decode(sig *dbus.Signal) (T, error) {
	var v T
	if !s.named(sig.Name) {
		return v, fmt.Errorf("decode %s: the signal is %s", s.Name(), sig.Name)
	}
	a := s.arguments()
	if len(sig.Body) < len(a.args) {
		return v, fmt.Errorf("decode %s: %d arguments, want the %d of (%s)", s.Name(), len(sig.Body), len(a.args), a.signature)
	}
	rv := reflect.ValueOf(&v).Elem()
	dest := make([]any, len(a.fields))
	for i, arg := range a.args {
		if !strings.ContainsRune(arg.Type, '(') {
			if got := dbus.SignatureOf(sig.Body[i]).String(); got != arg.Type {
				return v, fmt.Errorf("decode %s: argument %d is %s, want %s", s.Name(), i, got, arg.Type)
			}
		}
		dest[i] = rv.Field(a.fields[i]).Addr().Interface()
	}
	if err := dbus.Store(sig.Body[:len(a.args)], dest...); err != nil {
		return v, fmt.Errorf("decode %s as (%s): %w", s.Name(), a.signature, err)
	}
	return v, nil
}

// closeUndeclared closes the descriptors in the arguments sig, which
// decoded as this signal, carries after the declared ones: no T holds
// them.
func (s Signal[T]) closeUndeclared(sig *dbus.Signal) error {
	return dbus.CloseFDs(sig.Body[len(s.arguments().args):])
}

func (s Signal[T]) declaration() (iface string, sig introspect.Signal) {
	return s.iface, introspect.Signal{Name: s.member, Args: s.arguments().args}
}

// SignalDecl is a Signal as an Interface declares it, whatever it
// carries.
type SignalDecl interface {
	declaration() (iface string, sig introspect.Signal)
}
