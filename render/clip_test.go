package render

import "testing"

// PushClip takes a logical rect, mapped to the device like FillRect;
// PushClipDevice takes device pixels as given.
func TestPushClipMapsLogicalRects(t *testing.T) {
	data := make([]byte, Stride(20)*20)
	cv := NewScaled(data, Stride(20), 20, 20, 2, 1)
	prev := cv.PushClip(Rect{W: 5, H: 5})
	cv.FillRect(Rect{W: 10, H: 10}, RGB(255, 255, 255))
	cv.PopClip(prev)
	red := func(x, y int) uint8 { return data[y*Stride(20)+x*4+2] }
	if red(9, 9) != 255 || red(10, 10) != 0 {
		t.Errorf("logical clip 5x5 at 2x: device (9,9) red %d, (10,10) red %d; want the 10x10 device square", red(9, 9), red(10, 10))
	}
	if cv.clip != cv.Rect() {
		t.Errorf("PopClip left clip %v", cv.clip)
	}
	clear(data)
	prev = cv.PushClipDevice(Rect{W: 5, H: 5})
	cv.FillRect(Rect{W: 10, H: 10}, RGB(255, 255, 255))
	cv.PopClip(prev)
	if red(4, 4) != 255 || red(5, 5) != 0 {
		t.Errorf("device clip 5x5: device (4,4) red %d, (5,5) red %d; want the 5x5 device square", red(4, 4), red(5, 5))
	}
}
