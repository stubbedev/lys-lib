package gpu

import (
	"fmt"
	"image"
	"math"
	"unsafe"

	"github.com/go-text/typesetting/font"

	"github.com/stubbedev/lys-ui/internal/debug"
	"github.com/stubbedev/lys-ui/render"
	"github.com/stubbedev/lys-ui/third_party/vk"
)

// stepKind is what a recorded step does on the GPU.
type stepKind uint8

const (
	// stepDraw draws prims [first, first+count) into dst sampling tex.
	stepDraw stepKind = iota
	// stepCopy copies src's region into dst at the same place.
	stepCopy
	// stepDownload copies dst's region into the readback buffer.
	stepDownload
)

// step is one GPU operation of a frame, recorded in order.
type step struct {
	kind         stepKind
	dst, src     *surface
	tex          *surface
	replace      bool
	first, count uint32
	region       image.Rectangle
}

// upload copies staged bytes into a surface region. A segment's
// uploads run before its steps: their bytes never depend on drawing.
type upload struct {
	dst       *surface
	region    image.Rectangle
	offset    int
	rowLength int
}

// frame plays a render.List onto a target: the drawing calls the GPU
// does natively become primitives, the rest the CPU rasterizes into
// sprites the GPU draws, and the layer calls the GPU cannot do it runs
// on the CPU over the layer's pixels read back. It is a render.Player;
// the first error stops it, and Render reports it.
type frame struct {
	d          *Device
	target     *Target
	num, denom int
	prims      []prim
	// words are the primitives' variable-length data: gradient stops.
	words   []uint32
	steps   []step
	uploads []upload
	staging []byte
	// downloads is how many bytes the segment's downloads take.
	downloads int
	// stats counts what the frame drew how, for the gpu debug line.
	stats frameStats
	// page is the sprite page being packed: x, y and the shelf height.
	px, py, shelf int
	err           error
}

// Render draws l onto t and hands t back to its consumer. l is
// recorded at t's size; its layers keep their GPU surfaces on this
// device across frames.
func (d *Device) Render(l *render.List, t *Target) error {
	d.freeDeadLayers()
	d.retireImages()
	if err := d.ensureScratch(t.s.w, t.s.h); err != nil {
		return err
	}
	if err := t.s.ready(); err != nil {
		return err
	}
	num, denom := l.Scale()
	f := &d.frame
	f.d, f.target, f.num, f.denom, f.err = d, t, num, denom, nil
	f.clear()
	if debug.Enabled {
		f.stats = frameStats{sprites: map[string]int{}}
	}
	l.Play(f)
	if f.err != nil {
		return f.err
	}
	if debug.Enabled {
		f.stats.prims += len(f.prims)
	}
	err := f.flush(true)
	if debug.Enabled {
		debug.Log("gpu", "frame %dx%d: %d prims, %d segments, %d readbacks, sprite pixels %v", t.s.w, t.s.h, f.stats.prims, f.stats.segments, f.stats.readbacks, f.stats.sprites)
	}
	return err
}

// frameStats is what a frame drew how: its primitives, the segments it
// submitted in, its readbacks, and the pixels of CPU sprites per call.
type frameStats struct {
	prims, segments, readbacks int
	sprites                    map[string]int
}

// fail records the frame's first error.
func (f *frame) fail(err error) {
	if f.err == nil {
		f.err = err
	}
}

// on is the surface s names: the target, or a layer's.
func (f *frame) on(s render.State) *surface {
	if s.Layer == nil {
		return &f.target.s
	}
	return f.layer(s.Layer)
}

// layer is the GPU surface backing the recorded l.
func (f *frame) layer(l *render.Layer) *surface {
	s, ok := l.Backing().(*layerSurface)
	if !ok {
		panic(fmt.Sprintf("gpu: a recorded layer backed by %T, not a GPU surface", l.Backing()))
	}
	return s.s
}

// draw adds p, drawn into dst sampling tex (nil for none), to the
// frame, joining the previous draw when it shares all three.
func (f *frame) draw(dst, tex *surface, replace bool, p prim) {
	if f.err != nil {
		return
	}
	if tex != nil {
		if err := tex.ready(); err != nil {
			f.fail(err)
			return
		}
	}
	f.prims = append(f.prims, p)
	if n := len(f.steps); n > 0 {
		last := &f.steps[n-1]
		if last.kind == stepDraw && last.dst == dst && last.tex == tex && last.replace == replace {
			last.count++
			return
		}
	}
	f.steps = append(f.steps, step{kind: stepDraw, dst: dst, tex: tex, replace: replace, first: uint32(len(f.prims) - 1), count: 1})
}

