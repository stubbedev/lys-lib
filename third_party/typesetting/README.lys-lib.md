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
the outline is asked for, and `Header` reads the 10-byte header that
unvaried extents need. Glyph
offsets are checked per glyph: upstream sliced out of range (a panic)
on a bad `loca` and dropped the whole table when one glyph failed to
parse; a malformed glyph now reads as one without outline, the others
are unaffected. Outlines are not cached: a glyph is decoded each time
its outline is asked for, which render's glyph atlas already caches.
Parsing the font above takes 0.8 ms instead of 12.5 ms.
Pinned by `font/opentype/tables/lys_glyf_test.go`,
`font/lys_glyf_test.go` (every glyph of DejaVu Sans: the header from
the raw bytes matches the decoded glyph) and
`font/lys_heap_test.go` (a DejaVu Sans `Face`, 3.5x its file upstream,
holds at most 1.6x).

### Glyph variations are decoded on demand

`font/opentype/tables/xvar_src.go` (with `xvar_gen.go`'s `ParseGvar`)
and `font/variations.go`: upstream's `NewFont` decoded the tuple
variations (headers, point numbers and deltas) of every glyph in `gvar`
up front, the bulk of a variable face's heap (Noto Sans Mono's variable
TTF, 1.69 MB: 6.5 MB per `Face`, 1.65 MB with this patch; Quicksand VF,
125 KB: 3.1x its file, 2.0x). `tables.Gvar` now holds the raw table, its
shared tuples and the per-glyph offsets: `GlyphVariationData` decodes one
glyph's headers when its outline or extents are asked for at
non-default coordinates, and the face decodes that glyph's point
numbers and deltas against the point count of the outline being varied.
Nothing is cached: render's glyph atlas already keeps drawn glyphs.
The offsets are checked at load, as upstream did; a glyph whose
variation data fails to decode now draws unvaried, where upstream
dropped `gvar` for every glyph, and a point number past the glyph's
points is rejected where upstream indexed out of range (a panic).
`Glyf.PointNumbersCount`, which only fed the eager decode, is gone.
Outlines, extents, advances and vertical origins of every glyph of the
93 variable faces of nixpkgs' noto-fonts, at each axis's minimum, 0.4 of
the way to it, and 0.7 of the way to and at its maximum, are identical
to the eager decode's.
Pinned by `font/opentype/tables/lys_gvar_test.go`, `font/lys_gvar_test.go`
(every glyph of `render/testdata/Quicksand-VF.ttf` at wght 300, 580 and
700: outline, extents and advance hash to what the eager decode gave)
and `font/lys_heap_test.go` (a Quicksand VF `Face` holds at most 2.2x
its file).

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

### IsMonospace accepts multiple-cell glyphs

`font/metadata.go`, `IsMonospace`: upstream called a font proportional
at its second distinct non-zero advance. Noto Sans Mono's hmtx has
advances 600 x3309, 1200 x241 and 1800 x9, so it failed (its OS/2
panose proportion is 9, monospaced; fontconfig 2.18 gives it generic
family monospace). A font is now monospace when every non-zero advance
past `.notdef` is about a whole multiple, up to 3, of the narrowest one:
fontconfig's dual-width rule (stubbedev/lys-lib#47) extended to
triple-width glyphs, still within upstream's 1/33 tolerance. Pinned by
`font/metadata_test.go`.

### The system index is written only when it changed

A start that found every font file up to date left the index alone only after
gzipping and rewriting it, so each application start paid a fifth of the scan's
CPU and raced other starts on the same file. Now it writes only when a file was
added, removed or rescanned, and through a rename. The directory search error
now wraps its cause.

### Font files are mapped, not read

A font's tables were parsed through pread on the open file, thousands of small
reads for a collection such as Inter.ttc. The scanner and loadFromDisk now map
the file: a scan unmaps it after the footprints, and a loaded face keeps it, as
its tables stay in place.

### The system font index is read in place

`fontscan/serialize.go`, `scan.go`, `scandir.go`, `fontmap.go`,
`fontconfig.go`, `footprint.go`, `rune_coverage.go`, `langset.go`:
upstream gzips a stream of length-prefixed records, so every start
gunzips it and allocates each path, family, rune set and script set,
then walks the font tree and parses a whole font to check that one
loads, after parsing all of fontconfig's XML. Format 7 is a flat file of
native-endian tables (`font_index_v7.cache`) mapped privately for the
life of the process: loading checks the header, a CRC-32 and every
offset, then points slices and strings into the mapping, one array of
`Footprint`s being the only table built. A rune set or script set the
faces of a family share is stored once. The mapping is writable and
private, so a caller editing a `RuneSet` in place gets what a heap copy
gave it. The file is only ever replaced by a rename, so a reader keeps
the file it mapped. Truncating it from outside faults, as with a mapped
font.

An index of another format, byte order or checksum, a truncated one and
one holding an offset out of range are errors that `refreshSystemFontsIndex`
logs and answers by rescanning and replacing the file. The previous
format's file is deleted when the new one is written.

