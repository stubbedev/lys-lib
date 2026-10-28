package portaldialogs

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
)

// AuthRequest is one step of an administrator authentication (polkit's
// PAM conversation), (ssa(ss)a(us)ua(bs)sb): the dialog for a handle
// stays up across the steps and shows the latest.
type AuthRequest struct {
	// Message is what the action asks for and Icon its themed icon
	// name, "" for none.
	Message, Icon string
	// Details are shown labelled, in order (the program and command
	// line).
	Details []AuthDetail
	// Identities are who may authenticate; Selected is the uid of the
	// one the prompt is for, and one of them.
	Identities []AuthIdentity
	Selected   uint32
	// Notes are the conversation's messages so far, in arrival order.
	Notes []AuthNote
	// Prompt is what the entry asks for; Echo shows what is typed.
	Prompt string
	Echo   bool
}

// AuthDetail is one labelled line about the action, (ss).
type AuthDetail struct{ Label, Value string }

// AuthIdentity is one user who may authenticate, (us): the uid and the
// name shown for it.
type AuthIdentity struct {
	UID  uint32
	Name string
}

// AuthNote is one message of the conversation, (bs): Error says it is
// an error rather than information.
type AuthNote struct {
	Error bool
	Text  string
}

func (r AuthRequest) identity(uid uint32) bool {
	return slices.ContainsFunc(r.Identities, func(i AuthIdentity) bool { return i.UID == uid })
}

func (r AuthRequest) check() error {
	if !r.identity(r.Selected) {
		return fmt.Errorf("selected uid %d is not one of the %d identities", r.Selected, len(r.Identities))
	}
	return nil
}

// Response is the answer typed at an authentication prompt. It formats
// and logs as redacted; only Reveal gives the text.
type Response struct{ text string }

const redacted = "<redacted>"

// NewResponse holds text as a response.
func NewResponse(text string) Response { return Response{text: text} }

// Reveal is the text typed.
func (r Response) Reveal() string { return r.text }

// Format writes the response as redacted for every verb.
func (Response) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// LogValue logs the response as redacted.
func (Response) LogValue() slog.Value { return slog.StringValue(redacted) }

// AuthReply is how the user ended an Authenticate call: an AuthAnswer,
// or an AuthAction.
type AuthReply interface{ authReply() }

// AuthAction is what the user did in an authentication dialog other
// than answering its prompt: AuthCancel or AuthSwitch. With no
// Authenticate call waiting, the host signals it instead of replying.
type AuthAction interface {
	AuthReply
	authAction()
}

// AuthAnswer is the response typed at the prompt for the selected
// identity.
type AuthAnswer struct{ Response Response }

// AuthCancel is the user cancelling the authentication.
type AuthCancel struct{}

// AuthSwitch is the user choosing to authenticate as the identity UID
// instead; the caller restarts the conversation for it.
type AuthSwitch struct{ UID uint32 }

func (AuthAnswer) authReply()  {}
func (AuthCancel) authReply()  {}
func (AuthSwitch) authReply()  {}
func (AuthCancel) authAction() {}
func (AuthSwitch) authAction() {}

// encodeReply is the (ok, uid, response) the reply travels as: a
// cancel is (false, 0, ""), an answer carries the selected uid, and a
// switch carries another identity's uid and no response.
func encodeReply(r AuthRequest, reply AuthReply) (ok bool, uid uint32, response string, err error) {
	switch reply := reply.(type) {
	case AuthAnswer:
		return true, r.Selected, reply.Response.text, nil
	case AuthCancel:
		return false, 0, "", nil
	case AuthSwitch:
		if reply.UID == r.Selected {
			return false, 0, "", fmt.Errorf("switch to uid %d, which is already selected", reply.UID)
		}
		if !r.identity(reply.UID) {
			return false, 0, "", fmt.Errorf("switch to uid %d, which is not one of the identities", reply.UID)
		}
		return true, reply.UID, "", nil
	case nil:
		return false, 0, "", errors.New("no reply")
	default:
		return false, 0, "", fmt.Errorf("unknown reply %T", reply)
	}
}

// decodeReply reads the (ok, uid, response) answering r back into its
// reply, refusing one encodeReply cannot produce.
func decodeReply(r AuthRequest, ok bool, uid uint32, response string) (AuthReply, error) {
	switch {
	case !ok && uid == 0 && response == "":
		return AuthCancel{}, nil
	case !ok:
		return nil, fmt.Errorf("cancel carrying uid %d or a response", uid)
	case uid == r.Selected:
		return AuthAnswer{Response: NewResponse(response)}, nil
	case !r.identity(uid):
		return nil, fmt.Errorf("reply for uid %d, which is not one of the identities", uid)
	case response != "":
		return nil, fmt.Errorf("switch to uid %d carrying a response", uid)
	default:
		return AuthSwitch{UID: uid}, nil
	}
}
