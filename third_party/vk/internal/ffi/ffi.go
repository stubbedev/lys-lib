// Package ffi is the slice of goffi's API the Vulkan bindings call
// through, carried out by github.com/ebitengine/purego: purego and
// goffi each link a runtime shim (fakecgo) and cannot share a program,
// and purego is the one lys-ui's applications already use to load
// native libraries.
//
// Every Vulkan argument and result is an integer, a handle or a
// pointer, passed in integer registers. Float arguments (only a few
// rarely used commands, such as vkCmdSetLineWidth, take any) would
// travel in float registers, which purego.SyscallN does not fill, so
// a call with one panics rather than pass garbage.
package ffi

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
)

// CallingConvention is goffi's; only the platform default exists here.
type CallingConvention int

// DefaultCall is the platform's C calling convention.
const DefaultCall CallingConvention = 0

// TypeKind is a C type's category.
type TypeKind int

// The kinds an argument or result can have.
const (
	VoidType TypeKind = iota
	UInt32Type
	SInt32Type
	UInt64Type
	PointerType
	FloatType
)

// TypeDescriptor describes a C type.
type TypeDescriptor struct{ Kind TypeKind }

// The C types the bindings use.
var (
	VoidTypeDescriptor    = &TypeDescriptor{Kind: VoidType}
	UInt32TypeDescriptor  = &TypeDescriptor{Kind: UInt32Type}
	SInt32TypeDescriptor  = &TypeDescriptor{Kind: SInt32Type}
	UInt64TypeDescriptor  = &TypeDescriptor{Kind: UInt64Type}
	PointerTypeDescriptor = &TypeDescriptor{Kind: PointerType}
	FloatTypeDescriptor   = &TypeDescriptor{Kind: FloatType}
)

// CallInterface is a prepared signature.
type CallInterface struct {
	ret  TypeKind
	args []TypeKind
}

// PrepareCallInterface records a function's result and argument types.
func PrepareCallInterface(cif *CallInterface, _ CallingConvention, ret *TypeDescriptor, args []*TypeDescriptor) error {
	if cif == nil || ret == nil {
		return fmt.Errorf("ffi: a call interface needs a target and a result type")
	}
	cif.ret, cif.args = ret.Kind, make([]TypeKind, len(args))
	for i, a := range args {
		if a == nil {
			return fmt.Errorf("ffi: argument %d has no type", i)
		}
		cif.args[i] = a.Kind
	}
	return nil
}

// CallFunction calls fn with the arguments avalue points at, each read
// as its prepared type, and stores the result where rvalue points
// (nil for a void function). The errno result is always zero. The C
// side gets the arguments as integers, which keep nothing alive, so
// avalue, and through it every structure an argument points at, stays
// reachable until the call returns: otherwise a collection during a
// long call (vkCreateInstance) frees what the driver is reading, and
// another goroutine's allocation overwrites it.
func CallFunction(cif *CallInterface, fn unsafe.Pointer, rvalue unsafe.Pointer, avalue []unsafe.Pointer) (uintptr, error) {
	if len(avalue) != len(cif.args) {
		return 0, fmt.Errorf("ffi: %d arguments for a signature of %d", len(avalue), len(cif.args))
	}
	var args [16]uintptr
	for i, kind := range cif.args {
		switch kind {
		case UInt32Type:
			args[i] = uintptr(*(*uint32)(avalue[i]))
		case SInt32Type:
			args[i] = uintptr(uint32(*(*int32)(avalue[i])))
		case UInt64Type:
			args[i] = *(*uintptr)(avalue[i])
		case PointerType:
			p := *(*unsafe.Pointer)(avalue[i])
			onHeap(uintptr(p))
			args[i] = uintptr(p)
		default:
			panic(fmt.Sprintf("ffi: argument %d of kind %d cannot pass through purego.SyscallN", i, kind))
		}
	}
	r, _, _ := purego.SyscallN(uintptr(fn), args[:len(cif.args)]...)
	runtime.KeepAlive(avalue)
	switch cif.ret {
	case VoidType:
	case UInt32Type:
		*(*uint32)(rvalue) = uint32(r)
	case SInt32Type:
		*(*int32)(rvalue) = int32(uint32(r))
	case UInt64Type, PointerType:
		*(*uintptr)(rvalue) = r
	default:
		panic(fmt.Sprintf("ffi: a result of kind %d cannot come back through purego.SyscallN", cif.ret))
	}
	return 0, nil
}

// onHeap keeps what its argument points at off the goroutine stack.
// purego.SyscallN takes uintptrs, and only a pointer converted in the
// call's own argument list is moved to the heap for it; converted
// here, ahead of the call, a pointer into a stack the call then grows
// and copies would leave C writing to the old one.
//
//go:uintptrescapes
//go:noinline
func onHeap(uintptr) {}

// LoadLibrary opens the shared library name.
func LoadLibrary(name string) (unsafe.Pointer, error) {
	h, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, err
	}
	return *(*unsafe.Pointer)(unsafe.Pointer(&h)), nil
}

// GetSymbol looks name up in the library handle opened.
func GetSymbol(handle unsafe.Pointer, name string) (unsafe.Pointer, error) {
	p, err := purego.Dlsym(uintptr(handle), name)
	if err != nil {
		return nil, err
	}
	return *(*unsafe.Pointer)(unsafe.Pointer(&p)), nil
}

// FreeLibrary closes a library LoadLibrary opened.
func FreeLibrary(handle unsafe.Pointer) error {
	return purego.Dlclose(uintptr(handle))
}
