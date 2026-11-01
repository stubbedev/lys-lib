// Package x11 is a minimal X11 client for xkb.SetupX11: a connection to an X
// server over its Unix socket, authenticated from the Xauthority file, that
// carries requests with one reply each. It exists so that reading a keymap
// from an X server needs no C library and no X client library.
package x11

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	replyError   = 0
	replyReply   = 1
	headerLen    = 32
	socketDir    = "/tmp/.X11-unix"
	authProtocol = "MIT-MAGIC-COOKIE-1"
	familyLocal  = 256
	dialTimeout  = 5 * time.Second
	replyTimeout = 30 * time.Second
)

// Conn is a connection to an X server. Safe for concurrent use.
type Conn struct {
	mu   sync.Mutex
	conn net.Conn
	seq  uint16
}

// Error is an X protocol error.
type Error struct {
	Code        byte
	Sequence    uint16
	BadValue    uint32
	MinorOpcode uint16
	MajorOpcode byte
}

func (e *Error) Error() string {
	return fmt.Sprintf("x11: error %d on request %d.%d (value %#x)", e.Code, e.MajorOpcode, e.MinorOpcode, e.BadValue)
}

// Dial connects to a display such as ":0", or to $DISPLAY when display is
// empty. getenv reads DISPLAY, XAUTHORITY and HOME.
func Dial(display string, getenv func(string) string) (*Conn, error) {
	if display == "" {
		display = getenv("DISPLAY")
	}
	number, err := displayNumber(display)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(socketDir, "X"+number)
	nc, err := net.DialTimeout("unix", path, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("x11: connect to %s: %w", display, err)
	}
	c := &Conn{conn: nc}
	if err := c.setup(number, getenv); err != nil {
		return nil, errors.Join(fmt.Errorf("x11: set up %s: %w", display, err), nc.Close())
	}
	return c, nil
}

func displayNumber(display string) (string, error) {
	_, rest, ok := strings.Cut(display, ":")
	if !ok {
		return "", fmt.Errorf("x11: bad display %q", display)
	}
	number, _, _ := strings.Cut(rest, ".")
	if _, err := strconv.Atoi(number); err != nil {
		return "", fmt.Errorf("x11: bad display %q: %w", display, err)
	}
	return number, nil
}

// Close closes the connection.
func (c *Conn) Close() error {
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("x11: close: %w", err)
	}
	return nil
}

func pad4(n int) int { return (4 - n%4) % 4 }

func (c *Conn) setup(display string, getenv func(string) string) error {
	name, data := authCookie(display, getenv)
	req := make([]byte, 12, 12+len(name)+pad4(len(name))+len(data)+pad4(len(data)))
	req[0] = 'l'
	binary.LittleEndian.PutUint16(req[2:], 11)
	binary.LittleEndian.PutUint16(req[6:], uint16(len(name))) //nolint:gosec // a fixed protocol name
	binary.LittleEndian.PutUint16(req[8:], uint16(len(data))) //nolint:gosec // a cookie from the file
	req = append(req, name...)
	req = append(req, make([]byte, pad4(len(name)))...)
	req = append(req, data...)
	req = append(req, make([]byte, pad4(len(data)))...)
	if err := c.conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		return fmt.Errorf("deadline: %w", err)
	}
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()
	if _, err := c.conn.Write(req); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	var head [8]byte
	if _, err := io.ReadFull(c.conn, head[:]); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	rest := make([]byte, int(binary.LittleEndian.Uint16(head[6:]))*4)
	if _, err := io.ReadFull(c.conn, rest); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if head[0] != 1 {
		reason := rest
		if n := int(head[1]); n <= len(rest) {
			reason = rest[:n]
		}
		return fmt.Errorf("refused: %s", reason)
	}
	return nil
}

func authCookie(display string, getenv func(string) string) (name string, data []byte) {
	path := getenv("XAUTHORITY")
	if path == "" {
		home := getenv("HOME")
		if home == "" {
			return "", nil
		}
		path = filepath.Join(home, ".Xauthority")
	}
	src, err := os.ReadFile(path) //nolint:gosec // the file X11 names for this purpose
	if err != nil {
		return "", nil
	}
	hostname, _ := os.Hostname()
	return findCookie(src, display, hostname)
}

func findCookie(src []byte, display, hostname string) (string, []byte) {
	field := func() ([]byte, bool) {
		if len(src) < 2 {
			return nil, false
		}
		n := int(binary.BigEndian.Uint16(src))
		if len(src) < 2+n {
			return nil, false
		}
		out := src[2 : 2+n]
		src = src[2+n:]
		return out, true
	}
	for len(src) >= 2 {
		family := binary.BigEndian.Uint16(src)
		src = src[2:]
		address, ok1 := field()
		number, ok2 := field()
		name, ok3 := field()
		data, ok4 := field()
		if !ok1 || !ok2 || !ok3 || !ok4 {
			return "", nil
		}
		if string(name) != authProtocol || (len(number) > 0 && string(number) != display) {
			continue
		}
		if family == familyLocal && string(address) != hostname {
			continue
		}
		return string(name), data
	}
	return "", nil
}

// RoundTrip sends one request that has a reply and returns the reply. Events
// that arrive meanwhile are dropped.
func (c *Conn) RoundTrip(request []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.SetDeadline(time.Now().Add(replyTimeout)); err != nil {
		return nil, fmt.Errorf("x11: deadline: %w", err)
	}
	if _, err := c.conn.Write(request); err != nil {
		return nil, fmt.Errorf("x11: write request: %w", err)
	}
	c.seq++
	for {
		var head [headerLen]byte
		if _, err := io.ReadFull(c.conn, head[:]); err != nil {
			return nil, fmt.Errorf("x11: read reply: %w", err)
		}
		switch head[0] {
		case replyError:
			return nil, &Error{
				Code:        head[1],
				Sequence:    binary.LittleEndian.Uint16(head[2:]),
				BadValue:    binary.LittleEndian.Uint32(head[4:]),
				MinorOpcode: binary.LittleEndian.Uint16(head[8:]),
				MajorOpcode: head[10],
			}
		case replyReply:
			extra := int(binary.LittleEndian.Uint32(head[4:])) * 4
			reply := make([]byte, headerLen+extra)
			copy(reply, head[:])
			if _, err := io.ReadFull(c.conn, reply[headerLen:]); err != nil {
				return nil, fmt.Errorf("x11: read reply: %w", err)
			}
			return reply, nil
		}
	}
}
