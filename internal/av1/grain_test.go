package av1

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// planes are a 4:2:0 picture's samples: luma, then Cb and Cr at half
// the size, rounded up.
type planes struct {
	w, h    int
	y, u, v []int32
}

// readPlanes splits raw, ffmpeg's yuv420p or yuv420p10le frames of
// w x h, into pictures.
func readPlanes(t *testing.T, raw []byte, w, h, bitDepth int) []planes {
	t.Helper()
	bps := (bitDepth + 7) / 8
	cw, ch := (w+1)/2, (h+1)/2
	size := (w*h + 2*cw*ch) * bps
	if len(raw) == 0 || len(raw)%size != 0 {
		t.Fatalf("ffmpeg decoded %d bytes, no whole number of %d-byte frames", len(raw), size)
	}
	sample := func(b []byte, i int) int32 {
		if bps == 1 {
			return int32(b[i])
		}
		return int32(binary.LittleEndian.Uint16(b[2*i:]))
	}
	var out []planes
	for ; len(raw) > 0; raw = raw[size:] {
		p := planes{w: w, h: h, y: make([]int32, w*h), u: make([]int32, cw*ch), v: make([]int32, cw*ch)}
		for i := range p.y {
			p.y[i] = sample(raw, i)
		}
		for i := range p.u {
			p.u[i], p.v[i] = sample(raw[w*h*bps:], i), sample(raw[(w*h+cw*ch)*bps:], i)
		}
		out = append(out, p)
	}
	return out
}

// addNoise is the add noise synthesis process (7.18.3.5) as the
// specification writes it: the noise stripes, the noise image, then
// the noise scaled onto p's samples.
func addNoise(g *FilmGrain, t *Grain, p *planes, bitDepth int, identity bool) {
	center := int32(128) << (bitDepth - 8)
	lo, hi := -center, int32(256)<<(bitDepth-8)-1-center
	clip := func(v int32) int32 { return max(lo, min(hi, v)) }
	cw, ch := (p.w+1)/2, (p.h+1)/2
	rows := len(t.Offsets) / t.Stride
	type stripe [3][][]int32
	stripes := make([]stripe, rows)
	templates := [3]struct {
		grain  []int32
		stride int
	}{{t.Luma[:], GrainLumaWidth}, {t.Cb[:], GrainChromaWidth}, {t.Cr[:], GrainChromaWidth}}
	for num := range stripes {
		for plane := range 3 {
			sub := min(plane, 1)
			stripes[num][plane] = make([][]int32, 34>>sub)
			for i := range stripes[num][plane] {
				stripes[num][plane][i] = make([]int32, (t.Stride*32+34)>>sub)
			}
		}
		for block := range t.Stride {
			x := block * 16
			rand := t.Offsets[num*t.Stride+block]
			offsetX, offsetY := int(rand>>4), int(rand&15)
			for plane := range 3 {
				sub := min(plane, 1)
				planeX, planeY := 9+offsetX*2, 9+offsetY*2
				if sub == 1 {
					planeX, planeY = 6+offsetX, 6+offsetY
				}
				tpl := templates[plane]
				for i := range 34 >> sub {
					for j := range 34 >> sub {
						v := tpl.grain[(planeY+i)*tpl.stride+planeX+j]
						if sub == 0 {
							at := &stripes[num][plane][i][x*2+j]
							if j < 2 && g.OverlapFlag && x > 0 {
								if j == 0 {
									v = *at*27 + v*17
								} else {
									v = *at*17 + v*27
								}
								v = clip(round2(v, 5))
							}
							*at = v
							continue
						}
						at := &stripes[num][plane][i][x+j]
						if j == 0 && g.OverlapFlag && x > 0 {
							v = clip(round2(*at*23+v*22, 5))
						}
						*at = v
					}
				}
			}
		}
	}
	noise := func(plane, x, y int) int32 {
		sub := min(plane, 1)
		num := y >> (5 - sub)
		i := y - num<<(5-sub)
		v := stripes[num][plane][i][x]
		if !g.OverlapFlag || num == 0 {
			return v
		}
		switch {
		case sub == 0 && i < 2:
			old := stripes[num-1][plane][i+32][x]
			if i == 0 {
				v = old*27 + v*17
			} else {
				v = old*17 + v*27
			}
			return clip(round2(v, 5))
		case sub == 1 && i < 1:
			return clip(round2(stripes[num-1][plane][i+16][x]*23+v*22, 5))
		}
		return v
	}
	minValue, maxLuma, maxChroma := int32(0), int32(256)<<(bitDepth-8)-1, int32(256)<<(bitDepth-8)-1
	if g.ClipToRestrictedRange {
		minValue, maxLuma, maxChroma = 16<<(bitDepth-8), 235<<(bitDepth-8), 240<<(bitDepth-8)
		if identity {
			maxChroma = maxLuma
		}
	}
	scale := func(plane int, index int32) int32 {
		shift := uint(bitDepth - 8)
		x := index >> shift
		rem := index - x<<shift
		if bitDepth == 8 || x == 255 {
			return t.Scaling[plane][x]
		}
		start, end := t.Scaling[plane][x], t.Scaling[plane][x+1]
		return start + round2((end-start)*rem, shift)
	}
	scalingShift := uint(g.ScalingMinus8) + 8
	for y := range ch {
		for x := range cw {
			lumaX, lumaY := x<<1, y<<1
			average := round2(p.y[lumaY*p.w+lumaX]+p.y[lumaY*p.w+min(lumaX+1, p.w-1)], 1)
			for plane, c := range []struct {
				samples                []int32
				points                 uint8
				lumaMult, mult, offset int32
			}{
				{p.u, g.NumCbPoints, int32(g.CbLumaMult), int32(g.CbMult), int32(g.CbOffset)},
				{p.v, g.NumCrPoints, int32(g.CrLumaMult), int32(g.CrMult), int32(g.CrOffset)},
			} {
				if c.points == 0 && !g.ChromaScalingFromLuma {
					continue
				}
				orig := c.samples[y*cw+x]
				merged := average
				if !g.ChromaScalingFromLuma {
					combined := average*(c.lumaMult-128) + orig*(c.mult-128)
					merged = max(0, min(int32(1)<<bitDepth-1, combined>>6+(c.offset-256)<<(bitDepth-8)))
				}
				n := round2(scale(plane+1, merged)*noise(plane+1, x, y), scalingShift)
				c.samples[y*cw+x] = max(minValue, min(maxChroma, orig+n))
			}
		}
	}
	if g.NumYPoints == 0 {
		return
	}
	for y := range p.h {
		for x := range p.w {
			orig := p.y[y*p.w+x]
			n := round2(scale(0, orig)*noise(0, x, y), scalingShift)
			p.y[y*p.w+x] = max(minValue, min(maxLuma, orig+n))
		}
	}
}

