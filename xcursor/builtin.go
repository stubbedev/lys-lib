package xcursor

import (
	"image"
	"slices"
)

// Shape is a CSS cursor name and the legacy X names themes may ship it
// under instead.
type Shape struct {
	Name   string
	Legacy []string
}

// Shapes is cursor-shape-v1's shapes in protocol order, the first being
// its value 1.
var Shapes = []Shape{
	{"default", []string{"left_ptr"}},
	{"context-menu", []string{"left_ptr"}},
	{"help", []string{"question_arrow", "left_ptr"}},
	{"pointer", []string{"hand2", "hand1"}},
	{"progress", []string{"left_ptr_watch", "watch"}},
	{"wait", []string{"watch"}},
	{"cell", []string{"plus"}},
	{"crosshair", []string{"cross", "tcross"}},
	{"text", []string{"xterm"}},
	{"vertical-text", []string{"xterm"}},
	{"alias", []string{"dnd-link"}},
	{"copy", []string{"dnd-copy"}},
	{"move", []string{"fleur", "dnd-move"}},
	{"no-drop", []string{"dnd-none", "crossed_circle"}},
	{"not-allowed", []string{"crossed_circle"}},
	{"grab", []string{"openhand", "hand1"}},
	{"grabbing", []string{"closedhand", "fleur"}},
	{"e-resize", []string{"right_side"}},
	{"n-resize", []string{"top_side"}},
	{"ne-resize", []string{"top_right_corner"}},
	{"nw-resize", []string{"top_left_corner"}},
	{"s-resize", []string{"bottom_side"}},
	{"se-resize", []string{"bottom_right_corner"}},
	{"sw-resize", []string{"bottom_left_corner"}},
	{"w-resize", []string{"left_side"}},
	{"ew-resize", []string{"sb_h_double_arrow"}},
	{"ns-resize", []string{"sb_v_double_arrow"}},
	{"nesw-resize", []string{"fd_double_arrow"}},
	{"nwse-resize", []string{"bd_double_arrow"}},
	{"col-resize", []string{"sb_h_double_arrow"}},
	{"row-resize", []string{"sb_v_double_arrow"}},
	{"all-scroll", []string{"fleur"}},
	{"zoom-in", []string{"left_ptr"}},
	{"zoom-out", []string{"left_ptr"}},
	{"dnd-ask", []string{"dnd-copy"}},
	{"all-resize", []string{"fleur"}},
}

// Names lists the names to try for a CSS cursor name, best first.
func Names(css string) []string {
	if i := slices.IndexFunc(Shapes, func(s Shape) bool { return s.Name == css }); i >= 0 {
		return append([]string{css}, Shapes[i].Legacy...)
	}
	return []string{css}
}

// Builtin draws an arrow of the given size for a machine with no cursor
// theme, its tip at the hotspot.
func Builtin(size int) Image {
	size = max(size, 8)
	img := Image{Size: image.Pt(size, size), Nominal: size, Pixels: make([]byte, size*size*4)}
	inside := func(x, y int) bool {
		fy := float64(y) + 1
		return x >= 0 && y >= 0 && fy <= float64(size)*0.85 && float64(x) <= fy*0.6
	}
	for y := range size {
		for x := range size {
			if !inside(x, y) {
				continue
			}
			v := byte(0xff)
			if !inside(x-1, y) || !inside(x+1, y) || !inside(x, y-1) || !inside(x, y+1) {
				v = 0
			}
			copy(img.Pixels[(y*size+x)*4:], []byte{v, v, v, 0xff})
		}
	}
	return img
}
