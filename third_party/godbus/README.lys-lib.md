# github.com/godbus/dbus/v5 v5.2.2, patched for lys-lib

A copy of github.com/godbus/dbus/v5 v5.2.2 (BSD 2-Clause, LICENSE; the
fd-array bug is also unfixed on master 6fea989, 2026-09-24), its
non-test sources and lys-lib's own tests, carried as packages of the
lys-lib module and imported as
`github.com/stubbedev/lys-lib/third_party/godbus` (package `dbus`),
`.../godbus/introspect` and `.../godbus/prop`. Every lys process on
D-Bus talks to it through this copy, directly or through `dbusx`.

## Why it lives inside the module

As with `third_party/oksvg`: Go ignores a dependency's `replace`
directives, so a patched godbus wired in through go.mod (lys-shell's
`third_party/godbus-dbus`, which this copy replaces) reached only that
module's own builds, and anything importing its packages built against
upstream. As packages of the lys-lib module, the patches reach every
application importing lys-lib, with no `replace`.

## Patches

Drop the copy once upstream carries every fix below.

- `decoder.go`, case `'a'`: an `ah` decoded with the message's fds made a
  `[]UnixFDIndex` and appended `UnixFD` values to it. The reflect panic was
  a string, which `Decode`'s recover does not turn into an error, so every
  message carrying an fd array (xdg-desktop-portal's Email
  `attachment_fds`) arrived with an empty body. The slice is now
  `[]UnixFD`, and an index past the fds is an InvalidMessageError.
  Pinned by `lys_fd_array_test.go`.
- `encoder.go`, case `variantType`, and `lys_encode_as.go`: a Variant's
  value is encoded by the Variant's signature. `decode` returns a struct as
  `[]any`, which the Go-type encoder wrote as an array of variants under the
  struct signature: a malformed message, which dbus-daemon answers by
  disconnecting the sender. Echoing a decoded GIcon (DynamicLauncher's
  PrepareInstall) killed the portal's bus connection. Pinned by
  `TestDecodedStructsReencode`.
- `lys_file_reply.go` (with `sig.go`, `encoder.go`, `dbus.go`,
  `export.go`): `*os.File` marshals as `h`, and the files an exported
  method replies are closed once the reply is sent. A method returning a
  bare UnixFD had no safe moment to close its copy, since the reply goes
  out after it returns. The portal's clipboard pipes and PipeWire remote
  ride on this. Pinned by `TestAFileReplyIsSentThenClosed`.
- `lys_ordered.go` (with `conn.go`'s read loop): `DispatchInOrder`
  runs an interface's named methods one at a time in arrival order on one
  goroutine, through a queue bounded at 256 that holds back the read loop
  when full; every other call still runs on a goroutine of its own.
  Upstream dispatches each call on its own goroutine, so calls a sender
  does not wait on race: xdg-desktop-portal forwards RemoteDesktop input
  that way, and a click could land before the motion that placed it.
  Pinned by `TestOrderedCallsRunInArrivalOrder`.
- `prop/prop.go`, `set`: a new value is stored into a fresh allocation that
  replaces the property's, not into the existing one. `Get` and `GetAll`
  hand out the stored value (a slice shares its backing array) and the
  reply is encoded after the read lock is released, so storing in place
  raced with that encode and could rewrite a reply mid-flight; the race
  detector caught it on a fake NetworkManager's `ActiveConnections`.
  Pinned by `prop/lys_set_test.go`.
- `default_handler.go` and `export.go`: the handler's object table is read
  and changed only under its lock. `PathExists`, `introspectPath` and
  `unexport` read it unlocked, and `export` added a path's object and then
  its interface in two separate steps, so exporting and unexporting from
  several goroutines (lys-secrets' prompts and sessions) raced, and a call
  could find an object between the steps. `addInterface` and
  `deleteInterface` now do each change in one locked step. Pinned by
  `TestConcurrentExportsAndCalls`.
- Import path: every source imports `github.com/stubbedev/lys-lib/third_party/godbus`
  in place of `github.com/godbus/dbus/v5`, and the copy has no go.mod
  of its own. Upstream's tests, CI files and fuzz corpus are not
  carried; the tests named above pin the patches, the bus-dependent ones
  (`lys_bus_test.go`) on a private bus from `dbustest`.

The copy follows lys-lib's Go version, with golangci-lint's modernize fixes
applied throughout: `reflect.TypeFor` and `TypeAssert`, `strings.SplitSeq`
and `CutLast`, range over ints and over `Fields`/`Methods`,
`slices.Backward` and `strings.Builder` where upstream predates them. No
behaviour changes; upstream's tests passed under -race on a private
session bus when they were applied, in lys-shell.
