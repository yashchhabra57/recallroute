// Package workflow is the pull-and-notify state machine for alerts.
//
//	new ──acknowledge──▶ acknowledged ──pull──▶ pulled ──notify──▶ notified ──close──▶ closed
//	 │                       │                   ▲  │
//	 └──────dismiss──────────┴──▶ dismissed      └──┘ pull (more units found)
//
// Apply is a pure function: it validates a command against the current
// alert and lot and returns the new alert, the audit event and how much
// to take off the shelf. The store persists all three atomically.
package workflow

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yashchhabra57/recallroute/internal/domain"
)

// Action is a user command.
type Action string

const (
	Acknowledge Action = "acknowledge"
	Pull        Action = "pull"
	Notify      Action = "notify"
	Close       Action = "close"
	Dismiss     Action = "dismiss"
)

var (
	// ErrInvalidTransition means the action is not allowed from the
	// alert's current state.
	ErrInvalidTransition = errors.New("workflow: action not allowed in current state")
	// ErrValidation means the command is missing or has bad fields.
	ErrValidation = errors.New("workflow: invalid command")
)

// Command is what a volunteer submits.
type Command struct {
	Action     Action   `json:"action"`
	Actor      string   `json:"actor"`
	Quantity   int      `json:"quantity,omitempty"`
	Recipients []string `json:"recipients,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// Result of a successful Apply.
type Result struct {
	Alert domain.Alert
	Event domain.Event
	// QuarantineQty is how many units to remove from the lot's sellable
	// quantity (only non-zero for Pull).
	QuarantineQty int
}

var allowed = map[Action]map[domain.State]domain.State{
	Acknowledge: {domain.StateNew: domain.StateAcknowledged},
	Pull:        {domain.StateAcknowledged: domain.StatePulled, domain.StatePulled: domain.StatePulled},
	Notify:      {domain.StatePulled: domain.StateNotified},
	Close:       {domain.StateNotified: domain.StateClosed},
	Dismiss:     {domain.StateNew: domain.StateDismissed, domain.StateAcknowledged: domain.StateDismissed},
}

// Allowed lists the actions possible from a state, for UIs.
func Allowed(s domain.State) []Action {
	var out []Action
	for _, a := range []Action{Acknowledge, Pull, Notify, Close, Dismiss} {
		if _, ok := allowed[a][s]; ok {
			out = append(out, a)
		}
	}
	return out
}

// Apply validates cmd and computes the next state.
func Apply(a domain.Alert, lot domain.Lot, cmd Command, now time.Time) (Result, error) {
	cmd.Actor = strings.TrimSpace(cmd.Actor)
	cmd.Note = strings.TrimSpace(cmd.Note)
	if cmd.Actor == "" {
		return Result{}, fmt.Errorf("%w: actor is required", ErrValidation)
	}
	trans, ok := allowed[cmd.Action]
	if !ok {
		return Result{}, fmt.Errorf("%w: unknown action %q", ErrValidation, cmd.Action)
	}
	to, ok := trans[a.State]
	if !ok {
		return Result{}, fmt.Errorf("%w: cannot %s an alert that is %s", ErrInvalidTransition, cmd.Action, a.State)
	}

	ev := domain.Event{
		AlertID: a.ID,
		Action:  string(cmd.Action),
		From:    a.State,
		To:      to,
		Actor:   cmd.Actor,
		Note:    cmd.Note,
		At:      now,
	}
	res := Result{}

	switch cmd.Action {
	case Pull:
		if cmd.Quantity <= 0 {
			return Result{}, fmt.Errorf("%w: quantity must be positive", ErrValidation)
		}
		if cmd.Quantity > lot.Quantity {
			return Result{}, fmt.Errorf("%w: cannot pull %d units, only %d on the shelf", ErrValidation, cmd.Quantity, lot.Quantity)
		}
		ev.Quantity = cmd.Quantity
		res.QuarantineQty = cmd.Quantity
		a.PulledQty += cmd.Quantity
	case Notify:
		recips := clean(cmd.Recipients)
		if len(recips) == 0 && cmd.Note == "" {
			return Result{}, fmt.Errorf("%w: list the partner agencies notified, or add a note explaining why none were", ErrValidation)
		}
		ev.Recipients = recips
	case Dismiss:
		if cmd.Note == "" {
			return Result{}, fmt.Errorf("%w: a reason is required to dismiss an alert", ErrValidation)
		}
	case Acknowledge:
		if a.Tier == domain.TierReview && cmd.Note == "" {
			return Result{}, fmt.Errorf("%w: review-tier matches need a note confirming the product was checked by hand", ErrValidation)
		}
	}

	a.State = to
	a.Version++
	a.UpdatedAt = now
	res.Alert = a
	res.Event = ev
	return res, nil
}

func clean(xs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range xs {
		x = strings.TrimSpace(x)
		k := strings.ToLower(x)
		if x == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, x)
	}
	return out
}
