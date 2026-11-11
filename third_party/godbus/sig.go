package dbus

import (
	"fmt"
	"reflect"
	"strings"
)

// basicTypeOf is the type a basic type's signature byte decodes to
// (lys-lib patch: a switch, where upstream built a map when the package
// was imported).
func basicTypeOf(sig byte) (reflect.Type, bool) {
	switch sig {
	case 'y':
		return reflect.TypeFor[byte](), true
	case 'b':
		return reflect.TypeFor[bool](), true
	case 'n':
		return reflect.TypeFor[int16](), true
	case 'q':
		return reflect.TypeFor[uint16](), true
	case 'i':
		return reflect.TypeFor[int32](), true
	case 'u':
		return reflect.TypeFor[uint32](), true
	case 'x':
		return reflect.TypeFor[int64](), true
	case 't':
		return reflect.TypeFor[uint64](), true
	case 'd':
		return reflect.TypeFor[float64](), true
	case 's':
		return reflect.TypeFor[string](), true
	case 'g':
		return reflect.TypeFor[Signature](), true
	case 'o':
		return reflect.TypeFor[ObjectPath](), true
	case 'v':
		return reflect.TypeFor[Variant](), true
	case 'h':
		return reflect.TypeFor[UnixFDIndex](), true
	}
	return nil, false
}

// Signature represents a correct type signature as specified by the D-Bus
// specification. The zero value represents the empty signature, "".
type Signature struct {
	str string
}

// SignatureOf returns the concatenation of all the signatures of the given
// values. It panics if one of them is not representable in D-Bus.
func SignatureOf(vs ...any) Signature {
	var s strings.Builder
	for _, v := range vs {
		s.WriteString(getSignature(reflect.TypeOf(v), depthCounter{}))
	}
	return Signature{s.String()}
}

// SignatureOfType returns the signature of the given type. It panics if the
// type is not representable in D-Bus.
func SignatureOfType(t reflect.Type) Signature {
	return Signature{getSignature(t, depthCounter{})}
}

// getSignature returns the signature of the given type and panics on unknown types.
func getSignature(t reflect.Type, depth depthCounter) (sig string) {
	if !depth.Valid() {
		panic("container nesting too deep")
	}
	defer func() {
		if len(sig) > 255 {
			panic("signature exceeds the length limitation")
		}
	}()
	if t == fileType() {
		return "h" // lys-lib patch (lys_file_reply.go)
	}
	// handle simple types first
	switch t.Kind() {
	case reflect.Uint8:
		return "y"
	case reflect.Bool:
		return "b"
	case reflect.Int16:
		return "n"
	case reflect.Uint16:
		return "q"
	case reflect.Int, reflect.Int32:
		if t == reflect.TypeFor[UnixFD]() {
			return "h"
		}
		return "i"
	case reflect.Uint, reflect.Uint32:
		if t == reflect.TypeFor[UnixFDIndex]() {
			return "h"
		}
		return "u"
	case reflect.Int64:
		return "x"
	case reflect.Uint64:
		return "t"
	case reflect.Float64:
		return "d"
	case reflect.Pointer:
		return getSignature(t.Elem(), depth)
	case reflect.String:
		if t == reflect.TypeFor[ObjectPath]() {
			return "o"
		}
		return "s"
	case reflect.Struct:
		switch t {
		case reflect.TypeFor[Variant]():
			return "v"
		case reflect.TypeFor[Signature]():
			return "g"
		}
		var s strings.Builder
		for field := range t.Fields() {
			if field.PkgPath == "" && field.Tag.Get("dbus") != "-" {
				s.WriteString(getSignature(field.Type, depth.EnterStruct()))
			}
		}
		if len(s.String()) == 0 {
			panic(InvalidTypeError{t})
		}
		return "(" + s.String() + ")"
	case reflect.Array, reflect.Slice:
		return "a" + getSignature(t.Elem(), depth.EnterArray())
	case reflect.Map:
		if !isKeyType(t.Key()) {
			panic(InvalidTypeError{t})
		}
		return "a{" + getSignature(t.Key(), depth.EnterArray().EnterDictEntry()) + getSignature(t.Elem(), depth.EnterArray().EnterDictEntry()) + "}"
	case reflect.Interface:
		return "v"
	}
	panic(InvalidTypeError{t})
}

// ParseSignature returns the signature represented by this string, or a
// SignatureError if the string is not a valid signature.
func ParseSignature(s string) (sig Signature, err error) {
	if len(s) == 0 {
		return
	}
	if len(s) > 255 {
		return Signature{""}, SignatureError{s, "too long"}
	}
	sig.str = s
	for err == nil && len(s) != 0 {
		err, s = validSingle(s, depthCounter{})
	}
	if err != nil {
		sig = Signature{""}
	}

	return
}

// ParseSignatureMust behaves like ParseSignature, except that it panics if s
// is not valid.
func ParseSignatureMust(s string) Signature {
	sig, err := ParseSignature(s)
	if err != nil {
		panic(err)
	}
	return sig
}

// Empty returns whether the signature is the empty signature.
func (s Signature) Empty() bool {
	return s.str == ""
}

