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

### Strokes scale with the transform

`svg_path.go`, `SvgPath.DrawTransformed`: upstream hands the dasher
`stroke-width`, `stroke-dasharray` and `stroke-dashoffset` as device
pixels while the path itself goes through the full transform (viewBox
fit and element transforms), so a stroke kept its user-unit width at
any icon size. The patch scales all three by the transform's area
scale, sqrt(|det|), exact for uniform scales and rotations (lys-ui
#189).

### Stroke defaults are SVG's

`path_style.go`, `DefaultStyle`: upstream defaults `stroke-width` to 2
and `stroke-linejoin` to bevel; SVG's initial values are 1 and miter
(lys-ui #191).

### userSpaceOnUse gradients follow the transform

`svg_path.go`, `SvgPath.DrawTransformed`: upstream builds gradient
paints with rasterx's `GetColorFunction`, the identity transform, so a
`gradientUnits="userSpaceOnUse"` gradient was placed at its user-space
coordinates read as device pixels. The patch passes the path's full
transform to `GetColorFunctionUS`. rasterx maps the gradient's
geometry forward through it, which is exact for scales and
translations (the viewBox fit) but not for a radial gradient under a
rotation or skew (lys-ui #190).

## Upstream status

Each patch is offered upstream; once one is merged, the copy is
rebased onto it and the patch dropped.

- Strokes scale with the transform: srwiley/oksvg#54
- Stroke defaults are SVG's: srwiley/oksvg#55
- userSpaceOnUse gradients follow the transform: srwiley/oksvg#56