// sprite rasterizes one call on the CPU, within s's bounds, and draws
// the result: exact for the source-over calls, whose pixels blended
// onto transparency and then over the surface are the pixels blended
// over it directly.
func (f *frame) sprite(s render.State, call string, paint func(cv *render.Canvas)) {
	b := s.Bounds
	if f.err != nil || b.Empty() {
		return
	}
	if debug.Enabled {
		f.stats.sprites[call] += b.W * b.H
	}
	at, ok := f.place(b.W, b.H)
	if !ok {
		if f.err = f.flush(false); f.err != nil {
			return
		}
		at, _ = f.place(b.W, b.H)
	}
	sc := f.d.scratchCanvas(0, f.target.s.w, f.target.s.h, f.num, f.denom)
	sc.cv.ApplyState(render.State{Clip: b, Alpha: s.Alpha, Bright: s.Bright})
	paint(sc.cv)
	f.uploads = append(f.uploads, upload{dst: f.d.page, region: image.Rect(at.X, at.Y, at.X+b.W, at.Y+b.H), offset: len(f.staging), rowLength: b.W})
	f.staging = sc.take(f.staging, b)
	p := prim{quad: quadOf(b), inner: [4]float32{float32(b.X - at.X), float32(b.Y - at.Y)}, info: [4]uint32{kindTexture, 255, 0, flagRaw}}
	f.draw(f.on(s), f.d.page, false, p)
}

// place packs a w x h sprite onto the page, false when it is full.
func (f *frame) place(w, h int) (image.Point, bool) {
	pw, ph := f.d.page.w, f.d.page.h
	if f.px+w > pw {
		f.px, f.py, f.shelf = 0, f.py+f.shelf, 0
	}
	if f.py+h > ph {
		return image.Point{}, false
	}
	at := image.Pt(f.px, f.py)
	f.px += w
	f.shelf = max(f.shelf, h)
	return at, true
}

// cpu runs fn, a layer call the GPU does not do, on the CPU: it reads
// back the regions of the surfaces fn reads (the first is the one it
// writes) into scratch canvases, runs fn over them, and uploads the
// written region back.
func (f *frame) cpu(region render.Rect, surfaces []*surface, fn func(cvs []*render.Canvas)) {
	region = region.Intersect(render.Rect{W: f.target.s.w, H: f.target.s.h})
	if f.err != nil || region.Empty() {
		return
	}
	r := rectOf(region)
	offsets := make([]int, len(surfaces))
	for i, s := range surfaces {
		offsets[i] = f.downloads
		f.steps = append(f.steps, step{kind: stepDownload, dst: s, region: r, first: uint32(f.downloads)})
		f.downloads += r.Dx() * r.Dy() * 4
	}
	if debug.Enabled {
		f.stats.readbacks++
		f.stats.prims += len(f.prims)
	}
	if f.err = f.flush(false); f.err != nil {
		return
	}
	cvs := make([]*render.Canvas, len(surfaces))
	for i := range surfaces {
		sc := f.d.scratchCanvas(i, f.target.s.w, f.target.s.h, f.num, f.denom)
		sc.put(region, f.d.readback.mapped[offsets[i]:])
		cvs[i] = sc.cv
	}
	fn(cvs)
	f.uploads = append(f.uploads, upload{dst: surfaces[0], region: r, offset: len(f.staging), rowLength: r.Dx()})
	f.staging = f.d.scratches[0].take(f.staging, region)
	for _, sc := range f.d.scratches[1:len(surfaces)] {
		sc.clear(region)
	}
}

// flush runs the recorded segment on the GPU and waits for it; final
// hands the target back to its consumer.
func (f *frame) flush(final bool) error {
	d := f.d
	var err error
	if d.prims, err = d.prims.fit(len(f.prims) * primSize); err != nil {
		return err
	}
	copy(d.prims.mapped, unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(f.prims))), len(f.prims)*primSize))
	if d.words, err = d.words.fit(len(f.words) * 4); err != nil {
		return err
	}
	copy(d.words.mapped, unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(f.words))), len(f.words)*4))
	d.bindPrims(d.prims, d.words)
	if d.staging, err = d.staging.fit(len(f.staging)); err != nil {
		return err
	}
	copy(d.staging.mapped, f.staging)
	if d.readback, err = d.readback.fit(f.downloads); err != nil {
		return err
	}
	if debug.Enabled {
		f.stats.segments++
	}
	err = d.submit(func(cb vk.CommandBuffer) { f.record(cb, final) })
	f.clear()
	return err
}

