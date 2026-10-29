// Package dbusx holds the D-Bus plumbing every lys process shares, on
// lys-lib's patched godbus: exporting interfaces at a path and under a
// well-known name the way zbus's object_server().at + request_name
// does, serving zbus-style #[zbus(property)] getters (computed on every
// read) and setters over org.freedesktop.DBus.Properties, introspection
// built on demand, and watching a name for its owner.
package dbusx

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/third_party/godbus/introspect"
)

// Error names of the standard org.freedesktop.DBus errors (zbus's
// fdo::Error variants).
const (
	ErrFailed          = "org.freedesktop.DBus.Error.Failed"
	ErrInvalidArgs     = "org.freedesktop.DBus.Error.InvalidArgs"
	ErrUnknownProperty = "org.freedesktop.DBus.Error.UnknownProperty"
	ErrUnknownIface    = "org.freedesktop.DBus.Error.UnknownInterface"
	ErrPropertyRO      = "org.freedesktop.DBus.Error.PropertyReadOnly"
)

// ErrNameTaken is what Own and Serve wrap when another connection
// already owns the well-known name, so a caller can tell a taken name
// from a bus failure.
var ErrNameTaken = errors.New("name already owned")

// The interfaces Export serves itself at each path, for all of the
// path's interfaces.
const (
	propertiesIface     = "org.freedesktop.DBus.Properties"
	introspectableIface = "org.freedesktop.DBus.Introspectable"
)

// Failed is fdo::Error::Failed(msg).
func Failed(msg string) *dbus.Error { return dbus.NewError(ErrFailed, []any{msg}) }

// InvalidArgs is fdo::Error::InvalidArgs(msg).
func InvalidArgs(msg string) *dbus.Error { return dbus.NewError(ErrInvalidArgs, []any{msg}) }

// Getters maps property names to their read functions. Each returns a
// value godbus can marshal; the D-Bus signature is taken from it.
type Getters map[string]func() any

// Setters maps writable property names to their write functions, which
// decode the variant themselves and refuse a wrong type.
type Setters map[string]func(dbus.Variant) *dbus.Error

// properties is org.freedesktop.DBus.Properties for the interfaces of
// one object.
type properties struct {
	ifaces  map[string]Getters
	setters map[string]Setters
}

func (p *properties) of(iface string) (Getters, *dbus.Error) {
	getters, ok := p.ifaces[iface]
	if !ok {
		return nil, dbus.NewError(ErrUnknownIface, []any{"Unknown interface '" + iface + "'"})
	}
	return getters, nil
}

// Get returns one property.
func (p *properties) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	getters, derr := p.of(iface)
	if derr != nil {
		return dbus.Variant{}, derr
	}
	get, ok := getters[name]
	if !ok {
		return dbus.Variant{}, dbus.NewError(ErrUnknownProperty, []any{"Unknown property '" + name + "'"})
	}
	return dbus.MakeVariant(get()), nil
}

// GetAll returns every property.
func (p *properties) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	getters, derr := p.of(iface)
	if derr != nil {
		return nil, derr
	}
	out := make(map[string]dbus.Variant, len(getters))
	for name, get := range getters {
		out[name] = dbus.MakeVariant(get())
	}
	return out, nil
}

// Set writes a property that has a setter; the rest are read-only.
func (p *properties) Set(iface, name string, value dbus.Variant) *dbus.Error {
	getters, derr := p.of(iface)
	if derr != nil {
		return derr
	}
	if _, ok := getters[name]; !ok {
		return dbus.NewError(ErrUnknownProperty, []any{"Unknown property '" + name + "'"})
	}
	set, ok := p.setters[iface][name]
	if !ok {
		return dbus.NewError(ErrPropertyRO, []any{"Property '" + name + "' is read-only"})
	}
	return set(value)
}

// Service is one exported interface.
type Service struct {
	// Name is the well-known bus name and Path the object path.
	Name string
	Path dbus.ObjectPath
	// Interface is the D-Bus interface the methods are exported as.
	Interface string
	// Methods is the object whose exported methods (each ending in a
	// *dbus.Error result) form the interface.
	Methods any
	// Properties are the read-only properties.
	Properties Getters
	// Signals are the signals the interface emits.
	Signals []SignalDecl
}

// Interface is one D-Bus interface of an object: its methods (an
// object whose exported methods each end in a *dbus.Error result), its
// properties, the writers of those that are writable, and the signals
// it emits.
type Interface struct {
	Name       string
	Methods    any
	Properties Getters
	Setters    Setters
	Signals    []SignalDecl
	// Ordered are the methods whose calls run one at a time in arrival
	// order (dbus.Conn.DispatchInOrder), for a caller that does not wait
	// for each reply.
	Ordered []string
}

// Export puts ifaces at path on conn, with one Properties object serving
// all of their properties and introspection listing them all, as zbus's
// object_server().at does for each interface it mounts at a path. The
// returned unexport removes them again (a Request or Session object
// ending). Export is all or nothing: every interface is checked before
// any is exported, and a failure leaves none of them at path.
func Export(conn *dbus.Conn, path dbus.ObjectPath, ifaces ...Interface) (unexport func(), err error) {
	objects, err := objectsAt(path, ifaces)
	if err != nil {
		return nil, err
	}
	for i, o := range objects {
		if err := conn.Export(o.methods, path, o.iface); err != nil {
			unexportAll(conn, path, objects[:i])
			return nil, fmt.Errorf("export %s at %s: %w", o.iface, path, err)
		}
	}
	for _, iface := range ifaces {
		if len(iface.Ordered) > 0 {
			conn.DispatchInOrder(iface.Name, iface.Ordered...)
		}
	}
	return func() { unexportAll(conn, path, objects) }, nil
}

