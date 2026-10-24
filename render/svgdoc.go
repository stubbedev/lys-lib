package render

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	"golang.org/x/image/math/fixed"
)

// svgDoc is an SVG document parsed for drawing: oksvg's paths and the
// clip regions oksvg does not model.
type svgDoc struct {
	icon *oksvg.SvgIcon
	// clips[i] lists the clip regions SVGPaths[i] draws through, empty
	// when it is unclipped. The shapes under one clip-path reference
	// share its region, so a clipped group is one slice of paths.
	clips [][]*oksvg.SvgIcon
}

// parseSVG reads an SVG document through the shared preparation:
// foreign attributes dropped, path data made explicit, transforms
// canonical, rect radii resolved, clipPaths cut out for the clip
// regions, fill rules applied - for icons and SVG glyphs alike.
func parseSVG(data []byte) (*svgDoc, error) {
	l, err := layoutSVG(resolveRectRadii(canonicalTransforms(normalizePaths(dropForeignAttrs(data)))))
	if err != nil {
		return nil, fmt.Errorf("render: parse svg: %w", err)
	}
	icon, err := readSVGIcon(l.body)
	if err != nil {
		return nil, err
	}
	doc := &svgDoc{icon: icon, clips: make([][]*oksvg.SvgIcon, len(icon.SVGPaths))}
	if !l.matches(icon) {
		if l.clipped {
			return nil, errors.New("render: svg clip-path in a document whose shapes do not match its drawn paths (a <use>, an empty shape)")
		}
		return doc, nil
	}
	for i, s := range l.shapes {
		if s.evenOdd {
			icon.SVGPaths[i].UseNonZeroWinding = false
		}
	}
	regions := make(map[*svgClipUse]*oksvg.SvgIcon)
	for i, s := range l.shapes {
		for _, u := range s.clips {
			r, ok := regions[u]
			if !ok {
				if r, err = l.clipRegion(icon, u); err != nil {
					return nil, err
				}
				regions[u] = r
			}
			doc.clips[i] = append(doc.clips[i], r)
		}
	}
	return doc, nil
}

func readSVGIcon(body []byte) (*oksvg.SvgIcon, error) {
	icon, err := oksvg.ReadIconStream(bytes.NewReader(body), oksvg.StrictErrorMode)
	if err != nil {
		return nil, fmt.Errorf("render: parse svg: %w", err)
	}
	return icon, nil
}

// draw renders the document into img through m, user space to img
// pixels. A run of paths under the same clip regions draws into a
// layer that composites through their intersected coverage.
func (doc *svgDoc) draw(img *image.RGBA, m rasterx.Matrix2D) {
	paths := doc.icon.SVGPaths
	masks := make(map[*oksvg.SvgIcon]*image.Alpha)
	for i := 0; i < len(paths); {
		j := i + 1
		for j < len(paths) && slices.Equal(doc.clips[j], doc.clips[i]) {
			j++
		}
		if len(doc.clips[i]) == 0 {
			drawSVGPaths(img, paths[i:j], m)
		} else {
			layer := image.NewRGBA(img.Rect)
			drawSVGPaths(layer, paths[i:j], m)
			mask := clipMask(doc.clips[i], m, img.Rect, masks)
			draw.DrawMask(img, img.Rect, layer, img.Rect.Min, mask, img.Rect.Min, draw.Over)
		}
		i = j
	}
}

func drawSVGPaths(dst draw.Image, paths []oksvg.SvgPath, m rasterx.Matrix2D) {
	b := dst.Bounds()
	dasher := rasterx.NewDasher(b.Dx(), b.Dy(), newSVGScanner(b.Dx(), b.Dy(), dst))
	for i := range paths {
		paths[i].DrawTransformed(dasher, 1, m)
	}
}