// clear empties the recorded segment, keeping its slices' capacity.
func (f *frame) clear() {
	f.prims, f.words, f.steps, f.uploads, f.staging = f.prims[:0], f.words[:0], f.steps[:0], f.uploads[:0], f.staging[:0]
	f.px, f.py, f.shelf, f.downloads = 0, 0, 0, 0
}

// record records the segment: its uploads, then its steps, draws
// sharing a surface in one render pass.
func (f *frame) record(cb vk.CommandBuffer, final bool) {
	d := f.d
	f.target.acquire(cb)
	for _, u := range f.uploads {
		u.dst.upload(cb, u.region, d.staging, u.offset, u.rowLength)
	}
	var pass *surface
	var pipe vk.Pipeline
	var tex *surface
	end := func() {
		if pass != nil {
			d.cmds.CmdEndRenderPass(cb)
			pass = nil
		}
	}
	for i := range f.steps {
		st := &f.steps[i]
		switch st.kind {
		case stepCopy:
			end()
			st.src.to(cb, vk.ImageLayoutTransferSrcOptimal)
			st.dst.to(cb, vk.ImageLayoutTransferDstOptimal)
			layers := vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1}
			at := vk.Offset3D{X: int32(st.region.Min.X), Y: int32(st.region.Min.Y)}
			ic := vk.ImageCopy{
				SrcSubresource: layers, SrcOffset: at, DstSubresource: layers, DstOffset: at,
				Extent: vk.Extent3D{Width: uint32(st.region.Dx()), Height: uint32(st.region.Dy()), Depth: 1},
			}
			d.cmds.CmdCopyImage(cb, st.src.image, vk.ImageLayoutTransferSrcOptimal, st.dst.image, vk.ImageLayoutTransferDstOptimal, 1, &ic)
		case stepDownload:
			end()
			st.dst.download(cb, st.region, d.readback, int(st.first))
		case stepDraw:
			sampled := st.tex
			if sampled == nil {
				sampled = d.blank
			}
			if sampled.layout != vk.ImageLayoutShaderReadOnlyOptimal {
				end()
				sampled.to(cb, vk.ImageLayoutShaderReadOnlyOptimal)
			}
			if pass != st.dst {
				end()
				f.begin(cb, st.dst)
				pass, pipe, tex = st.dst, 0, nil
			}
			want := d.over
			if st.replace {
				want = d.replace
			}
			if pipe != want {
				d.cmds.CmdBindPipeline(cb, vk.PipelineBindPointGraphics, want)
				pipe = want
			}
			if tex != sampled {
				d.cmds.CmdBindDescriptorSets(cb, vk.PipelineBindPointGraphics, d.layout, 1, 1, &sampled.set, 0, nil)
				tex = sampled
			}
			d.cmds.CmdDraw(cb, 4, st.count, 0, st.first)
		}
	}
	end()
	if final {
		f.target.release(cb)
	}
}

// begin opens a render pass on s with the frame's viewport and
// bindings.
func (f *frame) begin(cb vk.CommandBuffer, s *surface) {
	d := f.d
	s.to(cb, vk.ImageLayoutColorAttachmentOptimal)
	area := vk.Rect2D{Extent: vk.Extent2D{Width: uint32(s.w), Height: uint32(s.h)}}
	bi := vk.RenderPassBeginInfo{SType: vk.StructureTypeRenderPassBeginInfo, RenderPass: d.pass, Framebuffer: s.fb, RenderArea: area}
	d.cmds.CmdBeginRenderPass(cb, &bi, vk.SubpassContentsInline)
	vp := vk.Viewport{Width: float32(s.w), Height: float32(s.h), MaxDepth: 1}
	d.cmds.CmdSetViewport(cb, 0, 1, &vp)
	d.cmds.CmdSetScissor(cb, 0, 1, &area)
	d.cmds.CmdBindDescriptorSets(cb, vk.PipelineBindPointGraphics, d.layout, 0, 1, &d.primSet, 0, nil)
	fc := frameConstants{w: float32(s.w), h: float32(s.h)}
	d.cmds.CmdPushConstants(cb, d.layout, vk.ShaderStageFlags(vk.ShaderStageVertexBit|vk.ShaderStageFragmentBit), 0, uint32(unsafe.Sizeof(fc)), unsafe.Pointer(&fc))
}