The index also holds what decides whether it is current without a walk:
the directories walked, a stamp per font file and directory, and the
parse of the fontconfig configuration with the environment it applies
to and a stamp per file it read or looked for. A start stats those and
opens nothing unless one differs; `DefaultFontDirectories` itself still
parses. A stamp mixes mtime, size and inode, since a Nix store fixes
every mtime and an upgrade replaces a font at the same path; upstream
compared the mtime alone. A file whose mtime falls within 2 s of the
scan is stamped as never matching, since a coarse clock could hide its
next change, and a file that could not be read forces a walk each
start. `assertValid` runs only when the index changed, and unmaps what
it parsed. A failure to write the index, or no cache directory (a
system user with no home), is logged and scans on every start, where
upstream returned no fonts at all.

`SystemFonts` returns the shared table instead of a copy, and a
`FontMap`'s first footprints share it too.

Run by `sysfont.Weighted("sans-serif", ...)` on a warm index:

| faces | | before | after |
|---|---|---|---|
| 762 (this host) | time | 21.5 ms | 3.7 ms |
| | allocations | 111.7k | 31.3k |
| | bytes | 11.6 MB | 3.0 MB |
| 1485 in 1000 files (fixture) | time | 19.9 ms | 5.6 ms |
| | allocations | 120.1k | 32.3k |
| | bytes | 15.4 MB | 3.8 MB |
| | openat / getdents64 / read | 261 / 152 / 113 | 159 / 2 / 36 |

Loading the fixture's index alone: gunzip and decode 5.5 ms, 11575
allocations, 3.4 MB; mapped 0.26 ms, 10 allocations, 0.33 MB, for a file
of 618 KB instead of 147 KB. Reading it onto the heap instead of mapping
takes 0.45 ms and 0.95 MB, so it is mapped. Moving the `Footprint` table
off the heap saves about 100 us of a full GC at this size, not worth a
dependency here, so the table stays a plain slice. Pinned by
`fontscan/lys_index_test.go`, which fails if parsing allocates more for
2000 fonts than for 10.

### Products round the same on every architecture

`font/variations.go`, `font/glyphs.go`, `font/cff/charstring.go`,
`font/opentype/tables/`, `harfbuzz/font.go`: upstream's `a*b + c` on deltas, scales
and hinting may fuse into an FMA on arm64 and not on amd64, so a varied outline
differed by the last bit between them. An explicit conversion rounds each product
first, as the Go spec provides, and a varied font now hashes to the same outlines
everywhere. Pinned by `font/lys_gvar_test.go` and by `just no-fused`.

### FONTCONFIG_PATH is honoured

`fontscan/fontconfig.go`: upstream looked the variable up as `"$FONTCONFIG_PATH"`, with
the dollar sign, so it was never found and a relative `FONTCONFIG_FILE` or include
resolved in `/etc/fonts` alone. Its directories are now searched, empty entries skipped,
then `/etc/fonts`, which fontconfig also always searches last. Pinned by
`fontscan/lys_fontconfig_test.go`.

### A table read in place is not a buffer

`font/opentype/reader.go`, `fontscan/scan.go`: a table of a `Mapped` font comes back as a
sub-slice of the font. Handing that back as the buffer for the next table, as
`font.Describe` and the scanner do, copied the next table over it: a write into the
font, a crash on a read-only mapping. Only a smaller table after a larger one did it,
which a scan's order mostly hid. A buffer inside the font now counts as none, and a
scan keeps no buffer past the file it mapped. Pinned by
`font/opentype/lys_mapped_test.go`.

### AddFont parses a font when a query needs it

`fontscan/fontmap.go`, `footprint.go`: upstream parsed each face and its coverage in
`AddFont`, so lys-ui's two embedded faces were parsed in every process that looked up
any font, nearly always to go unused. `AddFont` now reads the family and aspect, and
leaves the entry's coverage and face for the first query to reach it: a rune or language
check on that candidate, a query with a script, or the arbitrary last resort. The
entry keeps its place in the database, its order in `scriptMap` and its claim on the first
face, so what resolves does not change. A resource that is not a font is still refused
by `AddFont`; one whose tables are damaged is logged when reached and covers nothing,
where `AddFont` used to refuse it. The resource must stay readable. Pinned by
`fontscan/lys_lazy_test.go`, which compares answers with fonts parsed when added.

### Import path

Every source imports `github.com/stubbedev/lys-lib/third_party/typesetting`
in place of `github.com/go-text/typesetting`, and the copy has no
go.mod of its own. Upstream's tests (which read fonts from the separate
`go-text/typesetting-utils` module), testdata, `testutils`, CI files,
CODEOWNERS and READMEs are not carried. The heap and DejaVu tests read
the font `LYS_TEST_DEJAVU_SANS` names, which the dev shell sets, and
skip without it; the variable-font tests read render's
`testdata/Quicksand-VF.ttf`.

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

### A directory linked into a font directory is walked

`fontscan/scandir.go`: `filepath.WalkDir` does not follow links, and a link to a
directory reached `consume` as a file. A Nix profile links each package's fonts
that way. Upstream's parse failed softly and those fonts went unindexed; here it is
walked, once whatever links to it, and so is a font root that is itself a link.
A file that is not regular is skipped, and one unreadable font is logged and
skipped instead of ending the scan.