// clipMask is the coverage of the intersected regions, each rendered
// once per draw (masks caches them).
func clipMask(regions []*oksvg.SvgIcon, m rasterx.Matrix2D, rect image.Rectangle, masks map[*oksvg.SvgIcon]*image.Alpha) *image.Alpha {
	region := func(r *oksvg.SvgIcon) *image.Alpha {
		if a, ok := masks[r]; ok {
			return a
		}
		a := image.NewAlpha(rect)
		drawSVGPaths(a, r.SVGPaths, m)
		masks[r] = a
		return a
	}
	if len(regions) == 1 {
		return region(regions[0])
	}
	out := image.NewAlpha(rect)
	copy(out.Pix, region(regions[0]).Pix)
	for _, r := range regions[1:] {
		for i, a := range region(r).Pix {
			out.Pix[i] = uint8((uint32(out.Pix[i])*uint32(a) + 127) / 255)
		}
	}
	return out
}

// fitViewBox maps the viewBox (vx, vy, vw, vh) onto the target rect -
// oksvg's SetTarget without its bug: SetTarget translates by the
// viewBox origin in user units, unscaled, so any viewBox not starting
// at 0,0 ("0 -960 960 960", a shared SVG glyph document's region)
// landed off target.
func fitViewBox(vx, vy, vw, vh, x, y, w, h float64) rasterx.Matrix2D {
	sx, sy := w/vw, h/vh
	return rasterx.Identity.Translate(x-vx*sx, y-vy*sy).Scale(sx, sy)
}

// svgShapes are the elements oksvg draws as one path each.
var svgShapes = map[string]bool{
	"path": true, "rect": true, "circle": true, "ellipse": true,
	"polygon": true, "polyline": true, "line": true,
}

// svgLayout is what oksvg does not read from a document: the drawn
// shapes' fill rules, transforms and clips, and the clipPaths.
type svgLayout struct {
	// body is the document with its clipPath elements cut out: oksvg
	// would draw their shapes.
	body []byte
	// shapes are the drawn shape elements' states in document order;
	// nil when a <use> makes the order oksvg draws in unknowable.
	shapes    []svgState
	clipPaths map[string]svgClipPath
	// clipped reports a clip-path reference, drawn or in <defs> for a
	// <use> to draw.
	clipped bool
}

// svgState is an element's resolved state: what it inherits combined
// with its own attributes.
type svgState struct {
	// ctm maps the element's coordinates to the document's user space.
	ctm     rasterx.Matrix2D
	evenOdd bool
	// clipEvenOdd is its clip-rule, read when it is a clipPath child.
	clipEvenOdd bool
	// clips are the references it draws through, outermost first.
	clips []*svgClipUse
}

// svgClipUse is one clip-path reference: the clipPath it names and the
// referencing element's user space. Every shape under the element
// shares the one value.
type svgClipUse struct {
	id  string
	ctm rasterx.Matrix2D
}

// svgClipPath is a <clipPath> element.
type svgClipPath struct {
	// content is its children's markup.
	content   []byte
	transform rasterx.Matrix2D
	// objectBoundingBox reports clipPathUnits="objectBoundingBox": the
	// content is in units of the referencing element's bounding box.
	objectBoundingBox bool
	// evenOdd is the clip-rule its children inherit.
	evenOdd bool
}

// layoutSVG scans data for its svgLayout.
func layoutSVG(data []byte) (*svgLayout, error) {
	l := &svgLayout{clipPaths: make(map[string]svgClipPath)}
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	stack := []svgState{{ctm: rasterx.Identity}}
	var body bytes.Buffer
	kept := int64(0)
	defs := 0
	use := false
	for {
		start := d.InputOffset()
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if t, ok := tok.(xml.EndElement); ok {
			stack = stack[:len(stack)-1]
			if t.Name.Local == "defs" {
				defs--
			}
			continue
		}
		t, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		f := stack[len(stack)-1]
		if t.Name.Local == "clipPath" {
			if err := l.readClipPath(data, d, t, f); err != nil {
				return nil, err
			}
			body.Write(data[kept:start])
			kept = d.InputOffset()
			continue
		}
		if err := enterSVGElement(&f, t.Attr); err != nil {
			return nil, err
		}
		if len(f.clips) > len(stack[len(stack)-1].clips) {
			l.clipped = true
		}
		stack = append(stack, f)
		switch name := t.Name.Local; {
		case name == "use":
			use = true
		case name == "defs":
			defs++
		case defs == 0 && svgShapes[name]:
			l.shapes = append(l.shapes, f)
		}
	}
	body.Write(data[kept:])
	l.body = body.Bytes()
	if use {
		l.shapes = nil
	}
	return l, nil
}