func (f *frame) ClearDevice(s render.State, r render.Rect, col render.Color) {
	p := prim{quad: quadOf(s.Bounds), cols: [4]uint32{uint32(col)}, info: [4]uint32{kindRect, 255, 0, flagRaw}}
	f.draw(f.on(s), nil, true, p)
}

func (f *frame) FillRectDevice(s render.State, r render.Rect, col render.Color) {
	if col == 0 {
		return
	}
	p := shaded(kindRect, s, 0)
	p.quad, p.cols[0] = quadOf(s.Bounds), uint32(col)
	f.draw(f.on(s), nil, false, p)
}

func (f *frame) DrawCanvasDevice(s render.State, src *render.Canvas, x, y int) {
	f.sprite(s, "DrawCanvasDevice", func(cv *render.Canvas) { cv.DrawCanvasDevice(src, x, y) })
}

func (f *frame) DrawLayerAt(s render.State, src *render.Layer, x, y int) {
	p := shaded(kindTexture, s, flagOpaqueStores)
	p.quad, p.inner = quadOf(s.Bounds), [4]float32{float32(x), float32(y)}
	f.draw(f.on(s), f.layer(src), false, p)
}

func (f *frame) RoundedRect(s render.State, r render.Rect, radius int, col render.Color) {
	b := s.Bounds
	rad := min(float64(radius)*float64(f.num)/float64(f.denom), float64(min(b.W, b.H))/2)
	p := shaded(kindRounded, s, 0)
	p.quad, p.box, p.radii[0], p.cols[0] = quadOf(b), quadOf(b), float32(rad), uint32(col)
	f.draw(f.on(s), nil, false, p)
}

func (f *frame) Line(s render.State, x0, y0, x1, y1, width int, col render.Color) {
	if width < 1 {
		return
	}
	g := render.LineOf(x0, y0, x1, y1, width, f.num, f.denom)
	p := shaded(kindLine, s, 0)
	p.quad, p.cols[0] = quadOf(s.Bounds), uint32(col)
	p.box, p.radii = [4]float32{g.AX, g.AY, g.DX, g.DY}, [4]float32{g.InvLen2, g.Half}
	f.draw(f.on(s), nil, false, p)
}

func (f *frame) Shadow(s render.State, r render.Rect, cornerRadius, blur int, col render.Color) {
	f.sprite(s, "Shadow", func(cv *render.Canvas) { cv.Shadow(r, cornerRadius, blur, col) })
}

func (f *frame) DrawImage(s render.State, img image.Image, x, y int) {
	b := img.Bounds()
	f.scaledImage(s, img, render.MapRect(render.Rect{X: x, Y: y, W: b.Dx(), H: b.Dy()}, f.num, f.denom), func(cv *render.Canvas) { cv.DrawImage(img, x, y) })
}

// scaledImage draws img nearest-sampled into the device rect full, as
// render's DrawImage and DrawImageRect map it: a pixel's texel is its
// offset into full scaled by the image's size over full's, in integers.
// An image other than *image.RGBA draws as a sprite through raster.
func (f *frame) scaledImage(s render.State, img image.Image, full render.Rect, raster func(cv *render.Canvas)) {
	rgba, ok := img.(*image.RGBA)
	var tex *surface
	if ok && f.err == nil {
		tex, ok = f.imageTexture(rgba)
	}
	if !ok {
		f.sprite(s, "scaled image", raster)
		return
	}
	p := shaded(kindImage, s, 0)
	p.quad, p.box = quadOf(s.Bounds), [4]float32{float32(full.X), float32(full.Y), float32(full.W), float32(full.H)}
	p.radii = [4]float32{float32(tex.w), float32(tex.h)}
	f.draw(f.on(s), tex, false, p)
}

func (f *frame) DrawImageCover(s render.State, img image.Image, r render.Rect, radii render.Corners) {
	f.sprite(s, "DrawImageCover", func(cv *render.Canvas) { cv.DrawImageCover(img, r, radii) })
}

