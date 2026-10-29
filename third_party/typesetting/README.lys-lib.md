# github.com/go-text/typesetting v0.3.5, patched for lys-lib

A copy of github.com/go-text/typesetting v0.3.5 (BSD 3-Clause or
Unlicense, LICENSE; harfbuzz/LICENSE for the HarfBuzz port; v0.3.5 is
the latest release and upstream main parses fonts the same way as of
2026-10-29), its non-test sources (and harfbuzz's Ragel machines, the
source of the generated `ot_shape_*_machine.go`), carried as packages of
the lys-lib module and imported as
`github.com/stubbedev/lys-lib/third_party/typesetting/...`: `bidi`,
`di`, `font`, `font/cff`, `font/cff/interpreter`, `font/opentype`,
`font/opentype/tables`, `fontscan`, `harfbuzz`, `internal/unicodedata`,
`language`, `segmenter` and `shaping`, every package lys-lib and lys-ui
import directly or through those.

## Why it lives inside the module

As with `third_party/oksvg`: Go ignores a dependency's `replace`
directives, so a patched copy wired in through go.mod would reach
lys-lib's own builds and none of its consumers'. The fork has to cover
every package that names `font.Face`, since `render`, `text` and lys-ui
pass faces between `font`, `fontscan`, `shaping` and `harfbuzz`, and a
face from one copy is not a face of the other.

## Patches

Drop the copy once upstream carries every change below.

### Glyph outlines are decoded on demand

`font/opentype/tables/glyphs_glyf_src.go` (with `glyphs_glyf_gen.go`,
whose `ParseGlyph` moved to the source file) and `font/font.go`,
`glyphs.go`, `metrics.go`, `renderer.go`, `variations.go`: upstream's
`NewFont` decoded every `glyf` outline into `tables.Glyf` (`[]Glyph`
with parsed points) and kept the raw table the glyphs alias, about 3x a
TrueType file in heap per face (JetBrainsMono Nerd Font Regular, 2.53 MB:
8.9 MB per `Face`, 3.3 MB with this patch). `tables.Glyf` now holds
the raw table and the `loca` offsets: `Glyph` decodes one glyph when
the outline is asked for, `Header` reads the 10-byte header that
unvaried extents need, and `PointNumbersCount` gives `gvar` its
per-glyph point count from the contour end points (simple glyphs) or
the component records (composite ones) without decoding points. Glyph
offsets are checked per glyph: upstream sliced out of range (a panic)
on a bad `loca` and dropped the whole table when one glyph failed to
parse; a malformed glyph now reads as one without outline, the others
are unaffected. Outlines are not cached: a glyph is decoded each time
its outline is asked for, which render's glyph atlas already caches.
Parsing the font above takes 0.8 ms instead of 12.5 ms.
Pinned by `font/opentype/tables/lys_glyf_test.go`,
`font/lys_glyf_test.go` (every glyph of DejaVu Sans: header and point
count from the raw bytes match the decoded glyph) and
`font/lys_heap_test.go` (a DejaVu Sans `Face`, 3.5x its file upstream,
holds at most 1.6x).

### post glyph names are decoded on lookup

`font/opentype/tables/post_src.go` and `font/post.go`, `font/font.go`:
upstream decoded every format 2.0 `post` name into a string at load
(0.23 MB of the font above, which now holds 3.0 MB per `Face`), for
`GlyphName`, which shaping and rendering never call.
`PostNames20.Strings` is now the raw Pascal string data; the first
`GlyphName` indexes it once (`sync.OnceValue`, a `Font` stays safe for
concurrent use) and each lookup decodes one name. An index past the
names or in the reserved 32768..65535 range names nothing; upstream
rejected the whole names table for either, and a truncated string table
took the underline metrics and `isFixedPitch` with it. Pinned by
`font/lys_post_test.go`, and by `font/lys_heap_test.go`'s budget, now
1.45x.

### Import path

Every source imports `github.com/stubbedev/lys-lib/third_party/typesetting`
in place of `github.com/go-text/typesetting`, and the copy has no
go.mod of its own. Upstream's tests (which read fonts from the separate
`go-text/typesetting-utils` module), testdata, `testutils`, CI files,
CODEOWNERS and READMEs are not carried. The heap and DejaVu tests read
the font `LYS_TEST_DEJAVU_SANS` names, which the dev shell sets, and
skip without it.

## Verified against upstream

With the patches applied, upstream's v0.3.5 test suite passed (its
`glyf` tests adapted to the lazy table), and every glyph of the 789
faces in typesetting-utils, render/testdata and DejaVu Sans, plus 325
installed faces (Noto, JetBrainsMono Nerd Font), had the same name,
extents, outline and vertical origin as upstream's, variable fonts at
their default and at 0.7 on every axis.

## Not patched

Loading tables as subslices of one buffer instead of per-table copies
does not pay for a heap-backed file: the file buffer would be kept whole
(a `Font` of the font above would hold 2.9 MB instead of 2.1 MB). It
pays only over an mmapped file (0.4 MB of heap per `Font`), which needs
a mapping that lives as long as the face and a file nobody truncates.

Variable fonts still cost several times their file: `gvar` is decoded
whole at load (Noto Sans Mono's variable TTF, 1.7 MB, holds 7.0 MB per
`Face`, 8.0 MB upstream).
