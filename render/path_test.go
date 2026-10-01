package render

import "testing"

// TestFillPath pins the path fill: inside the outline is the color,
// outside untouched, a scaled canvas maps logical points to device
// pixels, the clip holds, and an empty path paints nothing.
func TestFillPath(t *testing.T) {
	red := RGB(255, 0, 0)
	tri := func() *Path {
		var p Path
		p.MoveTo(0, 0)
		p.LineTo(20, 0)
		p.LineTo(0, 20)
		p.Close()
		return &p
	}

	c, data := newTestCanvas(40, 40)
	c.FillPath(tri(), red)
	if got := pxAt(data, Stride(40), 3, 3); got != red {
		t.Errorf("inside = %#08x, want red", uint32(got))
	}
	if got := pxAt(data, Stride(40), 18, 18); got != 0 {
		t.Errorf("past the hypotenuse = %#08x, want untouched", uint32(got))
	}
	if edge := pxAt(data, Stride(40), 10, 9); edge == 0 {
		t.Error("the fill stops short of the hypotenuse")
	}

	data2 := make([]byte, Stride(40)*40)
	scaled := NewScaled(data2, Stride(40), 40, 40, 2, 1)
	scaled.FillPath(tri(), red)
	if got := pxAt(data2, Stride(40), 30, 3); got != red {
		t.Errorf("2x canvas at device (30,3) = %#08x, want red (logical 15,1.5)", uint32(got))
	}

	c3, data3 := newTestCanvas(40, 40)
	prev := c3.PushClip(Rect{X: 0, Y: 0, W: 5, H: 40})
	c3.FillPath(tri(), red)
	c3.PopClip(prev)
	if pxAt(data3, Stride(40), 8, 2) != 0 || pxAt(data3, Stride(40), 2, 2) != red {
		t.Error("the fill ignored the clip")
	}

	// A cubic bulges past its chord.
	c4, data4 := newTestCanvas(40, 40)
	var curve Path
	curve.MoveTo(0, 30)
	curve.CubeTo(10, 0, 30, 0, 40, 30)
	curve.Close()
	c4.FillPath(&curve, red)
	if pxAt(data4, Stride(40), 20, 12) != red || pxAt(data4, Stride(40), 20, 2) != 0 {
		t.Error("the cubic is not where its control points put it")
	}

	c5, _ := newTestCanvas(10, 10)
	c5.FillPath(&Path{}, red)
	c5.FillPath(nil, red)
	if c5.Touched() != 0 {
		t.Error("an empty path painted")
	}
}
