package render

import (
	"bytes"
	"testing"
)

// TestGlyphAtlasWarmDrawMatchesCold pins the atlas's core promise: a
// draw served from the cache produces the identical canvas a fresh
// rasterization produces, for both the outline masks and the tinted
// blit.
func TestGlyphAtlasWarmDrawMatchesCold(t *testing.T) {
	tf := testTypeface(t)
	s := tf.Shape("atlas warm/cold", 16)

	cvCold, dataCold := newTestCanvas(300, 40)
	glyphs.dropAll()
	tf.Draw(cvCold, s, 4, 30, RGB(255, 255, 255))

	cvWarm, dataWarm := newTestCanvas(300, 40)
	tf.Draw(cvWarm, s, 4, 30, RGB(255, 255, 255)) // every glyph a cache hit

	if !bytes.Equal(dataCold, dataWarm) {
		t.Fatal("warm draw differs from the cold rasterization")
	}

	// The same cached masks tint to a different color untouched: the
	// blit applies the color, the cache stays colorless.
	cvRed, dataRed := newTestCanvas(300, 40)
	tf.Draw(cvRed, s, 4, 30, RGB(255, 0, 0))
	if bytes.Equal(dataRed, dataWarm) {
		t.Fatal("a second color reused the first color's pixels")
	}
	cold, _ := inkBounds(dataCold, Stride(300), 300, 40)
	red, _ := inkBounds(dataRed, Stride(300), 300, 40)
	if cold != red {
		t.Errorf("ink bounds moved between colors: %v vs %v", cold, red)
	}
}

func TestGlyphAtlasReuse(t *testing.T) {
	tf := testTypeface(t)

	t.Run("repeat draws add no entries", func(t *testing.T) {
		glyphs.dropAll()
		cv, _ := newTestCanvas(300, 40)
		tf.Draw(cv, tf.Shape("reuse", 16), 4, 30, RGB(255, 255, 255))
		afterOne := glyphs.len()
		for range 5 {
			cv2, _ := newTestCanvas(300, 40)
			tf.Draw(cv2, tf.Shape("reuse", 16), 4, 30, RGB(255, 255, 255))
		}
		if glyphs.len() != afterOne {
			t.Errorf("repeat draws grew the atlas from %d to %d entries", afterOne, glyphs.len())
		}
	})

	t.Run("integer pen offsets share entries", func(t *testing.T) {
		glyphs.dropAll()
		cv, _ := newTestCanvas(320, 40)
		tf.Draw(cv, tf.Shape("aa", 16), 4, 30, RGB(255, 255, 255))
		n := glyphs.len()
		tf.Draw(cv, tf.Shape("aa", 16), 5, 30, RGB(255, 255, 255)) // +1 px
		tf.Draw(cv, tf.Shape("aa", 16), 9, 31, RGB(255, 255, 255)) // +1 px down
		if glyphs.len() != n {
			t.Errorf("whole-pixel offsets grew the atlas from %d to %d", n, glyphs.len())
		}
	})

	t.Run("subpixel offsets quantize into buckets", func(t *testing.T) {
		glyphs.dropAll()
		// Fractional pens arise inside the line (advances are
		// 26.6), so exercise cachedOutlineRaster at fractional x
		// directly: 4.25 and 4.29 land in one bucket, 4.0 in
		// another, and the atlas grows by two entries total.
		s := tf.Shape("b", 16)
		gid := s.runs[0].out.Glyphs[0].GlyphID
		scale := 16.0 / tf.upem
		cachedOutlineRaster(tf, gid, scale, 4.0, 30.0)
		n := glyphs.len()
		cachedOutlineRaster(tf, gid, scale, 4.25, 30.0)
		cachedOutlineRaster(tf, gid, scale, 4.27, 30.0)
		cachedOutlineRaster(tf, gid, scale, 4.29, 30.0)
		if got := glyphs.len() - n; got != 1 {
			t.Errorf("nearby fractional offsets produced %d entries, want 1 (one bucket)", got)
		}
	})

	t.Run("device scale changes the key", func(t *testing.T) {
		glyphs.dropAll()
		data := make([]byte, Stride(320)*40)
		cv2x := NewScaled(data, Stride(320), 320, 40, 2, 1)
		tf.Draw(cv2x, tf.Shape("s", 16), 4, 30, RGB(255, 255, 255))
		n := glyphs.len()
		cv1x, _ := newTestCanvas(320, 40)
		tf.Draw(cv1x, tf.Shape("s", 16), 4, 30, RGB(255, 255, 255))
		if glyphs.len() != n+1 {
			t.Error("a 2x raster reused the 1x mask: the scale is missing from the key")
		}
	})
}