func (f *frame) DrawImageDevice(s render.State, img image.Image, x, y int) {
	rgba, ok := img.(*image.RGBA)
	var tex *surface
	if ok && f.err == nil {
		tex, ok = f.imageTexture(rgba)
	}
	if !ok {
		f.sprite(s, "DrawImageDevice", func(cv *render.Canvas) { cv.DrawImageDevice(img, x, y) })
		return
	}
	p := shaded(kindTexture, s, flagOpaqueStores)
	p.quad, p.inner = quadOf(s.Bounds), [4]float32{float32(x), float32(y)}
	f.draw(f.on(s), tex, false, p)
}

func (f *frame) DrawImageRect(s render.State, img image.Image, r render.Rect) {
	f.scaledImage(s, img, s.Bounds, func(cv *render.Canvas) { cv.DrawImageRect(img, r) })
}

func (f *frame) DrawIcon(s render.State, ic *render.Icon, x, y int, m render.Affine) {
	if m == render.Identity {
		f.DrawImage(s, ic.Image(), x, y)
		return
	}
	mp, invertible := render.MappedOf(ic.Xform(x, y, m, f.num, f.denom))
	img := ic.Image()
	tex, ok := (*surface)(nil), invertible && f.err == nil
	if ok {
		tex, ok = f.imageTexture(img)
	}
	if !ok {
		f.sprite(s, "DrawIcon", func(cv *render.Canvas) { ic.DrawXformed(cv, x, y, m) })
		return
	}
	p := shaded(kindComposite, s, 0)
	p.quad, p.inner = quadOf(s.Bounds), [4]float32{0, 0, float32(img.Rect.Dx()), float32(img.Rect.Dy())}
	p.m0, p.m1, p.m2, p.widths = mappedRows(&mp)
	f.draw(f.on(s), tex, false, p)
}

func (f *frame) Arc(s render.State, cx, cy, radius, width, start, sweep float64, col render.Color) {
	f.sprite(s, "Arc", func(cv *render.Canvas) { cv.Arc(cx, cy, radius, width, start, sweep, col) })
}

func (f *frame) BoxShadow(s render.State, r render.Rect, radii render.Corners, sh render.BoxShadow) {
	if f.err != nil || sh.Color.A() == 0 {
		return
	}
	m, box, kw, kh := render.BoxShadowMask(r, radii, sh, f.num, f.denom)
	pos, ok := f.maskSlot(m)
	if !ok {
		f.sprite(s, "BoxShadow", func(cv *render.Canvas) { cv.BoxShadow(r, radii, sh) })
		return
	}
	x0, y0 := m.Origin()
	p := shaded(kindStretch, s, 0)
	p.quad, p.box, p.cols[0] = quadOf(s.Bounds), [4]float32{float32(box.X), float32(box.Y), float32(box.W), float32(box.H)}, uint32(sh.Color)
	p.radii, p.inner = [4]float32{float32(kw), float32(kh)}, [4]float32{float32(pos.X - x0), float32(pos.Y - y0)}
	f.draw(f.on(s), f.d.masks.page, false, p)
}

func (f *frame) PaintGrain(s render.State, r render.Rect, radii render.Corners, opacity float64, ox, oy int) {
	f.sprite(s, "PaintGrain", func(cv *render.Canvas) { cv.PaintGrain(r, radii, opacity, ox, oy) })
}

func (f *frame) RoundedBorderSides(s render.State, r render.Rect, radii render.Corners, widths render.Insets, cols [4]render.Color) {
	if widths.Zero() {
		return
	}
	outer, oradii, inner, iradii, w := render.BorderRing(r, radii, widths, f.num, f.denom)
	p := shaded(kindBorder, s, 0)
	p.quad, p.box, p.radii, p.inner, p.iradii, p.widths = quadOf(s.Bounds), vec(outer), vec(oradii), vec(inner), vec(iradii), render.SideReciprocals(w)
	p.cols = [4]uint32{uint32(cols[0]), uint32(cols[1]), uint32(cols[2]), uint32(cols[3])}
	f.draw(f.on(s), nil, false, p)
}