// enterSVGElement updates f, the parent's state, to an element's own:
// its transform, rules and clip-path.
func enterSVGElement(f *svgState, attrs []xml.Attr) error {
	m, err := transformOf(attrs)
	if err != nil {
		return err
	}
	f.ctm = f.ctm.Mult(m)
	if v, ok := presentationAttr(attrs, "fill-rule"); ok {
		f.evenOdd = v == "evenodd"
	}
	if v, ok := presentationAttr(attrs, "clip-rule"); ok {
		f.clipEvenOdd = v == "evenodd"
	}
	v, ok := presentationAttr(attrs, "clip-path")
	if !ok || v == "none" {
		return nil
	}
	id, ok := clipURL(v)
	if !ok {
		return fmt.Errorf("clip-path %q is not a url(#id) reference", v)
	}
	f.clips = append(slices.Clip(f.clips), &svgClipUse{id: id, ctm: f.ctm})
	return nil
}

// readClipPath records the clipPath element t opens, reading d to its
// end. Its children's markup is kept whole for the region's own parse.
func (l *svgLayout) readClipPath(data []byte, d *xml.Decoder, t xml.StartElement, parent svgState) error {
	id, _ := attrValue(t.Attr, "id")
	cp := svgClipPath{evenOdd: parent.clipEvenOdd}
	if v, ok := attrValue(t.Attr, "clipPathUnits"); ok {
		switch v = strings.TrimSpace(v); v {
		case "objectBoundingBox":
			cp.objectBoundingBox = true
		case "userSpaceOnUse":
		default:
			return fmt.Errorf("clipPath %q: clipPathUnits %q", id, v)
		}
	}
	var err error
	if cp.transform, err = transformOf(t.Attr); err != nil {
		return fmt.Errorf("clipPath %q: %w", id, err)
	}
	if v, ok := presentationAttr(t.Attr, "clip-rule"); ok {
		cp.evenOdd = v == "evenodd"
	}
	if v, ok := presentationAttr(t.Attr, "clip-path"); ok && v != "none" {
		return fmt.Errorf("clipPath %q: clip-path on a clipPath is unsupported", id)
	}
	from := d.InputOffset()
	for depth := 1; ; {
		to := d.InputOffset()
		tok, err := d.Token()
		if err != nil {
			return fmt.Errorf("clipPath %q: %w", id, err)
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			if depth--; depth > 0 {
				continue
			}
			cp.content = data[from:to]
			if _, dup := l.clipPaths[id]; id != "" && !dup {
				l.clipPaths[id] = cp
			}
			return nil
		}
	}
}

// matches reports whether the layout's shapes are icon's paths, one to
// one in order.
func (l *svgLayout) matches(icon *oksvg.SvgIcon) bool {
	return l.shapes != nil && len(l.shapes) == len(icon.SVGPaths)
}