func TestGlyphAtlasWarmDrawAllocatesNothing(t *testing.T) {
	tf := testTypeface(t)
	cv, _ := newTestCanvas(400, 40)
	s := tf.Shape("warm draws allocate nothing per glyph", 16)
	tf.Draw(cv, s, 4, 30, RGB(255, 255, 255)) // warm every glyph

	allocs := testing.AllocsPerRun(20, func() {
		tf.Draw(cv, s, 4, 30, RGB(255, 255, 255))
	})
	if allocs != 0 {
		t.Errorf("warm draw allocated %v times per op, want 0 (rasterizer or mask leak)", allocs)
	}
}

// TestGlyphDrawPlacesInkAroundBaseline pins where a glyph lands: ink
// straddles the baseline the pen named (ascent above, a descender
// below), starting near the pen's x. A blit that ignores the raster's
// origin offset shifts the whole line and fails here.
func TestGlyphDrawPlacesInkAroundBaseline(t *testing.T) {
	tf := testTypeface(t)
	for _, dev := range []struct{ num, denom int }{{1, 1}, {240, 120}} {
		lw, lh := 200, 120 // logical canvas
		dw, lhDev := lw*dev.num/dev.denom, lh*dev.num/dev.denom
		data := make([]byte, Stride(dw)*lhDev)
		cv := NewScaled(data, Stride(dw), dw, lhDev, dev.num, dev.denom)
		tf.Draw(cv, tf.Shape("gx", 20), 10, lh-40, RGB(255, 255, 255))
		bounds, count := inkBounds(data, Stride(dw), dw, lhDev)
		if count == 0 {
			t.Fatalf("scale %d/%d: no ink drawn", dev.num, dev.denom)
		}
		base := (lh - 40) * dev.num / dev.denom
		penX := 10 * dev.num / dev.denom
		if bounds.Y >= base {
			t.Errorf("scale %d/%d: ink top at y=%d is below the baseline %d", dev.num, dev.denom, bounds.Y, base)
		}
		if bounds.Y+bounds.H <= base {
			t.Errorf("scale %d/%d: ink bottom at y=%d never crosses the baseline %d; the line is shifted up", dev.num, dev.denom, bounds.Y+bounds.H, base)
		}
		if bounds.X < penX-2 || bounds.X > penX+6 {
			t.Errorf("scale %d/%d: ink starts at x=%d, want within a few pixels of the pen %d", dev.num, dev.denom, bounds.X, penX)
		}
	}
}

// TestBitmapStrikeScaleCached pins the embedded-bitmap half of the
// atlas: the CatmullRom resample of a color emoji strike happens once
// per (face, glyph, size), and repeat draws reuse it pixel-for-pixel.
func TestBitmapStrikeScaleCached(t *testing.T) {
	tf := blendEmoji(t)
	s := tf.Shape("\U0001F600", 16)

	cvCold, cold := newTestCanvas(64, 64)
	bitmapScales.dropAll()
	tf.Draw(cvCold, s, 8, 50, RGB(255, 255, 255))
	if n := bitmapScales.len(); n != 1 {
		t.Fatalf("cold emoji draw cached %d scaled strikes, want 1", n)
	}

	cvWarm, warm := newTestCanvas(64, 64)
	tf.Draw(cvWarm, s, 8, 50, RGB(255, 255, 255))
	if !bytes.Equal(cold, warm) {
		t.Fatal("warm emoji draw differs from the cold resample")
	}
	if bitmapScales.len() != 1 {
		t.Errorf("repeat emoji draws left %d scaled strikes cached, want 1", bitmapScales.len())
	}

	// A different size rescales: distinct key, its own entry.
	cvBig, _ := newTestCanvas(96, 96)
	tf.Draw(cvBig, tf.Shape("\U0001F600", 32), 8, 80, RGB(255, 255, 255))
	if bitmapScales.len() != 2 {
		t.Errorf("a second size produced %d cached strikes total, want 2", bitmapScales.len())
	}
}

// TestGlyphRasterSpaceGlyphParsesOnce checks the blank-glyph path
// (spaces: no outline segments, no bitmap) still blits nothing and does
// not wedge the atlas.
func TestGlyphRasterSpaceGlyphBlitsNothing(t *testing.T) {
	tf := testTypeface(t)
	cv, data := newTestCanvas(120, 32)
	tf.Draw(cv, tf.Shape("  ", 16), 4, 24, RGB(255, 255, 255))
	if _, count := inkBounds(data, Stride(120), 120, 32); count != 0 {
		t.Errorf("spaces produced %d ink pixels", count)
	}
	if px := pxAt(data, Stride(120), 60, 12); px != 0 {
		t.Errorf("space draw touched a pixel: %v", px)
	}
}