func (f *frame) RoundedRectCorners(s render.State, r render.Rect, radii render.Corners, col render.Color) {
	if col == 0 {
		return
	}
	box, rc := render.DeviceBox(r, radii, f.num, f.denom)
	p := shaded(kindRRect, s, 0)
	p.quad, p.box, p.radii, p.cols[0] = quadOf(s.Bounds), vec(box), vec(rc), uint32(col)
	f.draw(f.on(s), nil, false, p)
}

func (f *frame) PaintGradient(s render.State, r render.Rect, radii render.Corners, g render.Gradient) {
	if len(g.Stops) == 0 {
		return
	}
	m, ok := g.DeviceMap(r, radii, f.num, f.denom)
	if !ok {
		return
	}
	box, rc := render.DeviceBox(r, radii, f.num, f.denom)
	p := shaded(kindGradient, s, 0)
	p.quad, p.box, p.radii = quadOf(s.Bounds), vec(box), vec(rc)
	p.m0 = [4]float32{float32(m.CX), float32(m.CY), float32(m.DX), float32(m.DY)}
	p.m1 = [4]float32{m.InvLength, m.InvRX, m.InvRY, float32(m.From)}
	p.m2 = [4]float32{float32(m.Kind), float32(g.Extend), float32(len(f.words)), float32(len(g.Stops))}
	// The stops as the shader reads them: the stop range's reciprocal,
	// then each stop's position, color, and its segment's reciprocal.
	lo, hi := float32(g.Stops[0].Pos), float32(g.Stops[len(g.Stops)-1].Pos)
	f.words = append(f.words, math.Float32bits(render.Reciprocal(hi-lo)))
	prev := lo
	for _, st := range g.Stops {
		pos, inv := float32(st.Pos), float32(0)
		if pos-prev > 0 {
			inv = render.Reciprocal(pos - prev)
		}
		f.words = append(f.words, math.Float32bits(pos), uint32(st.Color), math.Float32bits(inv))
		prev = pos
	}
	f.draw(f.on(s), nil, false, p)
}

func (f *frame) PaintGradientTiles(s render.State, g render.Gradient, t render.Tile, clip render.Rect, radii render.Corners) {
	f.sprite(s, "PaintGradientTiles", func(cv *render.Canvas) { cv.PaintGradientTiles(g, t, clip, radii) })
}

func (f *frame) PaintImageTiles(s render.State, img image.Image, t render.Tile, clip render.Rect, radii render.Corners) {
	f.sprite(s, "PaintImageTiles", func(cv *render.Canvas) { cv.PaintImageTiles(img, t, clip, radii) })
}

func (f *frame) FillPath(s render.State, p *render.Path, col render.Color) {
	f.sprite(s, "FillPath", func(cv *render.Canvas) { cv.FillPath(p, col) })
}

func (f *frame) Glyph(s render.State, face *render.Typeface, gid font.GID, scale, penX, penY float64, col render.Color) {
	if f.err != nil {
		return
	}
	m, at, ok := render.GlyphCoverage(face, gid, scale, penX, penY, col)
	var pos image.Point
	if ok {
		pos, ok = f.maskSlot(m)
	}
	if !ok {
		f.sprite(s, "Glyph", func(cv *render.Canvas) { cv.DrawGlyph(face, gid, scale, penX, penY, col) })
		return
	}
	p := shaded(kindMask, s, 0)
	p.quad, p.inner, p.cols[0] = quadOf(s.Bounds), [4]float32{float32(at.X - pos.X), float32(at.Y - pos.Y)}, uint32(col)
	f.draw(f.on(s), f.d.masks.page, false, p)
}

func (f *frame) Layer(s render.State, l *render.Layer, region render.Rect) {
	f.readyLayer(l, region, false)
}

func (f *frame) ScratchLayer(s render.State, l *render.Layer, region render.Rect) {
	f.readyLayer(l, region, true)
}

// readyLayer backs l with a target-sized surface, its own when it
// fits, else a pooled one (scratch) or a new one, and clears region;
// a new surface clears whole, as a new raster layer starts.
func (f *frame) readyLayer(l *render.Layer, region render.Rect, scratch bool) {
	if f.err != nil {
		return
	}
	w, h := f.target.s.w, f.target.s.h
	ls, _ := l.Backing().(*layerSurface)
	if ls == nil || ls.s.w != w || ls.s.h != h {
		if ls != nil {
			f.d.dropLayer(ls)
		}
		var err error
		if ls, err = f.d.takeLayer(l, w, h, scratch); err != nil {
			f.fail(err)
			return
		}
		region = render.Rect{W: w, H: h}
	}
	if err := ls.s.ready(); err != nil {
		f.fail(err)
		return
	}
	clear := region.Intersect(render.Rect{W: w, H: h})
	if clear.Empty() {
		return
	}
	f.draw(ls.s, nil, true, prim{quad: quadOf(clear), info: [4]uint32{kindRect, 255, 0, flagRaw}})
}

