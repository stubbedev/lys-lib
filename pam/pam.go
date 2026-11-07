// Package pam binds libpam through purego, so a program stays
// CGO_ENABLED=0: transactions, their conversation, and the items lys
// reads back.
package pam

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/stubbedev/lys-lib/clib"
)

// libpamCandidates is where libpam is looked up.
var libpamCandidates = clib.SystemCandidates("libpam.so.0")

// libcCandidates holds the C heap allocator PAM frees our responses
// with: responses must be malloc'd memory.
var libcCandidates = []string{"libc.so.6"}

// cPamMessage and cPamResponse mirror struct pam_message and struct
// pam_response on LP64 Linux.
type cPamMessage struct {
	style int32
	msg   *byte
}

type cPamResponse struct {
	resp    unsafe.Pointer
	retcode int32
}

// cPamConv mirrors struct pam_conv.
type cPamConv struct {
	conv    uintptr
	appdata uintptr
}

// libpam holds the bound entry points.
type libpam struct {
	start        func(service, user string, conv unsafe.Pointer, pamh *uintptr) int32
	startConfdir func(service, user string, conv unsafe.Pointer, confdir string, pamh *uintptr) int32
	authenticate func(pamh uintptr, flags int32) int32
	acctMgmt     func(pamh uintptr, flags int32) int32
	chauthtok    func(pamh uintptr, flags int32) int32
	setcred      func(pamh uintptr, flags int32) int32
	openSession  func(pamh uintptr, flags int32) int32
	closeSession func(pamh uintptr, flags int32) int32
	putenv       func(pamh uintptr, kv string) int32
	getenvlist   func(pamh uintptr) unsafe.Pointer
	end          func(pamh uintptr, status int32) int32
	strerror     func(pamh uintptr, code int32) string
	getItem      func(pamh uintptr, itemType int32, item *unsafe.Pointer) int32
	calloc       func(n, size uintptr) unsafe.Pointer
	free         func(p unsafe.Pointer)
	strdup       func(s string) unsafe.Pointer
	callback     uintptr
}

var (
	libOnce sync.Once
	lib     *libpam
	errLib  error
)

// loadLibpam binds libpam and libc once per process.
func loadLibpam() (*libpam, error) {
	libOnce.Do(func() { lib, errLib = bindLibpam() })
	return lib, errLib
}

// CheckLibrary reports whether libpam and libc bind.
func CheckLibrary() error {
	_, err := loadLibpam()
	return err
}

func bindLibpam() (*libpam, error) {
	pam, err := clib.Open(libpamCandidates)
	if err != nil {
		return nil, fmt.Errorf("load libpam: %w", err)
	}
	libc, err := clib.Open(libcCandidates)
	if err != nil {
		return nil, fmt.Errorf("load libc: %w", err)
	}
	l := &libpam{}
	purego.RegisterLibFunc(&l.start, pam, "pam_start")
	purego.RegisterLibFunc(&l.authenticate, pam, "pam_authenticate")
	purego.RegisterLibFunc(&l.acctMgmt, pam, "pam_acct_mgmt")
	purego.RegisterLibFunc(&l.chauthtok, pam, "pam_chauthtok")
	purego.RegisterLibFunc(&l.setcred, pam, "pam_setcred")
	purego.RegisterLibFunc(&l.openSession, pam, "pam_open_session")
	purego.RegisterLibFunc(&l.closeSession, pam, "pam_close_session")
	purego.RegisterLibFunc(&l.putenv, pam, "pam_putenv")
	purego.RegisterLibFunc(&l.getenvlist, pam, "pam_getenvlist")
	purego.RegisterLibFunc(&l.end, pam, "pam_end")
	purego.RegisterLibFunc(&l.strerror, pam, "pam_strerror")
	purego.RegisterLibFunc(&l.getItem, pam, "pam_get_item")
	// pam_start_confdir (Linux-PAM 1.4+) is optional: only the tests
	// point a transaction at a private service directory.
	if _, err := purego.Dlsym(pam, "pam_start_confdir"); err == nil {
		purego.RegisterLibFunc(&l.startConfdir, pam, "pam_start_confdir")
	}
	purego.RegisterLibFunc(&l.calloc, libc, "calloc")
	purego.RegisterLibFunc(&l.free, libc, "free")
	purego.RegisterLibFunc(&l.strdup, libc, "strdup")
	l.callback = purego.NewCallback(pamConverseTrampoline)
	return l, nil
}

// conversations routes the C callback to the transaction that owns
// it, by the appdata key.
var conversations = struct {
	sync.Mutex
	next uintptr
	m    map[uintptr]Converse
}{m: make(map[uintptr]Converse)}

// pamConverseTrampoline is the C conversation function:
// int conv(int num_msg, const struct pam_message **msg,
// struct pam_response **resp, void *appdata_ptr).
func pamConverseTrampoline(num int32, msg, resp unsafe.Pointer, appdata uintptr) int32 {
	conversations.Lock()
	converse := conversations.m[appdata]
	conversations.Unlock()
	if converse == nil || num <= 0 || msg == nil || resp == nil || lib == nil {
		return ConvErr
	}
	ptrs := unsafe.Slice((**cPamMessage)(msg), num) //nolint:gosec // audited: PAM passes num_msg message pointers
	msgs := make([]Message, num)
	for i, p := range ptrs {
		msgs[i] = Message{Style: Style(p.style), Text: clib.GoString(p.msg)}
	}
	replies, code := converse(msgs)
	if code != Success {
		return code
	}
	for _, r := range replies {
		// A NUL cannot cross into C: it would silently truncate the
		// answer (the pam crate's CString::new refuses it the same way).
		if r.Set && strings.IndexByte(r.Text, 0) >= 0 {
			return ConvErr
		}
	}
	arr := lib.calloc(uintptr(num), unsafe.Sizeof(cPamResponse{}))
	if arr == nil {
		return BufErr
	}
	out := unsafe.Slice((*cPamResponse)(arr), num) //nolint:gosec // audited: arr was calloc(num, sizeof response)
	for i, r := range replies {
		if !r.Set {
			continue
		}
		out[i].resp = lib.strdup(r.Text)
		if out[i].resp == nil {
			for _, o := range out[:i] {
				lib.free(o.resp)
			}
			lib.free(arr)
			return BufErr
		}
	}
	*(*unsafe.Pointer)(resp) = arr
	return Success
}

