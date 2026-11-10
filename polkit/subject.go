package polkit

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/logind"
	"github.com/stubbedev/lys-lib/peercred"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// Subject is who polkitd decides about, and whom an agent authenticates
// for: a UnixSession or a UnixProcess.
type Subject interface {
	fmt.Stringer
	// Wire is the subject as it travels to polkitd.
	Wire() (WireSubject, error)
	sealed()
}

// The subject kinds and their details (polkitsubject.c).
const (
	kindUnixSession = "unix-session"
	kindUnixProcess = "unix-process"

	detailSessionID = "session-id"
	detailPID       = "pid"
	detailStartTime = "start-time"
	detailUID       = "uid"
	detailPIDFD     = "pidfd"
)

// WireSubject is a subject as it travels on the bus, (sa{sv}): its kind
// and that kind's details. It is what an Authority receives, and what
// polkitd's replies carry.
type WireSubject struct {
	Kind    string
	Details map[string]dbus.Variant
}

// UnixSession is the unix-session subject: a logind session.
type UnixSession struct{ ID logind.SessionID }

func (UnixSession) sealed() {}

// String names the session.
func (s UnixSession) String() string { return kindUnixSession + " " + string(s.ID) }

// Wire is the session as it travels to polkitd.
func (s UnixSession) Wire() (WireSubject, error) {
	if s.ID == "" {
		return WireSubject{}, errors.New("a unix-session subject without a session id")
	}
	return WireSubject{Kind: kindUnixSession, Details: map[string]dbus.Variant{detailSessionID: dbus.MakeVariant(string(s.ID))}}, nil
}

// UnixProcess is the unix-process subject: a process by its pid, the
// start time that tells it from a later process with the same pid, and
// the uid it runs as. PIDFD, when not nil, names the process itself:
// polkit 124 and later decide about that process, whatever its pid is
// reused for, and an older polkitd ignores it. It stays the caller's to
// close, after the calls that send the subject.
type UnixProcess struct {
	PID       uint32
	StartTime uint64
	UID       uint32
	PIDFD     *peercred.PIDFD
}

func (UnixProcess) sealed() {}

// String names the process and its user.
func (p UnixProcess) String() string {
	return kindUnixProcess + " " + strconv.FormatUint(uint64(p.PID), 10) + " of uid " + strconv.FormatUint(uint64(p.UID), 10)
}

// Wire is the process as it travels to polkitd, its pidfd included when
// it has one.
func (p UnixProcess) Wire() (WireSubject, error) {
	if p.PID == 0 {
		return WireSubject{}, errors.New("a unix-process subject without a pid")
	}
	details := map[string]dbus.Variant{
		detailPID:       dbus.MakeVariant(p.PID),
		detailStartTime: dbus.MakeVariant(p.StartTime),
		detailUID:       dbus.MakeVariant(intconv.Int32Bits(p.UID)),
	}
	if p.PIDFD != nil {
		fd, err := p.PIDFD.FD()
		if err != nil {
			return WireSubject{}, fmt.Errorf("%s: %w", p, err)
		}
		details[detailPIDFD] = dbus.MakeVariant(dbus.UnixFD(intconv.Must[int32](fd)))
	}
	return WireSubject{Kind: kindUnixProcess, Details: details}, nil
}

// Subject reads a received subject. A unix-process subject's pidfd is
// not part of what it returns: PIDFD hands that over.
func (w WireSubject) Subject() (Subject, error) {
	switch w.Kind {
	case kindUnixSession:
		id, err := detail[string](w, detailSessionID)
		if err != nil {
			return nil, err
		}
		return UnixSession{ID: logind.SessionID(id)}, nil
	case kindUnixProcess:
		pid, pidErr := detail[uint32](w, detailPID)
		start, startErr := detail[uint64](w, detailStartTime)
		uid, uidErr := detail[int32](w, detailUID)
		if err := errors.Join(pidErr, startErr, uidErr); err != nil {
			return nil, err
		}
		return UnixProcess{PID: pid, StartTime: start, UID: intconv.Uint32Bits(uid)}, nil
	}
	return nil, fmt.Errorf("subject kind %q is not supported, only %s and %s", w.Kind, kindUnixSession, kindUnixProcess)
}

