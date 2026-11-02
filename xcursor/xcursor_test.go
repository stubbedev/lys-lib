package xcursor

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type entry struct {
	nominal      uint32
	size, hot    image.Point
	fill         byte
	notAnImage   bool
	delay        uint32
	headerLength uint32
}

func encode(entries ...entry) []byte {
	le := binary.LittleEndian
	out := le.AppendUint32([]byte(magic), headerSize)
	out = le.AppendUint32(out, 0x10000)
	out = le.AppendUint32(out, uint32(len(entries)))
	pos := headerSize + 12*len(entries)
	var body []byte
	for _, e := range entries {
		kind := uint32(imageType)
		if e.notAnImage {
			kind = 0xfffe0001
		}
		out = le.AppendUint32(le.AppendUint32(le.AppendUint32(out, kind), e.nominal), uint32(pos+len(body)))
		length := e.headerLength
		if length == 0 {
			length = imageSize
		}
		chunk := le.AppendUint32(nil, length)
		for _, v := range []uint32{kind, e.nominal, 1, uint32(e.size.X), uint32(e.size.Y), uint32(e.hot.X), uint32(e.hot.Y), e.delay} {
			chunk = le.AppendUint32(chunk, v)
		}
		if !e.notAnImage {
			for range e.size.X * e.size.Y * 4 {
				chunk = append(chunk, e.fill)
			}
		}
		body = append(body, chunk...)
	}
	return append(out, body...)
}

type countingReader struct {
	r    *bytes.Reader
	read int
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.read += n
	return n, err
}

func nearest(data []byte, want int) (Image, int, error) {
	r := &countingReader{r: bytes.NewReader(data)}
	img, err := Nearest(r, int64(len(data)), want)
	return img, r.read, err
}

func TestNearestReadsOnlyTheImageItReturns(t *testing.T) {
	data := encode(
		entry{nominal: 24, size: image.Pt(24, 24), hot: image.Pt(1, 2), fill: 7},
		entry{nominal: 1, notAnImage: true},
		entry{nominal: 48, size: image.Pt(48, 48), hot: image.Pt(4, 4), fill: 9},
		entry{nominal: 96, size: image.Pt(96, 96), fill: 5},
	)
	for _, c := range []struct {
		want, nominal int
		side          int
		hot           image.Point
		fill          byte
	}{
		{20, 24, 24, image.Pt(1, 2), 7},
		{40, 48, 48, image.Pt(4, 4), 9},
		{200, 96, 96, image.Point{}, 5},
		{36, 24, 24, image.Pt(1, 2), 7},
	} {
		img, read, err := nearest(data, c.want)
		if err != nil {
			t.Fatal(err)
		}
		if img.Nominal != c.nominal || img.Size != image.Pt(c.side, c.side) || img.Hotspot != c.hot || img.Pixels[len(img.Pixels)-1] != c.fill {
			t.Errorf("nearest to %d: %+v, want the %d px image", c.want, img, c.nominal)
		}
		if pixels := c.side * c.side * 4; read > pixels+headerSize+4*12+imageSize {
			t.Errorf("nearest to %d read %d of %d bytes for a %d byte image", c.want, read, len(data), pixels)
		}
	}
}

func TestNearestRejectsMalformedFiles(t *testing.T) {
	good := encode(entry{nominal: 24, size: image.Pt(2, 2), fill: 1})
	cases := map[string][]byte{
		"empty":            nil,
		"bad magic":        append([]byte("Xcux"), good[4:]...),
		"no images":        encode(entry{nominal: 1, notAnImage: true}),
		"hotspot outside":  encode(entry{nominal: 24, size: image.Pt(2, 2), hot: image.Pt(3, 0)}),
		"zero size":        encode(entry{nominal: 24}),
		"bad image header": encode(entry{nominal: 24, size: image.Pt(1, 1), headerLength: 40}),
	}
	for n := 1; n < len(good); n++ {
		if _, _, err := nearest(good[:n], 24); err == nil {
			t.Fatalf("a file cut to %d of %d bytes parsed", n, len(good))
		}
	}
	for name, data := range cases {
		if _, _, err := nearest(data, 24); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestThemesInheritAndPickTheNearestSize(t *testing.T) {
	user, system := t.TempDir(), t.TempDir()
	write(t, filepath.Join(user, "mine", "index.theme"), []byte("[Icon Theme]\nName=Mine\nInherits=base, mine\n"))
	write(t, filepath.Join(user, "mine", "cursors", "left_ptr"), encode(
		entry{nominal: 24, size: image.Pt(1, 1), fill: 1},
		entry{nominal: 48, size: image.Pt(1, 1), fill: 2},
	))
	write(t, filepath.Join(system, "base", "cursors", "xterm"), encode(entry{nominal: 32, size: image.Pt(1, 1), fill: 3}))
	theme, err := Open("mine", []string{user, system})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		size  int
		names []string
		fill  byte
	}{
		{30, Names("default"), 1},
		{40, Names("default"), 2},
		{24, Names("text"), 3},
	} {
		img, err := theme.Load(c.size, c.names...)
		if err != nil {
			t.Fatal(err)
		}
		if img.Pixels[0] != c.fill {
			t.Errorf("%v at %d: image filled with %d, want %d", c.names, c.size, img.Pixels[0], c.fill)
		}
	}
	if _, err := theme.Load(24, "nonexistent"); err == nil {
		t.Fatal("a missing cursor loaded")
	}
	if _, err := Open("absent", []string{user}); err == nil {
		t.Fatal("a theme without cursors opened")
	}
}

func TestBrokenThemeFilesFailLoudly(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "broken", "cursors", "left_ptr"), []byte("Xcur"))
	theme, err := Open("broken", []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := theme.Load(24, "left_ptr"); err == nil {
		t.Fatal("a truncated cursor file loaded")
	}
}