// Start opens a transaction for service and user whose conversation
// calls converse for every batch of messages. confdir, when set, reads
// the service from that directory with pam_start_confdir instead of
// /etc/pam.d (tests).
func Start(service, user, confdir string, converse Converse) (*Txn, error) {
	l, err := loadLibpam()
	if err != nil {
		return nil, err
	}
	if confdir != "" && l.startConfdir == nil {
		return nil, errors.New("libpam has no pam_start_confdir")
	}
	conversations.Lock()
	conversations.next++
	key := conversations.next
	conversations.m[key] = converse
	conversations.Unlock()

	// struct pam_conv lives on the C heap: PAM keeps the pointer for
	// the whole transaction, which Go memory must not be handed for.
	conv := l.calloc(1, unsafe.Sizeof(cPamConv{}))
	if conv == nil {
		dropConversation(key)
		return nil, errors.New("out of memory")
	}
	*(*cPamConv)(conv) = cPamConv{conv: l.callback, appdata: key}

	var handle uintptr
	var rc int32
	if confdir != "" {
		rc = l.startConfdir(service, user, conv, confdir, &handle)
	} else {
		rc = l.start(service, user, conv, &handle)
	}
	if rc != Success {
		dropConversation(key)
		l.free(conv)
		return nil, fmt.Errorf("pam_start: %s", l.strerror(0, rc))
	}
	return &Txn{l: l, handle: handle, key: key, conv: conv}, nil
}

func dropConversation(key uintptr) {
	conversations.Lock()
	delete(conversations.m, key)
	conversations.Unlock()
}

// Txn is one libpam transaction.
type Txn struct {
	l      *libpam
	handle uintptr
	key    uintptr
	conv   unsafe.Pointer
}

// Authenticate is pam_authenticate.
func (t *Txn) Authenticate() int32 { return t.l.authenticate(t.handle, 0) }

// AcctMgmt is pam_acct_mgmt.
func (t *Txn) AcctMgmt() int32 { return t.l.acctMgmt(t.handle, 0) }

// Chauthtok is pam_chauthtok.
func (t *Txn) Chauthtok(flags int32) int32 { return t.l.chauthtok(t.handle, flags) }

// Setcred is pam_setcred.
func (t *Txn) Setcred(flags int32) int32 { return t.l.setcred(t.handle, flags) }

// OpenSession is pam_open_session.
func (t *Txn) OpenSession() int32 { return t.l.openSession(t.handle, 0) }

// CloseSession is pam_close_session.
func (t *Txn) CloseSession() int32 { return t.l.closeSession(t.handle, 0) }

// Putenv is pam_putenv.
func (t *Txn) Putenv(kv string) int32 { return t.l.putenv(t.handle, kv) }

// Getenvlist is the transaction's environment, the C list freed once
// copied.
func (t *Txn) Getenvlist() []string {
	list := t.l.getenvlist(t.handle)
	if list == nil {
		return nil
	}
	var env []string
	for p := (*unsafe.Pointer)(list); *p != nil; p = (*unsafe.Pointer)(unsafe.Add(unsafe.Pointer(p), unsafe.Sizeof(uintptr(0)))) { //nolint:gosec // audited: a NULL-terminated char** from pam_getenvlist
		env = append(env, clib.GoString((*byte)(*p)))
		t.l.free(*p)
	}
	t.l.free(list)
	return env
}

// Strerror is pam_strerror.
func (t *Txn) Strerror(code int32) string { return t.l.strerror(t.handle, code) }

// Authtok is a copy of the PAM_AUTHTOK item, nil when unset.
func (t *Txn) Authtok() []byte {
	var item unsafe.Pointer
	if t.l.getItem(t.handle, ItemAuthtok, &item) != Success || item == nil {
		return nil
	}
	n := 0
	for *(*byte)(unsafe.Add(item, n)) != 0 {
		n++
	}
	return bytes.Clone(unsafe.Slice((*byte)(item), n)) //nolint:gosec // audited: the NUL-terminated char* PAM owns, n bytes long
}

// End closes the transaction and releases the conversation.
func (t *Txn) End(status int32) {
	t.l.end(t.handle, status)
	dropConversation(t.key)
	t.l.free(t.conv)
}

// PAM return codes (security/_pam_types.h).
const (
	Success = 0
	BufErr  = 5
	ConvErr = 19
)

// ItemAuthtok is PAM_AUTHTOK.
const ItemAuthtok = 6

// Style is a conversation message's PAM style.
type Style int32

// The message styles (security/_pam_types.h).
const (
	PromptEchoOff Style = 1
	PromptEchoOn  Style = 2
	ErrorMsg      Style = 3
	TextInfo      Style = 4
)

// Message is one conversation message, decoded from C.
type Message struct {
	Style Style
	Text  string
}

// Reply is one conversation answer; Set false leaves the C response
// slot NULL (info and error messages).
type Reply struct {
	Text string
	Set  bool
}

// Converse answers one batch of messages, or returns a PAM error code
// that ends the conversation.
type Converse func([]Message) ([]Reply, int32)
