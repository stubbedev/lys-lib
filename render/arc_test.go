package render

import (
	"math"
	"testing"
)

func arcCanvas(t *testing.T) (*Canvas, []byte) {
	t.Helper()
	const w = 40
	buf := make([]byte, Stride(w)*w)
	return New(buf, Stride(w), w, w), buf
}

func alphaAt(buf []byte, x, y int) uint8 { return buf[y*Stride(40)+x*4+3] }

// A quarter arc from twelve o'clock clockwise paints the top and right
// of the ring and leaves the bottom and left empty; the round caps
// reach past the ends; the full circle paints all four sides.
func TestArcPaintsTheSweepWithRoundCaps(t *testing.T) {
	c, buf := arcCanvas(t)
	white := RGB(255, 255, 255)
	c.Arc(20, 20, 15, 4, -math.Pi/2, math.Pi/2, white)
	if alphaAt(buf, 20, 5) == 0 || alphaAt(buf, 35, 20) == 0 {
		t.Fatal("the quarter arc is missing its ends")
	}
	if alphaAt(buf, 31, 9) == 0 {
		t.Error("the middle of the quarter arc is empty")
	}
	if alphaAt(buf, 20, 35) != 0 || alphaAt(buf, 5, 20) != 0 {
		t.Error("the quarter arc painted the bottom or left")
	}
	// The cap at twelve o'clock rounds a little to the left of the end.
	if alphaAt(buf, 19, 5) == 0 {
		t.Error("no round cap at the start")
	}
	if alphaAt(buf, 20, 20) != 0 {
		t.Error("the arc filled its center")
	}

	c2, buf2 := arcCanvas(t)
	c2.Arc(20, 20, 15, 4, 0, 2*math.Pi, white)
	for _, p := range [][2]int{{20, 5}, {35, 20}, {20, 35}, {5, 20}} {
		if alphaAt(buf2, p[0], p[1]) == 0 {
			t.Errorf("the full circle missed %v", p)
		}
	}
}

func TestArcIgnoresDegenerateInput(t *testing.T) {
	c, buf := arcCanvas(t)
	white := RGB(255, 255, 255)
	c.Arc(20, 20, 15, 0, 0, math.Pi, white)
	c.Arc(20, 20, 0, 4, 0, math.Pi, white)
	c.Arc(20, 20, 15, 4, 0, 0, white)
	for i := 3; i < len(buf); i += 4 {
		if buf[i] != 0 {
			t.Fatal("a degenerate arc painted")
		}
	}
}
