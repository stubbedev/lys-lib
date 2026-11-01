# github.com/srwiley/rasterx v0.0.0-20220730225603-2ab79fcdd4ef, patched for lys-lib

A copy of github.com/srwiley/rasterx at 2ab79fcdd4ef (BSD 3-Clause, LICENSE), its
non-test sources only, carried as a package of the lys-lib module and imported as
`github.com/stubbedev/lys-lib/third_party/rasterx`. `render` and `third_party/oksvg`
stroke, dash and transform SVG paths through it.

## Why it lives inside the module

As with `third_party/oksvg`: Go ignores a dependency's `replace` directives, so a
patched copy wired in through go.mod would reach lys-lib's own builds and none of
its consumers'.

## Patches

### Products round the same on every architecture

`matrix.go`, `geomx.go`, `shapes.go`, `gradient.go`, `fill.go`, `stroke.go`: the Go
spec lets the compiler fuse `x*y + z` into one FMA instruction, which skips the
rounding of the product. It does so on arm64 and not on amd64, so a symbolic icon's
edge pixels differed between the two: 918 of 63296 in lys-ui's builtin icon grid and
12 of 480000 in lys-splash's frames. Each product that reaches an addition or a
subtraction is now converted, `float64(x*y) + z`, which the spec defines to round
first. That includes `x/2`, which the compiler turns into a product, and `x*y*2`,
which it turns into `x*y + x*y`. amd64's results are unchanged. Pinned by the
family's goldens, which hold exactly on amd64 and arm64, and by `just no-fused`.

### Import path

`scan.go` imports `third_party/vector` in place of `golang.org/x/image/vector`.

## Upstream status

Not offered upstream yet; the repository has had no commits since 2022.