func detail[T any](w WireSubject, key string) (T, error) {
	var zero T
	v, ok := w.Details[key]
	if !ok {
		return zero, fmt.Errorf("%s subject without %s", w.Kind, key)
	}
	value, ok := v.Value().(T)
	if !ok {
		return zero, fmt.Errorf("%s subject with a %s of type %s, want %s", w.Kind, key, v.Signature(), dbus.SignatureOf(zero))
	}
	return value, nil
}

// PIDFD is the pidfd a received unix-process subject carries: a
// descriptor of the receiver's own, for it to close. ok is false for a
// subject that carries none.
func (w WireSubject) PIDFD() (fd dbus.UnixFD, ok bool) {
	v, has := w.Details[detailPIDFD]
	if !has || w.Kind != kindUnixProcess {
		return -1, false
	}
	fd, ok = v.Value().(dbus.UnixFD)
	return fd, ok
}

// ProcessOf is the unix-process subject for the peer of a socket, by
// the pid and uid the kernel recorded when it connected and the start
// time /proc gives that pid now. Between the two the pid may have been
// reused: ProcessOfPIDFD closes that window where the kernel has
// SO_PEERPIDFD.
func ProcessOf(c peercred.Credentials) (UnixProcess, error) {
	pid, err := intconv.Exact[uint32](c.PID)
	if err != nil || pid == 0 {
		return UnixProcess{}, fmt.Errorf("polkit: the peer of uid %d has no pid in this pid namespace (%d)", c.UID, c.PID)
	}
	path := "/proc/" + strconv.FormatUint(uint64(pid), 10) + "/stat"
	stat, err := os.ReadFile(path) //nolint:gosec // procfs of a pid the kernel named
	if err != nil {
		return UnixProcess{}, fmt.Errorf("polkit: read the start time of pid %d: %w", pid, err)
	}
	start, err := startTime(stat)
	if err != nil {
		return UnixProcess{}, fmt.Errorf("polkit: %s: %w", path, err)
	}
	return UnixProcess{PID: pid, StartTime: start, UID: c.UID}, nil
}

// ProcessOfPIDFD is the unix-process subject for the peer pidfd names,
// carrying pidfd. The peer was still running after its start time was
// read, so that start time is its own and not a later process's with
// its pid; a peer that has exited is an error.
func ProcessOfPIDFD(pidfd *peercred.PIDFD) (UnixProcess, error) {
	p, err := ProcessOf(pidfd.Credentials())
	if err != nil {
		return UnixProcess{}, err
	}
	exited, err := pidfd.Exited()
	if err != nil {
		return UnixProcess{}, fmt.Errorf("polkit: %s: %w", p, err)
	}
	if exited {
		return UnixProcess{}, fmt.Errorf("polkit: %s has exited", p)
	}
	p.PIDFD = pidfd
	return p, nil
}

// startTimeField is starttime's place in /proc/pid/stat, counted from 1
// (proc_pid_stat(5)), and firstAfterComm that of the first field after
// the command name, which may itself hold spaces and parentheses.
const (
	startTimeField = 22
	firstAfterComm = 3
)

func startTime(stat []byte) (uint64, error) {
	_, afterComm, ok := bytes.CutLast(stat, []byte(")"))
	if !ok {
		return 0, errors.New("no command name in the process's stat")
	}
	fields := bytes.Fields(afterComm)
	i := startTimeField - firstAfterComm
	if len(fields) <= i {
		return 0, fmt.Errorf("the process's stat has %d fields after its command name, want over %d", len(fields), i)
	}
	start, err := strconv.ParseUint(string(fields[i]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("start time: %w", err)
	}
	return start, nil
}