// Single returns whether the signature represents a single, complete type.
func (s Signature) Single() bool {
	err, r := validSingle(s.str, depthCounter{})
	return err != nil && r == ""
}

// String returns the signature's string representation.
func (s Signature) String() string {
	return s.str
}

// A SignatureError indicates that a signature passed to a function or received
// on a connection is not a valid signature.
type SignatureError struct {
	Sig    string
	Reason string
}

func (e SignatureError) Error() string {
	return fmt.Sprintf("dbus: invalid signature: %q (%s)", e.Sig, e.Reason)
}

type depthCounter struct {
	arrayDepth, structDepth, dictEntryDepth int
}

func (cnt depthCounter) Valid() bool {
	return cnt.arrayDepth <= 32 && cnt.structDepth <= 32 && cnt.dictEntryDepth <= 32
}

func (cnt depthCounter) EnterArray() depthCounter {
	cnt.arrayDepth++
	return cnt
}

func (cnt depthCounter) EnterStruct() depthCounter {
	cnt.structDepth++
	return cnt
}

func (cnt depthCounter) EnterDictEntry() depthCounter {
	cnt.dictEntryDepth++
	return cnt
}

// Try to read a single type from this string. If it was successful, err is nil
// and rem is the remaining unparsed part. Otherwise, err is a non-nil
// SignatureError and rem is "". depth is the current recursion depth which may
// not be greater than 64 and should be given as 0 on the first call.
func validSingle(s string, depth depthCounter) (err error, rem string) { //nolint:staticcheck // Ignore "ST1008: error should be returned as the last argument".
	if s == "" {
		return SignatureError{Sig: s, Reason: "empty signature"}, ""
	}
	if !depth.Valid() {
		return SignatureError{Sig: s, Reason: "container nesting too deep"}, ""
	}
	switch s[0] {
	case 'y', 'b', 'n', 'q', 'i', 'u', 'x', 't', 'd', 's', 'g', 'o', 'v', 'h':
		return nil, s[1:]
	case 'a':
		if len(s) > 1 && s[1] == '{' {
			i := findMatching(s[1:], '{', '}')
			if i == -1 {
				return SignatureError{Sig: s, Reason: "unmatched '{'"}, ""
			}
			i++
			rem = s[i+1:]
			s = s[2:i]
			if len(s) == 0 {
				return SignatureError{Sig: s, Reason: "empty dict"}, ""
			}
			// lys-lib patch: a key is a basic type. Upstream took a
			// variant for one too, a type nothing can then encode,
			// name the signature of or hash.
			if !strings.Contains(basicTypes, s[:1]) {
				return SignatureError{Sig: s, Reason: "dict entry key is not a basic type"}, ""
			}
			err, nr := validSingle(s[1:], depth.EnterArray().EnterDictEntry())
			if err != nil {
				return err, ""
			}
			if nr != "" {
				return SignatureError{Sig: s, Reason: "too many types in dict"}, ""
			}
			return nil, rem
		}
		return validSingle(s[1:], depth.EnterArray())
	case '(':
		i := findMatching(s, '(', ')')
		if i == -1 {
			return SignatureError{Sig: s, Reason: "unmatched ')'"}, ""
		}
		rem = s[i+1:]
		s = s[1:i]
		if s == "" {
			// lys-lib patch: the specification allows no empty struct,
			// and an array of them never ends, as none takes a byte.
			return SignatureError{Sig: "()", Reason: "empty struct"}, ""
		}
		for err == nil && s != "" {
			err, s = validSingle(s, depth.EnterStruct())
		}
		if err != nil {
			rem = ""
		}
		return
	}
	return SignatureError{Sig: s, Reason: "invalid type character"}, ""
}

func findMatching(s string, left, right rune) int {
	n := 0
	for i, v := range s {
		switch v {
		case left:
			n++
		case right:
			n--
		}
		if n == 0 {
			return i
		}
	}
	return -1
}

// typeFor returns the type of the given signature. It ignores any left over
// characters and panics if s doesn't start with a valid type signature.
func typeFor(s string) (t reflect.Type) {
	return typeWithFDs(s, reflect.TypeFor[UnixFDIndex]())
}

// basicTypes are the types a dict entry's key can be.
const basicTypes = "ybnqiuxtdsogh"

// typeWithFDs is typeFor with fd as the type of an 'h', at any depth
// (lys-lib patch): UnixFDIndex as the wire carries it, UnixFD once a
// decoder holding the message's descriptors has resolved it.
func typeWithFDs(s string, fd reflect.Type) (t reflect.Type) {
	err, _ := validSingle(s, depthCounter{})
	if err != nil {
		panic(err)
	}

	if s[0] == 'h' {
		return fd
	}
	if t, ok := basicTypeOf(s[0]); ok {
		return t
	}
	switch s[0] {
	case 'a':
		if s[1] == '{' {
			i := strings.LastIndex(s, "}")
			t = reflect.MapOf(typeWithFDs(s[2:3], fd), typeWithFDs(s[3:i], fd))
		} else {
			t = reflect.SliceOf(typeWithFDs(s[1:], fd))
		}
	case '(':
		t = reflect.TypeFor[[]any]()
	}
	return
}
