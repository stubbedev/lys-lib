package portalsettings

import (
	"errors"
	"slices"
	"sync"

	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// The per-setting machinery every tracked portal key shares:
// one current value, one listener list, one update path. The monitor's
// single mutex covers all settings; update runs the listeners outside
// it, in registration order, skipping tombstoned slots.

// setting is one tracked portal setting's state.
type setting[T comparable] struct {
	current   T
	listeners []func(T)
}

// get returns the current value; the caller routes through the
// monitor's lock.
func (s *setting[T]) get(m *Monitor) T {
	m.mu.Lock()
	defer m.mu.Unlock()
	return s.current
}

// on registers fn and returns its unregister function; the slot, not
// the closure, is the identity (function values are not comparable).
func (s *setting[T]) on(m *Monitor, fn func(T)) (off func()) {
	if fn == nil {
		return func() {}
	}
	m.mu.Lock()
	s.listeners = append(s.listeners, fn)
	i := len(s.listeners) - 1
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if i < len(s.listeners) {
				s.listeners[i] = nil
			}
		})
	}
}

// update stores v and, when deliver is set and the value actually
// moved, runs the listeners - on the monitor goroutine, serialized,
// without holding the lock (a listener may read or register back).
// Duplicate values are dropped: the portal can re-announce a setting
// nobody changed.
func (s *setting[T]) update(m *Monitor, v T, deliver bool) {
	m.mu.Lock()
	changed := v != s.current
	s.current = v
	var fns []func(T)
	if deliver && changed && !m.closed {
		fns = slices.Clone(s.listeners)
	}
	m.mu.Unlock()
	for _, fn := range fns {
		if fn != nil {
			fn(v)
		}
	}
}

// readAllSettings reads every tracked namespace with one ReadAll. ok is
// false when the portal gave no answer at all, which says nothing about
// the preferences; a refusal or an unreadable reply reads as no keys.
func readAllSettings(conn *dbus.Conn) (values map[string]map[string]dbus.Variant, ok bool) {
	body, err := call(conn.Object(portalName, portalPath), readTimeout,
		readAll, []string{schemeNamespace, interfaceNamespace})
	switch {
	case err != nil && !portalAnswered(err):
		return nil, false
	case err != nil:
		return nil, true
	}
	if dbus.Store(body, &values) != nil {
		return nil, true
	}
	return values, true
}

// portalAnswered reports whether err is the portal's own reply rather
// than the bus or the connection failing to reach a portal ready to read.
func portalAnswered(err error) bool {
	var reply dbus.Error
	if !errors.As(err, &reply) {
		return false
	}
	switch reply.Name {
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.NoReply", "org.freedesktop.DBus.Error.Timeout", "org.freedesktop.DBus.Error.Disconnected",
		"org.freedesktop.DBus.Error.UnknownObject", "org.freedesktop.DBus.Error.UnknownInterface", "org.freedesktop.DBus.Error.UnknownMethod":
		return false
	}
	return true
}

// refreshSetting moves one setting to the value ReadAll returned for
// it, zero when the portal returned none.
func refreshSetting[T comparable](m *Monitor, s *setting[T], values map[string]map[string]dbus.Variant, namespace, key string, mapFn func(any) T, zero T, deliver bool) {
	v := zero
	if raw, ok := values[namespace][key]; ok {
		v = mapFn(raw)
	}
	s.update(m, v, deliver)
}

// settingChange is what SettingChanged carries: the namespace and key
// of a setting and its new value.
type settingChange struct {
	Namespace string       `dbus:"namespace"`
	Key       string       `dbus:"key"`
	Value     dbus.Variant `dbus:"value"`
}

var settingChanged = dbusx.NewSignal[settingChange](portalIface, "SettingChanged")

// applyChange moves one setting to the value a SettingChanged carries
// when it is the one for that setting's namespace and key.
func applyChange[T comparable](m *Monitor, s *setting[T], c settingChange, namespace, key string, mapFn func(any) T) {
	if c.Namespace == namespace && c.Key == key {
		s.update(m, mapFn(c.Value), true)
	}
}

// portalOwned is the bounded name-ownership probe every refresh starts
// with: no portal means Unknown-shaped zeros, deliberately without
// on-demand activation (a startup that may sit 25s waiting for dbus
// activation is worse than Unknown).
func portalOwned(conn *dbus.Conn) bool {
	body, err := call(conn.BusObject(), probeTimeout,
		"org.freedesktop.DBus.NameHasOwner", portalName)
	if err != nil || len(body) != 1 {
		return false
	}
	owned, _ := body[0].(bool)
	return owned
}