// clipRegion resolves the reference u to its region: the clipPath's
// children placed in the document's user space (the referencing
// element's, the clipPath's transform, and for objectBoundingBox
// units the element's bounding box), each filled opaque by its
// clip-rule. A region drawn for an element without extent is empty,
// so it clips everything away.
func (l *svgLayout) clipRegion(icon *oksvg.SvgIcon, u *svgClipUse) (*oksvg.SvgIcon, error) {
	cp, ok := l.clipPaths[u.id]
	if !ok {
		return nil, fmt.Errorf("render: svg clip-path references #%s, which is no clipPath", u.id)
	}
	m := u.ctm.Mult(cp.transform)
	content := cp.content
	if cp.objectBoundingBox {
		b, ok := l.bounds(icon, u)
		if !ok {
			return &oksvg.SvgIcon{}, nil
		}
		var err error
		if content, err = scaleSVGGeometry(content, bboxUnitScale); err != nil {
			return nil, fmt.Errorf("render: svg clipPath #%s: %w", u.id, err)
		}
		m = m.Translate(b.minX, b.minY).Scale(b.maxX-b.minX, b.maxY-b.minY).Scale(1/bboxUnitScale, 1/bboxUnitScale)
	}
	rule := ""
	if cp.evenOdd {
		rule = ` clip-rule="evenodd"`
	}
	src := make([]byte, 0, len(content)+160)
	src = append(src, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><g transform="`+matrixString(m)+`"`+rule+`>`...)
	src = append(append(src, content...), "</g></svg>"...)
	cl, err := layoutSVG(src)
	if err != nil {
		return nil, fmt.Errorf("render: svg clipPath #%s: %w", u.id, err)
	}
	if cl.clipped {
		return nil, fmt.Errorf("render: svg clipPath #%s: clip-path inside a clipPath is unsupported", u.id)
	}
	region, err := readSVGIcon(cl.body)
	if err != nil {
		return nil, fmt.Errorf("render: svg clipPath #%s: %w", u.id, err)
	}
	if !cl.matches(region) {
		return nil, fmt.Errorf("render: svg clipPath #%s: its shapes do not match its drawn paths (a <use>, an empty shape)", u.id)
	}
	for i := range region.SVGPaths {
		p := &region.SVGPaths[i]
		p.SetFillColor(color.Black)
		p.SetLineColor(nil)
		p.FillOpacity = 1
		p.UseNonZeroWinding = !cl.shapes[i].clipEvenOdd
	}
	return region, nil
}

// bboxUnitScale magnifies objectBoundingBox clip content before oksvg
// reads it: oksvg holds geometry in 26.6 fixed point of the element's
// own units, which for content in bounding-box units (0 to 1) snaps
// every coordinate to 1/64 of the box.
const bboxUnitScale = 1024.0

// svgLengthAttrs are the shape attributes that are lengths or
// coordinates in user units.
var svgLengthAttrs = map[string]bool{
	"x": true, "y": true, "width": true, "height": true, "rx": true, "ry": true,
	"cx": true, "cy": true, "r": true, "x1": true, "y1": true, "x2": true, "y2": true,
}

// scaleSVGGeometry rewrites the markup content with its geometry
// magnified k times: lengths, points and path data scaled, each
// element's transform conjugated so it acts on the scaled coordinates
// as it did on the originals.
func scaleSVGGeometry(content []byte, k float64) ([]byte, error) {
	d := xml.NewDecoder(bytes.NewReader(content))
	d.Strict = false
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			attrs, err := scaleSVGAttrs(t.Attr, k)
			if err != nil {
				return nil, fmt.Errorf("<%s>: %w", t.Name.Local, err)
			}
			err = enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: t.Name.Local}, Attr: attrs})
			if err != nil {
				return nil, err
			}
		case xml.EndElement:
			if err := enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: t.Name.Local}}); err != nil {
				return nil, err
			}
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// scaleSVGAttrs is scaleSVGGeometry for one element's attributes. The
// transform, attribute or style declaration, becomes one attribute.
func scaleSVGAttrs(attrs []xml.Attr, k float64) ([]xml.Attr, error) {
	m, err := transformOf(attrs)
	if err != nil {
		return nil, err
	}
	m.E, m.F = m.E*k, m.F*k
	out := make([]xml.Attr, 0, len(attrs)+1)
	for _, a := range attrs {
		name := a.Name.Local
		switch {
		case a.Name.Space != "" || name == "transform":
			continue
		case name == "style":
			a.Value = withoutStyleDecl(a.Value, "transform")
		case name == "d":
			v, ok := scalePathData(a.Value, k)
			if !ok {
				return nil, fmt.Errorf("path data %q does not parse", a.Value)
			}
			a.Value = v
		case name == "points":
			nums, ok := svgNumbers(a.Value)
			if !ok {
				return nil, fmt.Errorf("points %q do not parse", a.Value)
			}
			parts := make([]string, len(nums))
			for i, v := range nums {
				parts[i] = strconv.FormatFloat(v*k, 'g', -1, 64)
			}
			a.Value = strings.Join(parts, " ")
		case svgLengthAttrs[name]:
			v, err := strconv.ParseFloat(strings.TrimSpace(a.Value), 64)
			if err != nil {
				return nil, fmt.Errorf("%s %q is not a number in bounding-box units", name, a.Value)
			}
			a.Value = strconv.FormatFloat(v*k, 'g', -1, 64)
		}
		out = append(out, xml.Attr{Name: xml.Name{Local: name}, Value: a.Value})
	}
	return append(out, xml.Attr{Name: xml.Name{Local: "transform"}, Value: matrixString(m)}), nil
}

// withoutStyleDecl is a style attribute with its prop declarations
// removed.
func withoutStyleDecl(style, prop string) string {
	decls := strings.Split(style, ";")
	return strings.Join(slices.DeleteFunc(decls, func(decl string) bool {
		k, _, found := strings.Cut(decl, ":")
		return found && strings.TrimSpace(k) == prop
	}), ";")
}

// bounds is the object bounding box of the element that holds the
// reference u, in its user space: the tight extent of the fill
// geometry of the shapes it draws. false when it draws nothing.
func (l *svgLayout) bounds(icon *oksvg.SvgIcon, u *svgClipUse) (svgBounds, bool) {
	inv := u.ctm.Invert()
	b := svgBounds{minX: math.Inf(1), minY: math.Inf(1), maxX: math.Inf(-1), maxY: math.Inf(-1)}
	for i, s := range l.shapes {
		if slices.Contains(s.clips, u) {
			b.m = inv.Mult(s.ctm)
			icon.SVGPaths[i].Path.AddTo(&b)
		}
	}
	return b, b.maxX > b.minX && b.maxY > b.minY
}

// svgBounds is a rasterx.Adder that accumulates the tight extent of a
// path mapped through m: the end points and every curve's extrema.
type svgBounds struct {
	m                      rasterx.Matrix2D
	pen                    [2]float64
	minX, minY, maxX, maxY float64
}

func (b *svgBounds) point(p fixed.Point26_6) [2]float64 {
	x, y := b.m.Transform(float64(p.X)/64, float64(p.Y)/64)
	return [2]float64{x, y}
}

func (b *svgBounds) extend(p [2]float64) {
	b.minX, b.minY = math.Min(b.minX, p[0]), math.Min(b.minY, p[1])
	b.maxX, b.maxY = math.Max(b.maxX, p[0]), math.Max(b.maxY, p[1])
}

func (b *svgBounds) Start(a fixed.Point26_6) {
	b.pen = b.point(a)
	b.extend(b.pen)
}

func (b *svgBounds) Line(a fixed.Point26_6) {
	b.pen = b.point(a)
	b.extend(b.pen)
}

// QuadBezier extends by the quadratic as its exact cubic.
func (b *svgBounds) QuadBezier(c, e fixed.Point26_6) {
	p0, q, p3 := b.pen, b.point(c), b.point(e)
	var p1, p2 [2]float64
	for k := range 2 {
		p1[k] = p0[k] + 2*(q[k]-p0[k])/3
		p2[k] = p3[k] + 2*(q[k]-p3[k])/3
	}
	b.cubic(p0, p1, p2, p3)
}

func (b *svgBounds) CubeBezier(c1, c2, e fixed.Point26_6) {
	b.cubic(b.pen, b.point(c1), b.point(c2), b.point(e))
}

func (b *svgBounds) Stop(bool) {}

// cubic extends by the curve's end and the points where either
// coordinate turns: the roots in (0, 1) of its derivative.
func (b *svgBounds) cubic(p0, p1, p2, p3 [2]float64) {
	at := func(t float64) [2]float64 {
		u := 1 - t
		var p [2]float64
		for k := range 2 {
			p[k] = u*u*u*p0[k] + 3*u*u*t*p1[k] + 3*u*t*t*p2[k] + t*t*t*p3[k]
		}
		return p
	}
	for k := range 2 {
		a, c, e := p1[k]-p0[k], p2[k]-p1[k], p3[k]-p2[k]
		for _, t := range quadRoots(a-2*c+e, 2*(c-a), a) {
			if t > 0 && t < 1 {
				b.extend(at(t))
			}
		}
	}
	b.pen = p3
	b.extend(p3)
}

// quadRoots solves a t^2 + b t + c = 0, a linear equation when a is 0.
func quadRoots(a, b, c float64) []float64 {
	const eps = 1e-12
	if math.Abs(a) < eps {
		if math.Abs(b) < eps {
			return nil
		}
		return []float64{-c / b}
	}
	disc := b*b - 4*a*c
	if disc < 0 {
		return nil
	}
	sq := math.Sqrt(disc)
	return []float64{(-b + sq) / (2 * a), (-b - sq) / (2 * a)}
}

// transformOf is an element's own transform: the last of its transform
// attribute and style declaration, as oksvg applies them.
func transformOf(attrs []xml.Attr) (rasterx.Matrix2D, error) {
	v, set := "", false
	for _, a := range attrs {
		switch a.Name.Local {
		case "transform":
			v, set = a.Value, true
		case "style":
			if sv, ok := styleDecl(a.Value, "transform"); ok {
				v, set = sv, true
			}
		}
	}
	if !set {
		return rasterx.Identity, nil
	}
	m, ok := parseSVGTransform(v)
	if !ok {
		return m, fmt.Errorf("transform %q does not parse", v)
	}
	return m, nil
}

// presentationAttr reads an element's presentation attribute prop: the
// style declaration wins over the attribute, as in CSS.
func presentationAttr(attrs []xml.Attr, prop string) (string, bool) {
	v, set := "", false
	for _, a := range attrs {
		if a.Name.Space == "" && a.Name.Local == prop {
			v, set = strings.TrimSpace(a.Value), true
		}
	}
	for _, a := range attrs {
		if a.Name.Local != "style" {
			continue
		}
		if sv, ok := styleDecl(a.Value, prop); ok {
			v, set = sv, true
		}
	}
	return v, set
}

// styleDecl is the value of the last prop declaration in a style
// attribute.
func styleDecl(style, prop string) (string, bool) {
	v, set := "", false
	for decl := range strings.SplitSeq(style, ";") {
		k, val, found := strings.Cut(decl, ":")
		if found && strings.TrimSpace(k) == prop {
			v, set = strings.TrimSpace(val), true
		}
	}
	return v, set
}

// attrValue is the attribute name of an element, in no namespace.
func attrValue(attrs []xml.Attr, name string) (string, bool) {
	for _, a := range attrs {
		if a.Name.Space == "" && a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}

// clipURL is the id a url(#id) reference names.
func clipURL(v string) (string, bool) {
	inner, ok := strings.CutPrefix(v, "url(")
	if !ok {
		return "", false
	}
	inner, ok = strings.CutSuffix(inner, ")")
	if !ok {
		return "", false
	}
	inner = strings.Trim(strings.TrimSpace(inner), `"'`)
	id, ok := strings.CutPrefix(inner, "#")
	return id, ok && id != ""
}

// resolveRectRadii writes out the corner radius a rect leaves to the
// other: SVG rounds both axes by rx when ry is absent and by ry when
// rx is, but oksvg rounds only when both are given, so a rect with
// rx alone drew square corners. Clamping each radius to half its side
// is oksvg's (rasterx's AddRoundRect).
func resolveRectRadii(data []byte) []byte {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	var out []byte
	kept := 0
	for {
		start := int(d.InputOffset())
		tok, err := d.Token()
		if err != nil {
			break
		}
		t, ok := tok.(xml.StartElement)
		if !ok || t.Name.Local != "rect" {
			continue
		}
		rx, hasRx := attrValue(t.Attr, "rx")
		ry, hasRy := attrValue(t.Attr, "ry")
		if hasRx == hasRy {
			continue
		}
		attr, v := "ry", rx
		if hasRy {
			attr, v = "rx", ry
		}
		name := start + 1 + bytes.IndexAny(data[start+1:], " \t\r\n/>")
		out = append(out, data[kept:name]...)
		out = append(out, " "+attr+`="`+html.EscapeString(v)+`"`...)
		kept = name
	}
	if out == nil {
		return data
	}
	return append(out, data[kept:]...)
}
