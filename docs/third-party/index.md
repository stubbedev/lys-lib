# Third-party patch policy

`third_party/` carries patched copies of upstream Go code as packages of the
lys-lib module.

| Copy | Upstream | Used by |
| --- | --- | --- |
| [godbus](/third-party/godbus) | github.com/godbus/dbus/v5 | `dbusx`, `portaldialogs`, `dbustest` |
| [gomath](/third-party/gomath) | Go's `math` | Every family member, for results that are the same on every architecture |
| [oksvg](/third-party/oksvg) | github.com/srwiley/oksvg | `render`, for SVG icons and OT-SVG glyphs |
| [rasterx](/third-party/rasterx) | github.com/srwiley/rasterx | `render` and `oksvg`, for strokes, dashes and transforms |
| [typesetting](/third-party/typesetting) | github.com/go-text/typesetting | `render`, `text` and `gpu`, for fonts, shaping and bidirectional text |
| [vector](/third-party/vector) | golang.org/x/image/vector | `render` and `rasterx`, for coverage |
| [vk](/third-party/vk) | github.com/gogpu/wgpu's `hal/vulkan/vk` | `gpu` |
| [wayland](/third-party/wayland) | github.com/neurlang/wayland | `capture`, `vinput`, `wlr` and the rest of the Wayland code |

Each page above is that copy's `README.lys-lib.md`, kept beside the code: its
upstream version, commit and licence, and every patch.

## Why the copies live inside the module

Go ignores a dependency's `replace` directives. A patched copy wired in through
`go.mod` would reach lys-lib's own builds and none of its consumers'. As packages
of the module, the patches reach every application importing lys-lib, with no
`replace`, and every family member shares the same types.

When you import one of these directly, use the copy's import path, such as
`github.com/stubbedev/lys-lib/third_party/godbus`, not upstream's. A value from
upstream's module is not a value of the copy's: a `font.Face` from one is not a
face of the other.

## Rules

- **A copy records what it is.** Its `README.lys-lib.md` names the upstream
  version, commit and licence, and carries the upstream `LICENSE`.
- **Every patch is written down,** with the files it touches, the bug it fixes and
  why, in that README. A patch that is not a bug fix says it is a lys-lib
  integration choice.
- **A patch is pinned by a test,** named in the README where one applies. Those
  tests are lys-lib's own: only non-test sources are carried from upstream.
- **Bug fixes are offered upstream.** The oksvg, vk and wayland READMEs link their
  pull requests. A patch is dropped once upstream carries it, and a copy is dropped
  once upstream carries every patch it needs.
- **Only what is used is carried.** Upstream's CI files and tests stay out, and a
  copy has no `go.mod` of its own.
- **The module's lint and format rules do not apply.** `.golangci.yml` excludes
  `third_party/`, so a copy keeps upstream's shape and stays easy to compare.
