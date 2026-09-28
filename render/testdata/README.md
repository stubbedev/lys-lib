# render/testdata

Fixture assets for render-level and widget-level pixel tests.

## Cantarell-Regular.ttf

The golden-image harness (`internal/golden`, `render/golden_test.go`,
`widget/golden_test.go`) shapes and rasterizes every snapshot with this
font and nothing else, so glyph metrics and pixel output are byte-stable
on any machine, with or without fonts installed.

- Source: https://github.com/google/fonts/tree/main/ofl/cantarell
  (Cantarell Regular, downloaded 2026-09-27)
- SHA-256: `840a255779258a2b24b5e3d1f32a821ed641fdd5ef1e47c4dc180064db9ccfa1`
- License: SIL Open Font License 1.1 — see LICENSE-Cantarell.txt, which
  ships alongside the font and must be kept with it. The license permits
  bundling and redistribution inside software.

The file is the font's complete, unmodified regular weight (43 KB — small
enough to bundle whole, no subsetting step to keep in sync). To refresh
it, re-download, update the SHA-256 above, regenerate the goldens with
`UPDATE_GOLDEN=1 go test ./render ./widget`, and review the diff.

## NotoSansHebrew-Regular.ttf

The second fixture face, loaded only by the bidirectional-text tests
(`FixtureChain`): Cantarell covers no Hebrew, so mixed Hebrew + Latin
goldens shape through a fallback chain over the two — which is exactly
the path mixed-direction labels take in production.

- Source: https://github.com/notofonts/hebrew (Noto Sans Hebrew
  Regular, downloaded 2026-09-28)
- SHA-256: `cdefaf8efd47045f6820928eba84db5bed7557539328952b5f828315485e02ee`
- License: SIL Open Font License 1.1 — see LICENSE-NotoSansHebrew.txt,
  which ships alongside the font and must be kept with it.

The file is the font's complete, unmodified regular weight (26 KB).

## golden/

Committed PNG snapshots. A test fails when its golden is missing, so new
widgets must land with snapshots; `UPDATE_GOLDEN=1 go test ./...`
regenerates them.
