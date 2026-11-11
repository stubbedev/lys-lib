# golang.org/x/image/vector v0.47.0, patched for lys-lib

A copy of the `vector` package of golang.org/x/image v0.47.0 (BSD 3-Clause, LICENSE),
without its tests, its generator or the generator's template, carried as a package
of the lys-lib module and imported as
`github.com/stubbedev/lys-lib/third_party/vector`. `render` and `third_party/rasterx`
rasterize glyphs, clips and SVG paths through it.

## Why it lives inside the module

As with `third_party/oksvg`: Go ignores a dependency's `replace` directives, so a
patched copy wired in through go.mod would reach lys-lib's own builds and none of
its consumers'.

## Patches

### Curve flattening rounds the same on every architecture

`vector.go`: upstream aims for bit-exact results across architectures and across
Go and assembly, and `raster_floating.go` already converts its products for that
reason, but `QuadTo`, `CubeTo` and `lerp` did not. On arm64 they fused into FMAs, so
a curve's segments, and then its edge coverage, differed from amd64's. Each product
that reaches an addition or a subtraction is now converted first. amd64's results
are unchanged. Pinned by `lys_test.go`, which also holds the assembly accumulators
to the portable ones on the architectures that have them, and by `just no-fused`.

No initialisation: `haveAccumulateSIMD` is a function reading
`cpu.X86.HasSSE41` on use, where upstream copied it into a variable when the
package was imported. Pinned by `TestImportingCostsNothing`.

## Upstream status

Not offered upstream yet.
