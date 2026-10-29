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

### Import path

Every source imports `github.com/stubbedev/lys-lib/third_party/typesetting`
in place of `github.com/go-text/typesetting`, and the copy has no
go.mod of its own. Upstream's tests (which read fonts from the separate
`go-text/typesetting-utils` module), testdata, `testutils`, CI files,
CODEOWNERS and READMEs are not carried.
