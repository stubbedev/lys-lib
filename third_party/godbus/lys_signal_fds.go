package dbus

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// CloseFDs closes every descriptor a received message body carries, at
// any depth, for a receiver that does not take them over (lys-lib
// patch). A descriptor that does not close is in the error.
func CloseFDs(body []any) error {
	var errs []error
	for _, v := range body {
		mapFDs(v, func(fd UnixFD) UnixFD {
			if fd < 0 {
				return fd
			}
			if err := closeFD(fd); err != nil {
				errs = append(errs, fmt.Errorf("close descriptor %d: %w", fd, err))
			}
			return fd
		})
	}
	return errors.Join(errs...)
}

// CloseFDs closes every descriptor the signal carries, for a receiver
// that does not take them over (lys-lib patch): each channel a signal
// is delivered to owns the descriptors in what it receives.
func (s *Signal) CloseFDs() error {
	if s.bare {
		return nil
	}
	return CloseFDs(s.Body)
}

// forReceiver is the signal as receiver i of n is handed it: the
// signal itself for the last, which takes the descriptors that came
// off the wire, and a copy holding duplicates of them for each of the
// others. A descriptor that cannot be duplicated reaches its receiver
// as -1.
func (s *Signal) forReceiver(i, n int) *Signal {
	if s.bare || i == n-1 {
		return s
	}
	c := *s
	c.Body = make([]any, len(s.Body))
	for j, v := range s.Body {
		c.Body[j], _ = mapFDs(v, dupFD)
	}
	return &c
}

// mapFDs runs f on every descriptor in v, a value of a decoded message
// body, and returns v with each descriptor replaced by what f returned,
// and whether any changed. v itself is never written to: what holds a
// changed descriptor is copied.
func mapFDs(v any, f func(UnixFD) UnixFD) (any, bool) {
	switch x := v.(type) {
	case UnixFD:
		if n := f(x); n != x {
			return n, true
		}
		return v, false
	case []UnixFD:
		var out []UnixFD
		for i, fd := range x {
			n := f(fd)
			if n == fd {
				continue
			}
			if out == nil {
				out = slices.Clone(x)
			}
			out[i] = n
		}
		if out == nil {
			return v, false
		}
		return out, true
	case []any:
		var out []any
		for i, e := range x {
			n, changed := mapFDs(e, f)
			if !changed {
				continue
			}
			if out == nil {
				out = slices.Clone(x)
			}
			out[i] = n
		}
		if out == nil {
			return v, false
		}
		return out, true
	case Variant:
		n, changed := mapVariantFDs(x, f)
		if !changed {
			return v, false
		}
		return n, true
	case map[string]Variant:
		var out map[string]Variant
		for k, e := range x {
			n, changed := mapVariantFDs(e, f)
			if !changed {
				continue
			}
			if out == nil {
				out = maps.Clone(x)
			}
			out[k] = n
		}
		if out == nil {
			return v, false
		}
		return out, true
	}
	t := reflect.TypeOf(v)
	if t == nil || !holdsFDs(t) {
		return v, false
	}
	rv := reflect.ValueOf(v)
	var out reflect.Value
	switch t.Kind() {
	case reflect.Slice:
		for i := range rv.Len() {
			n, changed := mapFDs(rv.Index(i).Interface(), f)
			if !changed {
				continue
			}
			if !out.IsValid() {
				out = reflect.MakeSlice(t, rv.Len(), rv.Len())
				reflect.Copy(out, rv)
			}
			out.Index(i).Set(reflect.ValueOf(n))
		}
	case reflect.Map:
		for it := rv.MapRange(); it.Next(); {
			k, kChanged := mapFDs(it.Key().Interface(), f)
			e, eChanged := mapFDs(it.Value().Interface(), f)
			if !kChanged && !eChanged {
				continue
			}
			if !out.IsValid() {
				out = reflect.MakeMapWithSize(t, rv.Len())
				for all := rv.MapRange(); all.Next(); {
					out.SetMapIndex(all.Key(), all.Value())
				}
			}
			out.SetMapIndex(it.Key(), reflect.Value{})
			out.SetMapIndex(reflect.ValueOf(k), reflect.ValueOf(e))
		}
	}
	if !out.IsValid() {
		return v, false
	}
	return out.Interface(), true
}

// mapVariantFDs is mapFDs for a variant, whose signature says whether
// it can hold a descriptor at all.
func mapVariantFDs(x Variant, f func(UnixFD) UnixFD) (Variant, bool) {
	if !strings.ContainsAny(x.sig.str, "hv") {
		return x, false
	}
	n, changed := mapFDs(x.value, f)
	if !changed {
		return x, false
	}
	return Variant{sig: x.sig, value: n}, true
}

// holdsFDs reports whether a decoded value of type t can hold a
// descriptor: it is one, or a variant, or a container of either.
func holdsFDs(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Slice:
		return holdsFDs(t.Elem())
	case reflect.Map:
		return holdsFDs(t.Key()) || holdsFDs(t.Elem())
	case reflect.Interface:
		return true
	}
	return t == unixFDType || t == variantType
}
