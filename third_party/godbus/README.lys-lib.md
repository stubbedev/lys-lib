# github.com/godbus/dbus/v5 v5.2.4, patched for lys-lib

A copy of github.com/godbus/dbus/v5 v5.2.4 (4e46d5e, BSD 2-Clause,
LICENSE; the descriptor decoding bugs are unfixed there and on master,
which is v5.2.4 as of 2026-10-28), its
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

- `lys_received_fds.go` (with `decoder.go`, `sig.go`'s `typeWithFDs`):
  with the message's descriptors at hand every `h` decodes to its
  `UnixFD`, and the containers around it are built for that type, at any
  depth. Upstream resolved an `h` as it met one but built arrays and dicts
  for `UnixFDIndex`: an `ah` (xdg-desktop-portal's Email `attachment_fds`)
  arrived with an empty body up to v5.2.2, and since v5.2.3, which
  re-panics what is not an error, the reflect panic crashed the read loop
  and the process. A first patch fixed the bare `ah` only; a dict or a
  nested array of descriptors (`a{sh}`, `a{sah}`, `aah`, `a{hs}`) still
  did it, and any bus peer can send one. An index past the descriptors is
  an InvalidMessageError. A body naming one descriptor twice decodes to
  that descriptor and a duplicate (`F_DUPFD_CLOEXEC`, -1 when the process
  has no room), where upstream decoded one descriptor held twice, which
  its receiver closed twice. Without descriptors (`DecodeMessage`) an `h`
  stays a `UnixFDIndex`, in every container alike. Pinned by
  `lys_fd_array_test.go`, `TestDescriptorsDecodeInsideEveryContainer`,
  `TestADescriptorNamedTwiceIsDecodedAsTwoDescriptors`,
  `TestDescriptorsStayIndicesWithoutTheMessagesDescriptors` and
  `TestADictOfDescriptorsArrives`.
- `lys_received_fds_unix.go` (with `transport_unix.go`'s `readMessage`,
  `conn.go`'s read loop and call tracker, `export.go`'s `handleCall`):
  every descriptor that comes with a message is the decoded body's or is
  closed. Upstream closed none: those a body did not name, those of a
  message that failed to decode or was invalid, those attached to a
  message declaring none, those of a reply nobody waits for, of a message
  for a name the connection does not own, and of a call no method is run
  for (no such object, interface or method, arguments of other types)
  stayed open, so a peer could fill a process's descriptor table. A
  message that comes with another number of descriptors than its header
  declares is an InvalidMessageError; upstream indexed the descriptors it
  had received by the count the header promised and panicked when there
  were fewer. Control data received by a read that then failed or was
  truncated is kept so that its descriptors are closed too. Pinned by
  `TestTheDescriptorsOfAReadMessageAreTheBodysOrClosed` and
  `TestTheDescriptorsOfACallNoMethodRunsForAreClosed`.
- `decoder.go`, `Decode`, and `lys_received_fds.go`'s `decodeFailure`: no
  panic leaves `Decode`. One that is not the decoder's own error or its
  reader's (a reflect panic, a runtime error) is an InvalidMessageError,
  for which the read loop drops the message and keeps the connection;
  upstream re-panicked it, so a decoder defect met on a message a peer
  chose took the process down. A header that does not decode is never an
  InvalidMessageError (`headerError`): its body is unread, and reading on
  would take the body for the next message. Pinned by
  `TestAPanicWhileDecodingIsThatMessagesError`,
  `TestAnUndecodableHeaderIsNotASkippedMessage` and
  `FuzzDecodeBodyWithDescriptors`, which decodes bodies with descriptors
  attached and stores, re-encodes and closes what they decode to.
- `sig.go`, `validSingle`, and `dbus.go`, `isKeyType`: an empty struct is
  no signature, and a dict entry's key is a basic type, any of them.
  Upstream accepted `()`, and decoding an `a()` never ended, since no
  element takes a byte of the array's length; it accepted a variant as a
  key, a dict type nothing can encode or name the signature of
  (`SignatureOf` panicked on the decoded value); and it refused boolean
  and signature keys, so a peer's `a{bs}` ended the connection. Pinned by
  `TestAnEmptyStructIsNoSignature` and `TestEveryBasicTypeKeysADict`.
- `transport_unix.go` and `message.go`: the header and body lengths are
  summed in 64 bits. A header length near 2^32 wrapped below the 128 MiB
  message limit, and the unix transport then read up to 4 GiB of header
  into memory. Pinned by `TestTheTransportRefusesLengthsNearTheWrap` and
  `TestLengthsNearTheWrapAreTooLong`.
- `conn.go`, `handleSignal`: a `NameLost` or `NameAcquired` without its
  name is ignored. Upstream panicked on one, and on a connection to a
  peer rather than a bus the peer chooses the sender and the body. Pinned
  by `TestAnOwnershipSignalWithoutItsNameIsIgnored`.
- `conn.go`, `getSessionBusAddress`: an address found in the runtime
  directory is returned, not also written into
  `DBUS_SESSION_BUS_ADDRESS`. Upstream set the variable, so connecting to
  the session bus changed the environment of the whole process and of
  every child it started afterwards. Pinned by
  `TestFindingTheSessionBusLeavesTheEnvironmentAsItWas`.
- `lys_close.go` (with `conn.go`, `auth.go`, `export.go`,
  `lys_ordered.go`): `Close` returns once the goroutines the connection
  runs its own code on have ended: the read loop, the watcher of its
  context, the watcher of each pending call's context (which now ends
  with the connection too, where upstream's stayed until the call's
  context ended), each dispatched method call and each ordered lane.
  Upstream's `Close` returned with all of them winding down, so a
  closed connection's goroutines were still running in the next test
  of a program that counts them. A goroutine is not counted while it
  runs the application's code (an exported method, an interceptor, a
  signal handler other than this package's two), so that code may take
  its time and may itself close the connection; the connection's own
  goroutines close it through the non-waiting `close`. Pinned by
  `TestCloseWaitsForTheGoroutinesOfTheConnection`,
  `TestAMethodMayCloseItsOwnConnection` and
  `TestAConnectionClosedByItsContextLeavesNoGoroutine`, by goroutine id.
- No initialisation: importing the package runs no code. Upstream built
  maps (`sigToType`, `varTypeMap`, the variant parser's signature sets,
  `transports`, which each transport's file filled from an `init`), a
  table of header field types, a variable for the reflect type of each
  D-Bus type (`reflect.TypeFor` where one is used now, which costs a
  load) and the machine's byte order when the
  package was imported. They are switches (`basicTypeOf`, `varTypeSig`,
  `fieldType`, `transportFor`, with `lys_transport_absent_*.go` naming
  the transports a build leaves out) and functions called on use
  (`nativeEndian`, the signature sets). Pinned by
  `TestImportingCostsNothing`.
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
- `lys_signal_fds.go` (with `conn.go`'s `handleSignal`, `default_handler.go`
  and `sequential_handler.go`): each channel a signal is delivered to owns
  the descriptors in the `Signal` it receives. Upstream hands one `Signal`,
  and so the same `UnixFD` values, to every channel registered with
  `Conn.Signal`: two readers of logind's `ResumeDevice` on one connection
  closed one descriptor twice, or one used it after the other had closed it.
  A signal that came with descriptors now reaches the last channel as it
  was read and every other channel as a copy holding duplicates
  (`F_DUPFD_CLOEXEC`) of them, at any depth of the body; one that cannot be
  duplicated arrives as -1. The descriptors of a signal no channel takes
  (none registered, the channel removed with the signal undelivered, the
  connection closed) are closed, where upstream leaked them. A reader that
  does not take a signal's descriptors over closes them with
  `Signal.CloseFDs` (`CloseFDs` for a reply's body), which costs nothing for
  a signal that came without any. A signal without descriptors is still one
  `Signal` shared by every channel. Pinned by
  `TestEachSignalChannelOwnsItsDescriptors`,
  `TestDescriptorsOfASignalNoChannelTakesAreClosed` and
  `TestMapFDsReachesEveryDescriptorOfADecodedBody`.
- `conn_other.go`, `getRuntimeDirectory`: with `DBUS_SESSION_BUS_ADDRESS`
  unset the session bus is looked for at `$XDG_RUNTIME_DIR/bus`, as libdbus
  and sd-bus do. Upstream looks in `/run/user/<uid>` whatever
  `XDG_RUNTIME_DIR` says, so a process given a runtime directory of its own
  (a test, a nested session) joined the bus of the user's login session and
  missed the one in its own directory. `/run/user/<uid>` remains the guess
  only where `XDG_RUNTIME_DIR` names no absolute path. Pinned by
  `TestTheSessionBusIsFoundInTheRuntimeDirectory` and
  `TestARuntimeDirectoryWithoutABusHasNoSessionBus`.
- Import path: every source imports `github.com/stubbedev/lys-lib/third_party/godbus`
  in place of `github.com/godbus/dbus/v5`, and the copy has no go.mod
  of its own. Upstream's tests, CI files and fuzz corpus are not
  carried; the tests named above pin the patches, the bus-dependent ones
  (`lys_bus_test.go`) on a private bus from `dbustest`.

The copy follows lys-lib's Go version, with golangci-lint's modernize fixes
applied to the v5.2.2 sources: `reflect.TypeFor` and `TypeAssert`,
`strings.SplitSeq` and `CutLast`, range over ints and over
`Fields`/`Methods`, `slices.Backward` and `strings.Builder` where upstream
predates them. No behaviour changes; upstream's tests passed under -race
on a private session bus when they were applied, in lys-shell. The
v5.2.2..v5.2.4 changes are carried as upstream wrote them.

## No dbus-launch

`getSessionBusPlatformAddress` returns `ErrNoSessionBus` where upstream runs
`dbus-launch`. That forked a session bus nothing ever stopped, once per call: test
runs without a bus left 596 daemons behind, which used up the host's inotify
instances, and a program joined a bus no other program was on.