func (f *frame) Snapshot(s render.State, l *render.Layer, region render.Rect) {
	r := region.Intersect(render.Rect{W: f.target.s.w, H: f.target.s.h})
	if f.err != nil || r.Empty() {
		return
	}
	f.steps = append(f.steps, step{kind: stepCopy, src: f.on(s), dst: f.layer(l), region: rectOf(r)})
}

func (f *frame) Release(l *render.Layer) {
	ls, ok := l.Backing().(*layerSurface)
	if !ok {
		panic(fmt.Sprintf("gpu: Release of a layer backed by %T", l.Backing()))
	}
	f.d.poolLayer(ls)
	l.SetBacking(nil)
}

func (f *frame) Composite(s render.State, l *render.Layer, src render.Rect, m render.Affine, alpha float64) {
	f.composite(s, l, src, render.Affine3(m), alpha)
}

func (f *frame) CompositeProjective(s render.State, l *render.Layer, src render.Rect, m render.M3, alpha float64) {
	f.composite(s, l, src, m, alpha)
}

// composite is Composite and CompositeProjective: each covered pixel
// inverse-maps into src and samples it bilinearly.
func (f *frame) composite(s render.State, l *render.Layer, src render.Rect, m render.M3, alpha float64) {
	ls := f.layer(l)
	src = src.Intersect(render.Rect{W: ls.w, H: ls.h})
	mp, ok := render.MappedOf(m)
	a := s.Alpha * min(1, max(0, alpha))
	if src.Empty() || !ok || alphaScale(a) == 0 {
		return
	}
	s.Alpha = a
	p := shaded(kindComposite, s, 0)
	p.quad, p.inner = quadOf(s.Bounds.Intersect(render.Rect{W: f.target.s.w, H: f.target.s.h})), quadOf(src)
	p.m0, p.m1, p.m2, p.widths = mappedRows(&mp)
	f.draw(f.on(s), ls, false, p)
}

func (f *frame) DrawLayer(s render.State, l *render.Layer, src render.Rect, mode render.BlendMode) {
	if mode != render.BlendNormal {
		dst := f.on(s)
		f.cpu(s.Bounds, []*surface{dst, f.layer(l)}, func(cvs []*render.Canvas) {
			cvs[0].ApplyState(s)
			cvs[0].DrawLayer(render.LayerOf(cvs[1]), src, mode)
		})
		return
	}
	p := shaded(kindTexture, s, flagNoBright)
	p.quad = quadOf(s.Bounds)
	f.draw(f.on(s), f.layer(l), false, p)
}

func (f *frame) CrossFade(l, other *render.Layer, region render.Rect, t float64) {
	f.cpu(region, []*surface{f.layer(l), f.layer(other)}, func(cvs []*render.Canvas) {
		render.LayerOf(cvs[0]).CrossFade(render.LayerOf(cvs[1]), region, t)
	})
}

func (f *frame) ApplyMaskLayer(l, mask *render.Layer, region render.Rect, luminance bool) {
	f.cpu(region, []*surface{f.layer(l), f.layer(mask)}, func(cvs []*render.Canvas) {
		render.LayerOf(cvs[0]).ApplyMaskLayer(render.LayerOf(cvs[1]), region, luminance)
	})
}

func (f *frame) ApplyPathMask(l *render.Layer, p *render.Path, region render.Rect) {
	f.cpu(region, []*surface{f.layer(l)}, func(cvs []*render.Canvas) {
		render.LayerOf(cvs[0]).ApplyPathMask(p, region)
	})
}

func (f *frame) ApplyFilters(l *render.Layer, region render.Rect, fl *render.Filters, shape render.FilterShape) {
	f.cpu(region, []*surface{f.layer(l)}, func(cvs []*render.Canvas) {
		render.ApplyFilters(render.LayerOf(cvs[0]), region, fl, shape)
	})
}
