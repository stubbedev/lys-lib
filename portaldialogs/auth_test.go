package portaldialogs

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestResponseRedacts(t *testing.T) {
	r := NewResponse("hunter2")
	if r.Reveal() != "hunter2" {
		t.Fatalf("Reveal = %q", r.Reveal())
	}
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("answered", "response", r, "reply", AuthAnswer{Response: r})
	for _, out := range []string{
		fmt.Sprint(r), fmt.Sprintf("%s %q %x %d %+v %#v", r, r, r, r, r, r),
		fmt.Sprintf("%v %+v %#v", AuthAnswer{Response: r}, AuthAnswer{Response: r}, AuthAnswer{Response: r}),
		fmt.Sprint([]AuthReply{AuthAnswer{Response: r}}), logged.String(),
	} {
		if strings.Contains(out, "hunter2") || !strings.Contains(out, redacted) {
			t.Errorf("printed as %q", out)
		}
	}
}

func TestReplyEncoding(t *testing.T) {
	r := AuthRequest{Identities: []AuthIdentity{{0, "root"}, {1000, "Ada"}}, Selected: 1000}
	for _, reply := range []AuthReply{
		AuthAnswer{Response: NewResponse("pw")}, AuthAnswer{}, AuthSwitch{UID: 0}, AuthCancel{},
	} {
		ok, uid, response, err := encodeReply(r, reply)
		if err != nil {
			t.Errorf("encode %#v: %v", reply, err)
			continue
		}
		if got, err := decodeReply(r, ok, uid, response); err != nil || got != reply {
			t.Errorf("%#v travels as (%v, %d) and reads back as %#v, %v", reply, ok, uid, got, err)
		}
	}
	for _, reply := range []AuthReply{nil, AuthSwitch{UID: 1000}, AuthSwitch{UID: 42}} {
		if _, _, _, err := encodeReply(r, reply); err == nil {
			t.Errorf("encoded %#v", reply)
		}
	}
	for _, tc := range []struct {
		ok       bool
		uid      uint32
		response string
	}{
		{false, 1000, ""}, {false, 0, "pw"}, {true, 42, ""}, {true, 0, "pw"},
	} {
		if got, err := decodeReply(r, tc.ok, tc.uid, tc.response); err == nil {
			t.Errorf("decoded (%v, %d, %q) as %#v", tc.ok, tc.uid, tc.response, got)
		}
	}
}

func TestAuthRequestCheck(t *testing.T) {
	ids := []AuthIdentity{{0, "root"}, {1000, "Ada"}}
	if err := (AuthRequest{Identities: ids, Selected: 1000}).check(); err != nil {
		t.Errorf("selected identity refused: %v", err)
	}
	for _, r := range []AuthRequest{{}, {Identities: ids, Selected: 42}} {
		if err := r.check(); err == nil {
			t.Errorf("%+v passed", r)
		}
	}
}
