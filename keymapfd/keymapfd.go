// Package keymapfd reads the xkb keymap a wl_keyboard.keymap event
// carries. It maps the fd, never reads it: a compositor may hand every
// client a dup of one shared memfd, whose file offset a read() moves
// for all of them - the next client then reads nothing and has no
// keymap at all, every key NoSymbol. Hyprland shares its keymap fd
// that way; the protocol says to mmap it.
package keymapfd

import (
	"errors"
	"fmt"
	"syscall"
)

// Read maps fd for size bytes and returns the keymap text with its
// trailing NULs cut.
func Read(fd uintptr, size uint32) ([]byte, error) {
	if size == 0 {
		return nil, errors.New("keymapfd: empty keymap")
	}
	data, err := syscall.Mmap(int(fd), 0, int(size), syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		return nil, fmt.Errorf("keymapfd: map the keymap: %w", err)
	}
	defer func() { _ = syscall.Munmap(data) }()
	text := append([]byte(nil), data...)
	for len(text) > 0 && text[len(text)-1] == 0 {
		text = text[:len(text)-1]
	}
	return text, nil
}

// Close closes a keymap fd once it is read; the event handed it over.
func Close(fd uintptr) { _ = syscall.Close(int(fd)) }
