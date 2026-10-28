package dbusx

import (
	"fmt"
	"reflect"
	"strings"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/third_party/godbus/introspect"
)

// Signal is one signal of a D-Bus interface, carrying a T: a struct
// whose exported fields, except those tagged `dbus:"-"`, are the
// signal's arguments in order. A field's dbus tag names its argument
// in the introspection data; an untagged field's argument is unnamed.
// The same Signal declares it on an Interface, emits it and decodes
// it, so its name and signature exist once.
type Signal[T any] struct {
	iface, member string
	fields        []int
	args          []introspect.Arg
}

// NewSignal declares the signal member of iface carrying a T. It
// panics when T is not a struct or a field has no D-Bus type, as a
// declaration that cannot exist.
func NewSignal[T any](iface, member string) Signal[T] {
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("dbusx: signal %s.%s carries %s, which is not a struct", iface, member, t))
	}
	s := Signal[T]{iface: iface, member: member}
	for i := range t.NumField() {
		f := t.Field(i)
		name := f.Tag.Get("dbus")
		if !f.IsExported() || name == "-" {
			continue
		}
		s.fields = append(s.fields, i)
		s.args = append(s.args, introspect.Arg{Name: name, Type: dbus.SignatureOfType(f.Type).String()})
	}
	return s
}

// Name is the signal's full name, interface and member, as a received
// dbus.Signal carries it.
func (s Signal[T]) Name() string { return s.iface + "." + s.member }

// Emit sends the signal from the object at path on conn, carrying v.
func (s Signal[T]) Emit(conn *dbus.Conn, path dbus.ObjectPath, v T) error {
	rv := reflect.ValueOf(v)
	body := make([]any, len(s.fields))
	for i, f := range s.fields {
		body[i] = rv.Field(f).Interface()
	}
	if err := conn.Emit(path, s.Name(), body...); err != nil {
		return fmt.Errorf("emit %s at %s: %w", s.Name(), path, err)
	}
	return nil
}

// Decode reads a received signal as this one, refusing another signal
// and a body that does not carry exactly its arguments. Arguments
// holding a struct arrive untyped, so within them it accepts what
// dbus.Store converts.
func (s Signal[T]) Decode(sig *dbus.Signal) (T, error) {
	var v T
	if sig.Name != s.Name() {
		return v, fmt.Errorf("decode %s: the signal is %s", s.Name(), sig.Name)
	}
	if len(sig.Body) != len(s.args) {
		return v, fmt.Errorf("decode %s: %d arguments, want %d (%s)", s.Name(), len(sig.Body), len(s.args), s.signature())
	}
	rv := reflect.ValueOf(&v).Elem()
	dest := make([]any, len(s.fields))
	for i, arg := range s.args {
		if !strings.ContainsRune(arg.Type, '(') {
			if got := dbus.SignatureOf(sig.Body[i]).String(); got != arg.Type {
				return v, fmt.Errorf("decode %s: argument %d is %s, want %s", s.Name(), i, got, arg.Type)
			}
		}
		dest[i] = rv.Field(s.fields[i]).Addr().Interface()
	}
	if err := dbus.Store(sig.Body, dest...); err != nil {
		return v, fmt.Errorf("decode %s as (%s): %w", s.Name(), s.signature(), err)
	}
	return v, nil
}

func (s Signal[T]) signature() string {
	var b strings.Builder
	for _, arg := range s.args {
		b.WriteString(arg.Type)
	}
	return b.String()
}

func (s Signal[T]) declaration() (iface string, sig introspect.Signal) {
	return s.iface, introspect.Signal{Name: s.member, Args: s.args}
}

// SignalDecl is a Signal as an Interface declares it, whatever it
// carries.
type SignalDecl interface {
	declaration() (iface string, sig introspect.Signal)
}
