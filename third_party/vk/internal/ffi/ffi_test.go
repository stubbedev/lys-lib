package ffi

import (
	"testing"
	"unsafe"
)

// A pointer argument reaches C intact however deep the caller's stack
// is: the call can grow and copy the stack after the arguments become
// uintptrs, so what they point at must not live on it.
func TestPointerArgumentsSurviveAStackCopy(t *testing.T) {
	lib, err := LoadLibrary("libc.so.6")
	if err != nil {
		t.Skipf("no libc to call: %v", err)
	}
	t.Cleanup(func() {
		if err := FreeLibrary(lib); err != nil {
			t.Error(err)
		}
	})
	memset, err := GetSymbol(lib, "memset")
	if err != nil {
		t.Fatal(err)
	}
	var cif CallInterface
	if err := PrepareCallInterface(&cif, DefaultCall, PointerTypeDescriptor, []*TypeDescriptor{PointerTypeDescriptor, SInt32TypeDescriptor, UInt64TypeDescriptor}); err != nil {
		t.Fatal(err)
	}
	for depth := range 300 {
		got := make(chan uint64)
		go func() { got <- fillAt(depth, &cif, memset) }()
		if v := <-got; v != 0xabababababababab {
			t.Fatalf("at depth %d memset left %#x", depth, v)
		}
	}
}

// fillAt has memset fill a uint64 on its stack depth frames down.
//
//go:noinline
func fillAt(depth int, cif *CallInterface, memset unsafe.Pointer) uint64 {
	if depth > 0 {
		var pad [48]byte
		return fillAt(depth-1, cif, memset) + uint64(pad[depth%len(pad)])
	}
	var x uint64
	p, c, n := unsafe.Pointer(&x), int32(0xab), uint64(8)
	var r unsafe.Pointer
	if _, err := CallFunction(cif, memset, unsafe.Pointer(&r), []unsafe.Pointer{unsafe.Pointer(&p), unsafe.Pointer(&c), unsafe.Pointer(&n)}); err != nil {
		panic(err)
	}
	return x
}
