# Development

## Gates

`nix develop` (or direnv) enters the dev shell; `just` lists recipes.
`just gates` runs everything CI runs:

| Recipe | Runs |
| --- | --- |
| `just fmt-check` | Formatting check (gofumpt, gci) |
| `just check` | `vet`, `lint`, `no-fused`, `test`, `build` |
| `just race` | Tests under the race detector |
| `just gpu` | GPU tests on lavapipe |
| `just headless` | Capture and virtual input tests against a private headless sway |

Other recipes:

| Recipe | Runs |
| --- | --- |
| `just fmt` | Format Go sources |
| `just gpu-host` | Every GPU path on the host's GPU under the validation layer, including the encoder and dmabuf capture |
| `just docker [recipe] [memory]` | A recipe (default `gates`, memory `10g`) in a container with the dev shell's store paths, the checkout, GPU render nodes and host drivers, but no display, session bus or runtime dir |
| `just generate` | Rebuild shaders, Wayland bindings and keysym tables |
| `just goldens` | Rewrite render goldens (`UPDATE_GOLDEN=1`) |
| `just svg-refs` | Rasterize SVG references with rsvg-convert |
| `just bench` | Benchmarks |
| `just test-env`, `just test-env-stop` | Start or stop the headless sway test session |
| `just vendor-hash` | Update the Nix `vendorHash` from `go.sum` |
| `just release-patch`, `release-minor`, `release-major` | Tag and push the next release from a clean default branch with green CI |

`nix fmt` formats Nix files with alejandra.

## Determinism

Rendering, typesetting, animation and styling produce identical bits on every
architecture, so one golden serves all. Two rules enforce this:

- Go may fuse `x*y + z` into an FMA (arm64 does, amd64 does not). Write
  `float64(x*y) + z` wherever a product, or a division by a constant power of
  two, feeds an addition or subtraction, including across statements.
  `just no-fused` compiles for arm64 and fails on any FMA instruction.
- Use [gomath](/third-party/gomath) for trigonometry, exponentials, logarithms,
  `Pow`, `Cbrt`, `Hypot`, `Erf` and `Erfc`. The linter rejects `math`'s versions
  outside tests.

## CI

- `ci.yml` runs `just gates` in the dev shell on amd64 and arm64 runners.
- `security.yml` runs CodeQL (Go, Actions), Grype and govulncheck, uploading to
  code scanning; dependency review checks pull requests against a licence
  allow-list.
- `vendor-hash.yml` keeps the Nix vendor hash in step with `go.mod` changes on
  pull requests.
- `docs.yml` publishes this site.
- Dependabot updates modules and workflows. Actions are pinned to commits.

## Test support

Importable by tests here and in other family members.

| Package | Purpose |
| --- | --- |
| `golden` | Compares pixels against committed PNG goldens, exact by default. A missing golden fails. `Reference` compares against another renderer's output within a `Tolerance`. `UPDATE_GOLDEN=1` rewrites goldens |
| `gputest` | Selects the GPU tests' Vulkan driver from `LYS_TEST_GPU`: `lavapipe` for Mesa's CPU driver, any other value for the host's device; unset runs where a device exists and skips elsewhere |
| `wltest` | Private runtime dir, loopback display for wire-level tests, fake linux-dmabuf feedback, and a client that shows solid-colour surfaces on the headless compositor (`LYS_TEST_HEADLESS`) |
| `pipewire/pwtest` | Private PipeWire daemon and libpipewire reference consumers |
| `eis/eistest` | libei reference client for EIS servers |
| `dbustest` | Private `dbus-daemon`s configured from a generated file; needs only `dbus-daemon` on `PATH` |
| `logind/logindtest` | A fake systemd-logind on a `dbustest` bus that records every call and emits logind's signals |
| `sockdir` | A directory short enough for Unix socket paths (`sun_path` is 108 bytes) |
