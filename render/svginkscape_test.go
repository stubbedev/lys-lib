package render

import "testing"

// inkscapeSVG is an Inkscape-written icon reduced to what made them
// fail: editor metadata, a foreign-namespace sodipodi:namedview, stops
// given in style, a gradient inheriting its stops through xlink:href,
// and an even-odd fill from the root's style.
const inkscapeSVG = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"
  xmlns:sodipodi="http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd"
  xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape"
  xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"
  width="16" height="16" viewBox="0 0 16 16" style="fill-rule:evenodd" inkscape:version="0.92">
  <metadata><rdf:RDF><rdf:Description/></rdf:RDF></metadata>
  <sodipodi:namedview pagecolor="#ffffff"><sodipodi:guide position="0,0"/></sodipodi:namedview>
  <defs>
    <linearGradient id="base"><stop style="stop-color:#ff0000;stop-opacity:1" offset="0"/><stop style="stop-color:#0000ff" offset="1"/></linearGradient>
    <linearGradient xlink:href="#base" id="chained" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="16" y2="0"/>
  </defs>
  <rect x="0" y="0" width="16" height="16" style="fill:url(#chained)" sodipodi:insensitive="true"/>
</svg>`

// An Inkscape icon loads and paints its chained gradient: red at the
// start, blue at the end (#257).
func TestLoadSVGInkscape(t *testing.T) {
	ic, err := LoadSVG([]byte(inkscapeSVG), 16, 16)
	if err != nil {
		t.Fatalf("LoadSVG: %v", err)
	}
	buf := make([]byte, Stride(16)*16)
	ic.Draw(New(buf, Stride(16), 16, 16), 0, 0)
	left, right := ColorFromBytes(buf[8*Stride(16):]), ColorFromBytes(buf[8*Stride(16)+15*4:])
	if left.R() < 200 || left.B() > 40 || right.B() < 200 || right.R() > 40 {
		t.Errorf("gradient ends %#x .. %#x, want red to blue", uint32(left), uint32(right))
	}
}

// A malformed document still fails.
func TestLoadSVGMalformed(t *testing.T) {
	if _, err := LoadSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect></svg>`), 16, 16); err == nil {
		t.Error("a malformed document loaded")
	}
}
