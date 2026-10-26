package golden

import (
	"fmt"
	"image"
	"testing"
)

// recorder is a TB that records a failure instead of stopping the test.
type recorder struct{ failed string }

func (r *recorder) Helper()                        {}
func (r *recorder) Logf(string, ...any)            {}
func (r *recorder) Fatalf(format string, a ...any) { r.failed = fmt.Sprintf(format, a...) }

// The tolerance contract: a pixel matches within ChannelDiff on every
// channel, and the comparison passes while at most MaxMismatch pixels
// do not (#188).
func TestCheckTolerance(t *testing.T) {
	t.Setenv("UPDATE_GOLDEN", "")
	dir := t.TempDir()
	golden := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	write(t, dir+"/g.png", golden)
	shot := func(diffs ...uint8) *image.NRGBA {
		img := image.NewNRGBA(golden.Rect)
		for i, d := range diffs {
			img.Pix[i*4+3] = d
		}
		return img
	}
	for name, tc := range map[string]struct {
		img  *image.NRGBA
		tol  Tolerance
		pass bool
	}{
		"identical":                {shot(), Tolerance{}, true},
		"exact by default":         {shot(1), Tolerance{}, false},
		"within the channel diff":  {shot(3, 3), Tolerance{ChannelDiff: 3}, true},
		"past the channel diff":    {shot(4), Tolerance{ChannelDiff: 3}, false},
		"within the mismatch cap":  {shot(9, 9), Tolerance{ChannelDiff: 3, MaxMismatch: 2}, true},
		"past the mismatch cap":    {shot(9, 9, 9), Tolerance{ChannelDiff: 3, MaxMismatch: 2}, false},
		"mismatch cap, exact diff": {shot(1), Tolerance{MaxMismatch: 1}, true},
	} {
		t.Run(name, func(t *testing.T) {
			r := &recorder{}
			Check(r, dir, "g", tc.img, tc.tol)
			if pass := r.failed == ""; pass != tc.pass {
				t.Errorf("pass = %v, want %v (%s)", pass, tc.pass, r.failed)
			}
		})
	}
}
