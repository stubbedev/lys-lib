package opus

import (
	"slices"
	"testing"
)

func TestSoftClipLeavesAudioUnderFullScaleAlone(t *testing.T) {
	want := []float32{0, 0.25, -1, 1, -0.5, 0.75, 1, -1}
	for channels := 1; channels <= 2; channels++ {
		x := slices.Clone(want)
		var mem [2]float32
		softClip(x, 1, channels, &mem)
		if !slices.Equal(x, want) || mem != [2]float32{} {
			t.Errorf("%d channels: %v with curves %v, want the input untouched and no curve", channels, x, mem)
		}
	}
}

func TestSoftClipBendsExcursionsUnderFullScale(t *testing.T) {
	x := []float32{0.5, -0.2, 0.9, 1.6, 1.2, 0.4, -0.1, -0.8}
	var mem [2]float32
	softClip(x, 1, 1, &mem)
	for i, v := range x {
		if v > 1 || v < -1 {
			t.Errorf("sample %d is %v, want it within full scale", i, v)
		}
	}
	if want := []float32{0.5, -0.2}; !slices.Equal(x[:2], want) {
		t.Errorf("the samples before the excursion's zero crossing are %v, want %v", x[:2], want)
	}
	if want := []float32{-0.1, -0.8}; !slices.Equal(x[6:], want) || mem[0] != 0 {
		t.Errorf("the samples after the excursion are %v with curve %v, want %v and none", x[6:], mem[0], want)
	}
}

func TestSoftClipCarriesACurveIntoAPacketUnderFullScale(t *testing.T) {
	var mem [2]float32
	softClip([]float32{0.5, 1.5, 1.2}, 1, 1, &mem)
	a := mem[0]
	if a >= 0 {
		t.Fatalf("a packet ending above full scale leaves curve %v, want a negative one", a)
	}
	x := []float32{0.8, 0.4, -0.3, 0.9}
	softClip(x, 1, 1, &mem)
	want := []float32{0.8 + float32(float32(a*0.8)*0.8), 0.4 + float32(float32(a*0.4)*0.4), -0.3, 0.9}
	if !slices.Equal(x, want) || mem[0] != 0 {
		t.Errorf("the next packet is %v with curve %v, want %v (bent up to its zero crossing) and none", x, mem[0], want)
	}
}

func TestSoftClipScalesBeforeItBends(t *testing.T) {
	x := []float32{16384, -8192, 32768, -32768, 0}
	var mem [2]float32
	softClip(x, 1.0/32768, 1, &mem)
	if want := []float32{0.5, -0.25, 1, -1, 0}; !slices.Equal(x, want) || mem != [2]float32{} {
		t.Errorf("int16-scale samples under full scale come out as %v with curves %v, want %v and none", x, mem, want)
	}
	x = []float32{-0.25, 0.75, 0.5, -0.25}
	softClip(x, 2, 1, &mem)
	if x[0] != -0.5 || x[3] != -0.5 {
		t.Errorf("the samples around the excursion are %v and %v, want both doubled to -0.5", x[0], x[3])
	}
	if x[1] > 1 || x[1] <= 0.75 || x[2] >= 1 || x[2] <= 0.5 {
		t.Errorf("the excursion scales and bends to %v, want its 1.5 bent under full scale and its 1 below", x[1:3])
	}
}
