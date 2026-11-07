package gpu

import (
	"image"
	"image/color"
	"testing"
)

func TestReadRectReadsBackOnlyItsRowsAndMatchesTheFullFrame(t *testing.T) {
	d := openDevice(t)
	const w, h = 64, 48
	tg := newTarget(t, d, w, h)
	runPass(t, d, tg, nil, func(p *Pass) {
		p.Clear(color.RGBA{A: 0xff})
		p.FillRect(image.Rect(0, 10, w, 20), color.RGBA{R: 0xff, A: 0xff}, Mask{})
		p.FillRect(image.Rect(5, 30, 40, 31), color.RGBA{G: 0xff, A: 0xff}, Mask{})
	})
	full := readTarget(t, tg)
	rows := image.Rect(0, 15, w, 32)
	before := d.readBack
	got, err := tg.ReadRect(rows)
	if err != nil {
		t.Fatal(err)
	}
	if read := d.readBack - before; read != rows.Dx()*rows.Dy()*4 {
		t.Fatalf("%d bytes read back for %v, want %d", read, rows, rows.Dx()*rows.Dy()*4)
	}
	if got.Bounds() != rows {
		t.Fatalf("an image of %v for %v", got.Bounds(), rows)
	}
	for y := rows.Min.Y; y < rows.Max.Y; y++ {
		for x := rows.Min.X; x < rows.Max.X; x++ {
			if a, b := got.RGBAAt(x, y), full.RGBAAt(x, y); a != b {
				t.Fatalf("(%d,%d) reads %v alone, %v in the full frame", x, y, a, b)
			}
		}
	}
	for _, r := range []image.Rectangle{{}, image.Rect(0, 40, w, 49), image.Rect(-1, 0, 4, 4)} {
		if _, err := tg.ReadRect(r); err == nil {
			t.Errorf("reading back %v of a %dx%d target did not fail", r, w, h)
		}
	}
}
