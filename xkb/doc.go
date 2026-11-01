// Package xkb is a pure-Go implementation of the XKB keyboard model:
// compiling keymaps from rules, model, layout, variant and options,
// parsing and serializing the xkb_v1 text format wl_keyboard carries,
// tracking modifier, layout and LED state, and composing keysym sequences
// into text. It reads xkeyboard-config and Compose data from the system
// and links no C.
//
// Its behaviour follows libxkbcommon 1.13's TEXT_V1 keymap format, whose
// sources (MIT) it was written against, and the differential tests hold
// it to that library. See LICENSE.libxkbcommon.
package xkb
