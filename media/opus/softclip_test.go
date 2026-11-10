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
		softClip(x, channels, &mem)
		if !slices.Equal(x, want) || mem != [2]float32{} {
			t.Errorf("%d channels: %v with curves %v, want the input untouched and no curve", channels, x, mem)
		}
	}
}

func TestSoftClipBendsExcursionsUnderFullScale(t *testing.T) {
	x := []float32{0.5, -0.2, 0.9, 1.6, 1.2, 0.4, -0.1, -0.8}
	var mem [2]float32
	softClip(x, 1, &mem)
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
	softClip([]float32{0.5, 1.5, 1.2}, 1, &mem)
	a := mem[0]
	if a >= 0 {
		t.Fatalf("a packet ending above full scale leaves curve %v, want a negative one", a)
	}
	x := []float32{0.8, 0.4, -0.3, 0.9}
	softClip(x, 1, &mem)
	want := []float32{0.8 + float32(float32(a*0.8)*0.8), 0.4 + float32(float32(a*0.4)*0.4), -0.3, 0.9}
	if !slices.Equal(x, want) || mem[0] != 0 {
		t.Errorf("the next packet is %v with curve %v, want %v (bent up to its zero crossing) and none", x, mem[0], want)
	}
}
