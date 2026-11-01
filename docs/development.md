# Development

## Gates

`nix develop` (or direnv) enters the dev shell, and `just` lists the recipes.
`just gates` runs every gate CI runs:

| Recipe | What it does |
| --- | --- |
| `just fmt-check` | Formatting |
| `just check` | `vet`, `lint`, `test`, `build` |
| `just race` | The test suite under the race detector |
| `just gpu` | The GPU tests on lavapipe |
| `just headless` | The capture and virtual input tests against a private headless sway |

`just gpu-host` runs every GPU path on the host's own GPU, under the validation
layer. `nix fmt` formats the nix files with alejandra.

`just docker <recipe>` (default `gates`) runs a recipe in a container that sees
the dev shell's store paths, the checkout, the GPUs' render and NVIDIA device
nodes and the host's drivers, but no display socket, session bus or runtime dir of
the desktop session, with memory capped.

## CI

`.github/workflows/ci.yml` runs `just gates` in the dev shell.
`.github/workflows/security.yml` runs CodeQL (Go and GitHub Actions), Grype and
govulncheck, uploading findings to code scanning, and dependency review gates pull
requests against a licence allow-list. Dependabot keeps the module and the
workflows current. Every action is pinned to a commit.

## Test support

These packages are for tests, here and in other family members.

| Package | What it does |
| --- | --- |
| `golden` | Compares rendered pixels against committed PNG snapshots, exact by default. A missing golden fails the test, and `References` from another renderer compare within a `Tolerance` |
| `gputest` | Picks the Vulkan driver the GPU tests run on from `LYS_TEST_GPU`: `lavapipe` for Mesa's CPU driver, any other value for the host's device, and unset runs where a device is found and skips elsewhere |
| `wltest` | A private runtime dir and a loopback display for wire-level tests, and a client that puts solid-colored surfaces on the headless compositor for capture tests to find |
| `pipewire/pwtest` | A private PipeWire daemon, and libpipewire consumers as the reference a producer is checked by |
| `dbustest` | Private `dbus-daemon`s, configured from a file the helper writes, so a test needs only `dbus-daemon` on `PATH` |
| `sockdir` | A directory short enough to hold Unix sockets, since `sun_path` holds 108 bytes |

## History

The packages moved here from lys-ui and lys-shell with their history. Issue
numbers in that history name their repository (`stubbedev/lys-ui#N`,
`stubbedev/lys-shell#N`).
