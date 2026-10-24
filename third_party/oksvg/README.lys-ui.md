# github.com/srwiley/oksvg v0.0.0-20221011165216-be6e8873101c, patched for lys-ui

A copy of github.com/srwiley/oksvg at be6e8873101c (BSD 3-Clause,
LICENSE), its non-test sources only, carried as a package of the lys-ui
module and imported as `github.com/stubbedev/lys-ui/third_party/oksvg`.
`render` rasterizes SVG icons and OT-SVG glyphs through it.

## Why it lives inside the module

As with `third_party/neurlang-wayland`: Go ignores a dependency's
`replace` directives, so a patched copy wired in through go.mod would
reach lys-ui's own builds and none of its consumers'. As a package of
the lys-ui module, the patches reach every application importing
lys-ui.

## Patches

None yet; the copy is verbatim.

## Upstream status

Each patch is offered upstream; once one is merged and released, the
copy is rebased onto that release and the patch dropped.
