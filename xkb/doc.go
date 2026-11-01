// Package xkb is a pure-Go implementation of the XKB keyboard model: compiling
// keymaps from rules, model, layout, variant and options, parsing and
// serializing keymap text in the xkb_v1 and libxkbcommon v2 formats, tracking
// modifier, layout and LED state, composing keysym sequences into text, and
// reading keymaps from an X server. It reads xkeyboard-config and Compose data
// from the system and links no C. Package registry lists what a ruleset offers,
// and package x11 is an X11 connection for SetupX11.
//
// Its behaviour follows libxkbcommon 1.13, whose sources (MIT) it was written
// against, and the differential tests hold it to that library across every
// layout, variant, option and model xkeyboard-config offers, in both keymap
// formats. docs/xkb.md maps every libxkbcommon function to its Go equivalent,
// and a test fails when a header declares one the table misses. See
// LICENSE.libxkbcommon.
package xkb
