package xkb

import (
	"runtime"
	"testing"
)

func benchKeymap(b *testing.B) *Keymap {
	b.Helper()
	km, err := testData(b).NewKeymapFromNames(benchNames)
	if err != nil {
		b.Fatal(err)
	}
	return km
}

var benchNames = Names{Rules: "evdev", Model: "pc105", Layout: "us,ru", Options: "grp:alt_shift_toggle"}

func benchC(b *testing.B) (*libxkb, uintptr) {
	b.Helper()
	l := loadLibxkb(b)
	return l, l.compile(b, benchNames)
}

const (
	benchKeyA     = 38
	benchKeyShift = 50
)

var benchSink uint32

func BenchmarkUpdateKeyLetter(b *testing.B) {
	st := benchKeymap(b).NewState()
	b.ReportAllocs()
	for b.Loop() {
		st.UpdateKey(benchKeyA, KeyPressed)
		st.UpdateKey(benchKeyA, KeyReleased)
	}
}

func BenchmarkUpdateKeyLetterC(b *testing.B) {
	l, c := benchC(b)
	st := l.stateNew(c)
	b.ReportAllocs()
	for b.Loop() {
		l.updateKey(st, benchKeyA, 1)
		l.updateKey(st, benchKeyA, 0)
	}
}

func BenchmarkUpdateKeyShift(b *testing.B) {
	st := benchKeymap(b).NewState()
	b.ReportAllocs()
	for b.Loop() {
		st.UpdateKey(benchKeyShift, KeyPressed)
		st.UpdateKey(benchKeyShift, KeyReleased)
	}
}

func BenchmarkUpdateKeyShiftC(b *testing.B) {
	l, c := benchC(b)
	st := l.stateNew(c)
	b.ReportAllocs()
	for b.Loop() {
		l.updateKey(st, benchKeyShift, 1)
		l.updateKey(st, benchKeyShift, 0)
	}
}

func BenchmarkKeyGetOneSym(b *testing.B) {
	st := benchKeymap(b).NewState()
	st.UpdateKey(benchKeyShift, KeyPressed)
	b.ReportAllocs()
	for b.Loop() {
		benchSink += uint32(st.KeyGetOneSym(benchKeyA))
	}
}

func BenchmarkKeyGetOneSymC(b *testing.B) {
	l, c := benchC(b)
	st := l.stateNew(c)
	l.updateKey(st, benchKeyShift, 1)
	b.ReportAllocs()
	for b.Loop() {
		benchSink += l.oneSym(st, benchKeyA)
	}
}

func BenchmarkKeyGetUTF32(b *testing.B) {
	st := benchKeymap(b).NewState()
	b.ReportAllocs()
	for b.Loop() {
		benchSink += uint32(st.KeyGetUTF32(benchKeyA))
	}
}

func BenchmarkKeyGetUTF32C(b *testing.B) {
	l, c := benchC(b)
	st := l.stateNew(c)
	b.ReportAllocs()
	for b.Loop() {
		benchSink += l.stateUTF32(st, benchKeyA)
	}
}

func BenchmarkSerializeMods(b *testing.B) {
	st := benchKeymap(b).NewState()
	b.ReportAllocs()
	for b.Loop() {
		benchSink += uint32(st.SerializeMods(StateModEffective))
	}
}

func BenchmarkSerializeModsC(b *testing.B) {
	l, c := benchC(b)
	st := l.stateNew(c)
	b.ReportAllocs()
	for b.Loop() {
		benchSink += l.serializeMods(st, stateModsEff)
	}
}

func BenchmarkLEDNameIsActive(b *testing.B) {
	st := benchKeymap(b).NewState()
	b.ReportAllocs()
	for b.Loop() {
		if st.LEDNameIsActive("Caps Lock") {
			benchSink++
		}
	}
}

func BenchmarkLEDNameIsActiveC(b *testing.B) {
	l, c := benchC(b)
	st := l.stateNew(c)
	name := cstr("Caps Lock")
	b.ReportAllocs()
	for b.Loop() {
		benchSink += uint32(l.ledActive(st, name))
	}
}

func BenchmarkLEDIndexIsActive(b *testing.B) {
	km := benchKeymap(b)
	st := km.NewState()
	i, ok := km.LEDIndex("Caps Lock")
	if !ok {
		b.Fatal("no Caps Lock LED")
	}
	b.ReportAllocs()
	for b.Loop() {
		if st.LEDIndexIsActive(i) {
			benchSink++
		}
	}
}

func BenchmarkKeysymFromName(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		benchSink += uint32(KeysymFromName("ISO_Level3_Shift", KeysymNoFlags))
	}
}

func BenchmarkKeysymFromNameC(b *testing.B) {
	l := loadLibxkb(b)
	name := cstr("ISO_Level3_Shift")
	b.ReportAllocs()
	for b.Loop() {
		benchSink += l.keysymFromName(name, 0)
	}
}

func BenchmarkCompileFromNames(b *testing.B) {
	ctx := testData(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ctx.NewKeymapFromNames(benchNames); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompileFromNamesC(b *testing.B) {
	l := loadLibxkb(b)
	b.ReportAllocs()
	for b.Loop() {
		l.keymapUnref(l.compile(b, benchNames))
	}
}

func BenchmarkParseServedKeymap(b *testing.B) {
	ctx := testData(b)
	text := []byte(benchKeymap(b).Text())
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ctx.NewKeymapFromString(text); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseServedKeymapC(b *testing.B) {
	l, c := benchC(b)
	text := l.text(b, c)
	name := cstr(text)
	b.ReportAllocs()
	for b.Loop() {
		l.keymapUnref(l.newFromString(l.ctx, name, formatTextV1, 0))
	}
}

func BenchmarkKeymapText(b *testing.B) {
	km := benchKeymap(b)
	b.ReportAllocs()
	for b.Loop() {
		benchSink += uint32(len(km.serialize(FormatTextV1, 0)))
	}
}

func benchGC(b *testing.B, keep any) {
	b.Helper()
	runtime.GC()
	b.ResetTimer()
	for b.Loop() {
		runtime.GC()
	}
	b.StopTimer()
	runtime.KeepAlive(keep)
}

func BenchmarkGCBaseline(b *testing.B) { benchGC(b, nil) }

func BenchmarkGCKeymapsAlive(b *testing.B) {
	kms := make([]*Keymap, 0, 100)
	for range 100 {
		kms = append(kms, benchKeymap(b))
	}
	benchGC(b, kms)
}
