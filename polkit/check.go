package polkit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
)

// Action is the id of a polkit action, such as
// org.freedesktop.policykit.exec.
type Action string

// CheckFlags are CheckAuthorization's flags.
type CheckFlags uint32

// AllowUserInteraction lets polkitd have the subject's authentication
// agent ask a person to authenticate, and wait for the answer, before
// it answers the check.
const AllowUserInteraction CheckFlags = 1

// AuthorizationResult is polkitd's answer to a check, (bba{ss}).
type AuthorizationResult struct {
	IsAuthorized bool
	IsChallenge  bool
	Details      map[string]string
}

// DismissedDetail is the detail polkitd sets to "true" when the person
// asked to authenticate dismissed the dialog.
const DismissedDetail = "polkit.dismissed"

// Verdict is what an AuthorizationResult says.
type Verdict uint8

// The verdicts. Challenge says authentication is needed and nobody was
// asked: the check did not allow interaction, or no agent is registered
// for the subject.
const (
	Authorized Verdict = iota + 1
	Denied
	Dismissed
	Challenge
)

// String names the verdict.
func (v Verdict) String() string {
	switch v {
	case Authorized:
		return "authorized"
	case Denied:
		return "denied"
	case Dismissed:
		return "dismissed"
	case Challenge:
		return "challenge"
	}
	return "Verdict(" + strconv.FormatUint(uint64(v), 10) + ")"
}

// Verdict reads the result.
func (r AuthorizationResult) Verdict() Verdict {
	switch {
	case r.IsAuthorized:
		return Authorized
	case r.Details[DismissedDetail] == "true":
		return Dismissed
	case r.IsChallenge:
		return Challenge
	}
	return Denied
}

// cancellations numbers the checks this process makes, so that each
// has a cancellation id of its own on whichever connection it is made.
var cancellations atomic.Uint64

// CheckAuthorization asks polkitd whether subject may perform action.
// With AllowUserInteraction the call lasts as long as the person takes
// to authenticate, so it is bounded by ctx alone; when ctx ends first
// the check is cancelled at polkitd, which takes its dialog down.
func (a Authority) CheckAuthorization(ctx context.Context, subject Subject, action Action, flags CheckFlags) (AuthorizationResult, error) {
	wire, err := subject.Wire()
	if err != nil {
		return AuthorizationResult{}, fmt.Errorf("polkit: check %s for %s: %w", action, subject, err)
	}
	id := "lys-" + strconv.FormatUint(cancellations.Add(1), 10)
	var r AuthorizationResult
	if _, err := a.call(ctx, "CheckAuthorization", []any{&r}, wire, string(action), map[string]string{}, uint32(flags), id); err != nil {
		err = fmt.Errorf("polkit: check %s for %s: %w", action, subject, err)
		if ctx.Err() != nil {
			return AuthorizationResult{}, errors.Join(err, a.cancel(ctx, id))
		}
		return AuthorizationResult{}, err
	}
	return r, nil
}

func (a Authority) cancel(ctx context.Context, id string) error {
	if _, err := a.bounded(context.WithoutCancel(ctx), "CancelCheckAuthorization", nil, id); err != nil {
		return fmt.Errorf("polkit: cancel check %s: %w", id, err)
	}
	return nil
}