// grains are the film grain parameters of the frames a stream shows, in
// the order it shows them.
func grains(t *testing.T, b []byte) ([]FilmGrain, *Stream) {
	t.Helper()
	var s Stream
	var out []FilmGrain
	var h FrameHeader
	var tiles []Tile
	walk := NewOBUs(b)
	for u, ok := walk.Next(); ok; u, ok = walk.Next() {
		switch u.Type {
		case OBUSequenceHeader:
			if err := s.Seq.Parse(u.Payload); err != nil {
				t.Fatal(err)
			}
		case OBUFrameHeader, OBUFrame:
			if err := s.ParseFrameHeader(&h, &u); err != nil {
				t.Fatal(err)
			}
			if h.ShowFrame {
				out = append(out, h.FilmGrain)
			}
			tiles = tiles[:0]
			if h.ShowExistingFrame {
				s.Update(&h)
				continue
			}
			if u.Type == OBUFrame {
				tiles = checkTiles(t, &s, &h, u.Payload[h.HeaderBytes:], tiles)
			}
		case OBUTileGroup:
			tiles = checkTiles(t, &s, &h, u.Payload, tiles)
		}
	}
	if err := walk.Err(); err != nil {
		t.Fatal(err)
	}
	return out, &s
}

// The film grain a stream's frame headers describe, synthesized as
// 7.18.3 says onto the frames ffmpeg (dav1d) decodes without it, is
// what ffmpeg decodes with it, sample for sample: libaom's estimated
// grain and its test vectors, which cover grain in every plane, chroma
// scaled from luma, each autoregression lag, overlapped and clipped
// noise, at 8 and 10 bits and at an odd size.
func TestFilmGrainMatchesFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg to make and decode the streams")
	}
	type clip struct {
		name     string
		w, h     int
		bitDepth int
		params   string
	}
	clips := []clip{{"estimated", 192, 128, 8, "denoise-noise-level=10"}, {"estimated, 10 bits, odd size", 271, 153, 10, "denoise-noise-level=10"}}
	for n := 1; n <= 16; n++ {
		clips = append(clips, clip{fmt.Sprintf("test vector %d", n), 192, 128, 8 + 2*(n%2), fmt.Sprintf("film-grain-test=%d", n)})
	}
	for _, c := range clips {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "a.obu")
			format := "yuv420p"
			if c.bitDepth == 10 {
				format = "yuv420p10le"
			}
			src := fmt.Sprintf("testsrc2=size=%dx%d:rate=25:duration=0.4,crop=%d:%d:0:0:exact=1", c.w+c.w%2, c.h+c.h%2, c.w, c.h)
			run(t, "-v", "error", "-y", "-f", "lavfi", "-i", src, "-pix_fmt", format, "-c:v", "libaom-av1", "-cpu-used", "6", "-g", "6", "-aom-params", c.params, "-f", "obu", path)
			decode := func(flags ...string) []planes {
				args := append([]string{"-v", "error"}, flags...)
				cmd := exec.Command("ffmpeg", append(args, "-i", path, "-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", format, "-")...)
				cmd.Stderr = os.Stderr
				raw, err := cmd.Output()
				if err != nil {
					t.Fatalf("ffmpeg decoding with %v: %v", flags, err)
				}
				return readPlanes(t, raw, c.w, c.h, c.bitDepth)
			}
			want, clean := decode(), decode("-export_side_data", "film_grain")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			params, s := grains(t, b)
			if len(params) != len(want) || len(clean) != len(want) {
				t.Fatalf("%d frames shown, ffmpeg decodes %d with grain and %d without", len(params), len(want), len(clean))
			}
			var grain Grain
			applied, differing := 0, 0
			for i := range clean {
				g := &params[i]
				if g.ApplyGrain {
					applied++
					g.Synthesize(&grain, c.bitDepth, c.w, c.h)
					addNoise(g, &grain, &clean[i], c.bitDepth, s.Seq.Color.MatrixCoefficients == 0)
				}
				for plane, got := range [][]int32{clean[i].y, clean[i].u, clean[i].v} {
					ref := [][]int32{want[i].y, want[i].u, want[i].v}[plane]
					bad := 0
					for j := range got {
						if got[j] != ref[j] {
							bad++
						}
					}
					if bad > 0 {
						differing++
						t.Errorf("frame %d plane %d: %d of %d samples differ from ffmpeg's (grain %+v)", i, plane, bad, len(got), *g)
					}
				}
				if differing > 3 {
					t.FailNow()
				}
			}
			if applied == 0 {
				t.Errorf("none of the %d frames applies film grain: the clip tests nothing", len(want))
			}
		})
	}
}
