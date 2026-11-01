package xkb

import (
	"testing"

	memory "github.com/stubbedev/memory-go"
)

func offHeapSlice[T any](b *testing.B, a *memory.Local, src []T) []T {
	b.Helper()
	s, err := memory.MakeSlice[T](a, len(src))
	if err != nil {
		b.Fatal(err)
	}
	dst := s.Elems()
	copy(dst, src)
	return dst
}

func moveFlatOffHeap(b *testing.B, a *memory.Local, m *Keymap) {
	b.Helper()
	m.flatKeys = offHeapSlice(b, a, m.flatKeys)
	m.flatGroups = offHeapSlice(b, a, m.flatGroups)
	m.flatLevels = offHeapSlice(b, a, m.flatLevels)
	m.flatSyms = offHeapSlice(b, a, m.flatSyms)
	m.flatActions = offHeapSlice(b, a, m.flatActions)
	m.flatTypes = offHeapSlice(b, a, m.flatTypes)
	m.flatEntries = offHeapSlice(b, a, m.flatEntries)
	m.modLookup = offHeapSlice(b, a, m.modLookup)
	m.wideEntries = offHeapSlice(b, a, m.wideEntries)
	m.nameIndex = offHeapSlice(b, a, m.nameIndex)
	m.aliasPairs = offHeapSlice(b, a, m.aliasPairs)
}

func BenchmarkOffHeapUpdateKeyLetter(b *testing.B) {
	var a memory.Local
	defer a.Close()
	km := benchKeymap(b)
	moveFlatOffHeap(b, &a, km)
	st := km.NewState()
	b.ReportAllocs()
	for b.Loop() {
		st.UpdateKey(benchKeyA, KeyPressed)
		st.UpdateKey(benchKeyA, KeyReleased)
	}
}

func BenchmarkOffHeapUpdateKeyShift(b *testing.B) {
	var a memory.Local
	defer a.Close()
	km := benchKeymap(b)
	moveFlatOffHeap(b, &a, km)
	st := km.NewState()
	b.ReportAllocs()
	for b.Loop() {
		st.UpdateKey(benchKeyShift, KeyPressed)
		st.UpdateKey(benchKeyShift, KeyReleased)
	}
}

func BenchmarkOffHeapKeyGetOneSym(b *testing.B) {
	var a memory.Local
	defer a.Close()
	km := benchKeymap(b)
	moveFlatOffHeap(b, &a, km)
	st := km.NewState()
	st.UpdateKey(benchKeyShift, KeyPressed)
	b.ReportAllocs()
	for b.Loop() {
		benchSink += uint32(st.KeyGetOneSym(benchKeyA))
	}
}

func BenchmarkOffHeapGCKeymapsAlive(b *testing.B) {
	var a memory.Local
	defer a.Close()
	kms := make([]*Keymap, 0, 100)
	for range 100 {
		km := benchKeymap(b)
		moveFlatOffHeap(b, &a, km)
		kms = append(kms, km)
	}
	benchGC(b, kms)
}
