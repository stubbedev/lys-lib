package render

import "math"

// InkOverhang is how far the text's glyph outlines reach past its line
// box, which is Advance wide and Ascent over Descent tall from the pen's
// start on the baseline: pixels at 1x, rounded out, with one more for
// antialiasing. ok is false when a glyph's extents are unknown, and the
// caller must assume any reach.
func (s *ShapedText) InkOverhang() (reach Insets, ok bool) {
	minX, maxX := 0.0, s.Advance()
	minY, maxY := -s.Ascent(), s.Descent()
	for _, p := range s.pens(0, 0, 1) {
		ext, known := p.face.face.GlyphExtents(p.gid)
		if !known {
			return Insets{}, false
		}
		if ext.Width == 0 || ext.Height == 0 {
			continue
		}
		top := float64(ext.YBearing)
		bottom := top + float64(ext.Height)
		shear := float64(math.Abs(p.face.slant) * math.Max(math.Abs(top), math.Abs(bottom)))
		x0 := p.x + float64((float64(ext.XBearing)-shear)*p.scale)
		x1 := p.x + float64((float64(ext.XBearing+ext.Width)+shear)*p.scale)
		minX, maxX = math.Min(minX, x0), math.Max(maxX, x1)
		minY = math.Min(minY, p.y-float64(top*p.scale))
		maxY = math.Max(maxY, p.y-float64(bottom*p.scale))
	}
	out := func(v float64) int { return int(math.Ceil(math.Max(v, 0))) + 1 }
	return Insets{
		Top:    out(-s.Ascent() - minY),
		Right:  out(maxX - s.Advance()),
		Bottom: out(maxY - s.Descent()),
		Left:   out(-minX),
	}, true
}
