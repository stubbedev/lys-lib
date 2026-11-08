// Package protocols is the Wayland protocol XML every lys binding is generated
// from: the core protocol, the upstream extensions at the versions pinned in
// sources.json, and the lys-* protocols. Generators import FS; binaries do not.
package protocols

import "embed"

// FS holds every protocol as <name>.xml at its root.
//
//go:embed *.xml
var FS embed.FS