func TestSearchPath(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	if got := SearchPath(env(map[string]string{"XCURSOR_PATH": "/a:/b", "HOME": "/h"})); !slices.Equal(got, []string{"/a", "/b"}) {
		t.Fatalf("XCURSOR_PATH gives %v", got)
	}
	want := []string{"/h/.local/share/icons", "/h/.icons", "/d/icons", "/usr/share/pixmaps"}
	if got := SearchPath(env(map[string]string{"HOME": "/h", "XDG_DATA_DIRS": "/d"})); !slices.Equal(got, want) {
		t.Fatalf("defaults give %v, want %v", got, want)
	}
}

func TestBuiltinArrowHasItsTipAtTheHotspot(t *testing.T) {
	for _, size := range []int{16, 24, 36, 48} {
		img := Builtin(size)
		if img.Size != image.Pt(size, size) || img.Hotspot != (image.Point{}) || img.Nominal != size {
			t.Fatalf("size %d: %+v", size, img)
		}
		if img.Pixels[3] != 0xff {
			t.Fatalf("size %d: the tip pixel is transparent", size)
		}
	}
}

func TestThemesThatInheritEachOtherStillOpen(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a", "index.theme"), []byte("[Icon Theme]\nInherits=b\n"))
	write(t, filepath.Join(dir, "b", "index.theme"), []byte("[Icon Theme]\nInherits=a\n"))
	write(t, filepath.Join(dir, "b", "cursors", "left_ptr"), encode(entry{nominal: 24, size: image.Pt(1, 1), fill: 7}))
	theme, err := Open("a", []string{dir})
	if err != nil {
		t.Fatalf("a theme inheriting itself through another: %v", err)
	}
	if img, err := theme.Load(24, Names("default")...); err != nil || img.Pixels[0] != 7 {
		t.Fatalf("Load = %v, %v", img, err)
	}
}

func TestNearestFramesReadsOneSizesAnimationInOrder(t *testing.T) {
	data := encode(
		entry{nominal: 24, size: image.Pt(24, 24), fill: 1, delay: 10},
		entry{nominal: 48, size: image.Pt(48, 48), fill: 2, delay: 20},
		entry{nominal: 24, size: image.Pt(24, 24), fill: 3, delay: 30},
		entry{nominal: 48, size: image.Pt(48, 48), fill: 4, delay: 40},
	)
	frames, err := NearestFrames(bytes.NewReader(data), int64(len(data)), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("%d frames, want the two of size 24", len(frames))
	}
	for i, want := range []struct {
		fill  byte
		delay uint32
	}{{1, 10}, {3, 30}} {
		if frames[i].Pixels[0] != want.fill || frames[i].Delay != want.delay || frames[i].Nominal != 24 {
			t.Errorf("frame %d: fill %d delay %d nominal %d", i, frames[i].Pixels[0], frames[i].Delay, frames[i].Nominal)
		}
	}
}

func TestAllReadsEveryImageAndNoOtherChunk(t *testing.T) {
	data := encode(
		entry{nominal: 24, size: image.Pt(24, 24)},
		entry{nominal: 1, notAnImage: true},
		entry{nominal: 48, size: image.Pt(48, 48)},
	)
	images, err := All(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 || images[0].Nominal != 24 || images[1].Nominal != 48 {
		t.Fatalf("images %+v", images)
	}
}

func TestFramesOfACursorTheThemeLacksIsNotExist(t *testing.T) {
	dir := t.TempDir()
	cursors := filepath.Join(dir, "T", "cursors")
	if err := os.MkdirAll(cursors, 0o750); err != nil {
		t.Fatal(err)
	}
	data := encode(entry{nominal: 24, size: image.Pt(24, 24), delay: 7})
	if err := os.WriteFile(filepath.Join(cursors, "watch"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	theme, err := Open("T", []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	frames, err := theme.Frames(24, "watch")
	if err != nil || len(frames) != 1 || frames[0].Delay != 7 {
		t.Fatalf("Frames(watch) = %+v, %v", frames, err)
	}
	if _, err := theme.Frames(24, "xterm"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Frames(xterm): %v", err)
	}
}