// exported is one interface's methods as Export hands them to the
// connection.
type exported struct {
	iface   string
	methods any
}

// objectsAt checks ifaces and builds what Export puts at path: each
// interface's methods, then the Properties and Introspectable objects
// that answer for all of them.
func objectsAt(path dbus.ObjectPath, ifaces []Interface) ([]exported, error) {
	props := &properties{ifaces: make(map[string]Getters, len(ifaces)), setters: make(map[string]Setters, len(ifaces))}
	node := introspect.Node{Name: string(path), Interfaces: []introspect.Interface{
		introspect.IntrospectData,
		{Name: propertiesIface, Methods: introspect.Methods(props)},
	}}
	objects := make([]exported, 0, len(ifaces)+2)
	for _, iface := range ifaces {
		if iface.Name == propertiesIface || iface.Name == introspectableIface {
			return nil, fmt.Errorf("export %s at %s: Export serves that interface itself", iface.Name, path)
		}
		if _, dup := props.ifaces[iface.Name]; dup {
			return nil, fmt.Errorf("export %s at %s: the interface is given twice", iface.Name, path)
		}
		signals, err := introspectSignals(iface)
		if err != nil {
			return nil, err
		}
		for name := range iface.Setters {
			if _, ok := iface.Properties[name]; !ok {
				return nil, fmt.Errorf("export %s: setter for %s, which has no getter", iface.Name, name)
			}
		}
		props.ifaces[iface.Name] = iface.Properties
		props.setters[iface.Name] = iface.Setters
		node.Interfaces = append(node.Interfaces, introspect.Interface{Name: iface.Name, Methods: introspect.Methods(iface.Methods), Signals: signals})
		objects = append(objects, exported{iface: iface.Name, methods: iface.Methods})
	}
	return append(objects,
		exported{iface: propertiesIface, methods: props},
		exported{iface: introspectableIface, methods: lazyIntrospectable{node: node, ifaces: ifaces}},
	), nil
}

// unexportAll takes objects off path again.
func unexportAll(conn *dbus.Conn, path dbus.ObjectPath, objects []exported) {
	for _, o := range objects {
		_ = conn.Export(nil, path, o.iface)
	}
}

// Serve exports s on conn and takes its well-known name; the returned
// release drops the name and unexports s. A name already owned is
// ErrNameTaken, as zbus's request_name with DoNotQueue, and leaves
// nothing exported.
func Serve(conn *dbus.Conn, s Service) (func(), error) {
	unexport, err := Export(conn, s.Path, Interface{Name: s.Interface, Methods: s.Methods, Properties: s.Properties, Signals: s.Signals})
	if err != nil {
		return nil, err
	}
	release, err := Own(conn, s.Name)
	if err != nil {
		unexport()
		return nil, err
	}
	return func() {
		release()
		unexport()
	}, nil
}

// Own takes a well-known name; the returned release drops it. A name
// already owned is ErrNameTaken, as zbus's request_name with
// DoNotQueue.
func Own(conn *dbus.Conn, name string) (func(), error) {
	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", name, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("request %s: %w", name, ErrNameTaken)
	}
	return func() { _, _ = conn.ReleaseName(name) }, nil
}

// lazyIntrospectable builds the introspection data per call: a
// property's type comes from reading it, which must not happen while the
// exporter may hold the lock its getters take.
type lazyIntrospectable struct {
	node   introspect.Node
	ifaces []Interface
}

func (l lazyIntrospectable) Introspect() (string, *dbus.Error) {
	node := l.node
	node.Interfaces = slices.Clone(l.node.Interfaces)
	for i, iface := range l.ifaces {
		node.Interfaces[2+i].Properties = introspectProps(iface.Properties, iface.Setters)
	}
	return string(introspect.NewIntrospectable(&node)), nil
}

func introspectSignals(iface Interface) ([]introspect.Signal, error) {
	out := make([]introspect.Signal, 0, len(iface.Signals))
	for _, decl := range iface.Signals {
		owner, sig := decl.declaration()
		if owner != iface.Name {
			return nil, fmt.Errorf("export %s: signal %s.%s belongs to another interface", iface.Name, owner, sig.Name)
		}
		out = append(out, sig)
	}
	return out, nil
}

func introspectProps(getters Getters, setters Setters) []introspect.Property {
	names := make([]string, 0, len(getters))
	for name := range getters {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]introspect.Property, 0, len(names))
	for _, name := range names {
		access := "read"
		if _, ok := setters[name]; ok {
			access = "readwrite"
		}
		out = append(out, introspect.Property{
			Name:   name,
			Type:   dbus.SignatureOf(getters[name]()).String(),
			Access: access,
		})
	}
	return out
}

// File wraps a descriptor received over D-Bus; a negative one is an
// error, not a file.
func File(fd dbus.UnixFD, name string) (*os.File, error) {
	if fd < 0 {
		return nil, fmt.Errorf("%s: invalid file descriptor %d", name, fd)
	}
	return os.NewFile(uintptr(fd), name), nil
}
