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

## No work at import

Importing a package runs no code: a program pays for what it uses, not for what
it links. A package that lys-ui links, vendored copies included, carries

```go
func TestImportingCostsNothing(t *testing.T) { testbuild.RequireNoInit(t) }
```

which fails when the package, or a test file in it, has an init function or a
package-level variable the compiler cannot lay out statically: a map, a call
(`regexp.MustCompile`, `fmt.Errorf`, `reflect.TypeFor`, `maphash.MakeSeed`), a
computed table, a slice of another variable. Write a switch or a static array
in place of a small map, a constant in place of a computed one (with a test
holding it to its formula), and build the rest on first use behind a
`sync.Once` in a struct beside its value. `gpu` and the packages under it do
not carry the test yet.

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
| `dbustest` | Private `dbus-daemon`s configured from a generated file; needs only `dbus-daemon` on `PATH`. `NewPipe` is a descriptor to send whose `RequireEveryCopyClosed` fails the test while any copy the bus handed out is still open |
| `pulse/pulsetest` | A fake PulseAudio server speaking the native protocol, with scriptable devices, streams and failures; a client's own streams go with its connection |
| `logind/logindtest` | A fake systemd-logind on a `dbustest` bus that records every call and emits logind's signals; its helpers report with `t.Errorf`, so hooks on other goroutines may call them |
| `polkit/polkittest` | A fake polkitd Authority on a `dbustest` bus: scripted or held authorization checks, agent registrations, actions and temporary authorizations, every check and cancel recorded |
| `peercred/peercredtest` | A socket peer that is another process, with its own pid and lifetime: the test binary run again through `Run` in the package's `TestMain`, or `Main` inside `testhome.Main` |
| `sockdir` | A directory short enough for Unix socket paths (`sun_path` is 108 bytes) |
| `testhome` | Runs a test binary against a scratch home and no session but its own: `Run` (or `Main`) in `TestMain` moves `HOME` and the XDG per-user directories, unsets the variables that advertise a compositor, display or media server, and pins the session bus, the system bus and the sound server to sockets only a test's own fake serves, keeping the go command's caches and a recipe's headless session (`LYS_TEST_HEADLESS`). `RequireNoHostSession`, called from one test per package, fails where a host endpoint is still within reach |

A package whose tests build applications, dial a bus or connect to a display
isolates itself with both halves:

```go
func TestMain(m *testing.M) { testhome.Run(m) }

func TestTheTestsReachNoHostSession(t *testing.T) { testhome.RequireNoHostSession(t) }
```

A test that needs a bus starts its own (`dbustest`) and points the process at it
(`UseAsSessionBus`, `UseAsSystemBus`). A test binary an isolated test starts
again, as a helper or as the program under test, keeps the environment its
parent gave it.

A recipe that boots a session for the tests, a headless compositor or a VM's
login, exports `LYS_TEST_SESSION` with that session's runtime directory.
`Main` then leaves the session in place, and the guard holds the runtime
directory, the buses, the sound server and the display to exactly that
directory or the test home, wherever it lies; a session bus the recipe did not
name is pinned to an absent socket. Without the variable a session in
`/run/user/<uid>` is the host's and fails the guard.
