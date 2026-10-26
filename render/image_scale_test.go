package render

import (
	"image"
	"testing"
)

func TestPlaceImageStretch(t *testing.T) {
	// The whole source, resampled to exactly the box whatever the ratio.
	for _, tt := range []struct{ srcW, srcH, boxW, boxH int }{
		{4, 4, 10, 3}, {16, 2, 5, 9}, {7, 7, 7, 7},
	} {
		src, w, h := placeCentered(tt.srcW, tt.srcH, tt.boxW, tt.boxH, ImageStretch)
		if src != image.Rect(0, 0, tt.srcW, tt.srcH) || w != tt.boxW || h != tt.boxH {
			t.Errorf("stretch %v = %v, %dx%d; want the full source at %dx%d", tt, src, w, h, tt.boxW, tt.boxH)
		}
	}
	if src, w, h := placeCentered(4, 4, 0, 3, ImageStretch); !src.Empty() || w != 0 || h != 0 {
		t.Errorf("zero box = %v %dx%d", src, w, h)
	}
}

func TestPlaceImageScaleDown(t *testing.T) {
	// Fits: natural size, never enlarged.
	if src, w, h := placeCentered(4, 3, 10, 10, ImageScaleDown); src != image.Rect(0, 0, 4, 3) || w != 4 || h != 3 {
		t.Errorf("fitting source = %v, %dx%d; want its natural 4x3", src, w, h)
	}
	// Exactly the box on one axis still fits.
	if _, w, h := placeCentered(10, 3, 10, 10, ImageScaleDown); w != 10 || h != 3 {
		t.Errorf("edge fit = %dx%d", w, h)
	}
	// Too big on either axis: shrunk like ImageFit, not clipped like ImageNone.
	for _, tt := range []struct{ srcW, srcH, boxW, boxH int }{{20, 10, 10, 10}, {5, 40, 10, 10}, {30, 30, 10, 20}} {
		gotSrc, gotW, gotH := placeCentered(tt.srcW, tt.srcH, tt.boxW, tt.boxH, ImageScaleDown)
		wantSrc, wantW, wantH := placeCentered(tt.srcW, tt.srcH, tt.boxW, tt.boxH, ImageFit)
		if gotSrc != wantSrc || gotW != wantW || gotH != wantH {
			t.Errorf("oversized %v = %v %dx%d, want the fit %v %dx%d", tt, gotSrc, gotW, gotH, wantSrc, wantW, wantH)
		}
	}
}
