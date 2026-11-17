package workflow

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/yashchhabra57/recallroute/internal/domain"
)

var now = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func newAlert(tier domain.Tier) domain.Alert {
	return domain.Alert{ID: 7, Tier: tier, State: domain.StateNew, Version: 1}
}

func lot(q int) domain.Lot { return domain.Lot{ID: 3, Quantity: q} }

func TestHappyPath(t *testing.T) {
	a := newAlert(domain.TierConfirmed)
	l := lot(24)
	steps := []Command{
		{Action: Acknowledge, Actor: "sam"},
		{Action: Pull, Actor: "sam", Quantity: 20},
		{Action: Pull, Actor: "sam", Quantity: 4, Note: "found 4 more in overflow"},
		{Action: Notify, Actor: "sam", Recipients: []string{"Hope Pantry", " hope pantry ", "St. Mary's"}},
		{Action: Close, Actor: "lead"},
	}
	wantStates := []domain.State{domain.StateAcknowledged, domain.StatePulled, domain.StatePulled, domain.StateNotified, domain.StateClosed}
	for i, c := range steps {
		r, err := Apply(a, l, c, now)
		if err != nil {
			t.Fatalf("step %d (%s): %v", i, c.Action, err)
		}
		if r.Alert.State != wantStates[i] || r.Alert.Version != a.Version+1 {
			t.Fatalf("step %d: state=%s version=%d", i, r.Alert.State, r.Alert.Version)
		}
		if r.Event.From != a.State || r.Event.To != r.Alert.State {
			t.Errorf("step %d: event %s->%s", i, r.Event.From, r.Event.To)
		}
		l.Quantity -= r.QuarantineQty
		a = r.Alert
		if c.Action == Notify && !reflect.DeepEqual(r.Event.Recipients, []string{"Hope Pantry", "St. Mary's"}) {
			t.Errorf("recipients = %v", r.Event.Recipients)
		}
	}
	if a.PulledQty != 24 || l.Quantity != 0 {
		t.Errorf("pulled=%d remaining=%d", a.PulledQty, l.Quantity)
	}
}

func TestInvalidTransitions(t *testing.T) {
	cases := []struct {
		state  domain.State
		action Action
	}{
		{domain.StateNew, Pull},
		{domain.StateNew, Close},
		{domain.StateAcknowledged, Notify},
		{domain.StatePulled, Dismiss},
		{domain.StateClosed, Acknowledge},
		{domain.StateDismissed, Pull},
	}
	for _, c := range cases {
		a := newAlert(domain.TierConfirmed)
		a.State = c.state
		_, err := Apply(a, lot(5), Command{Action: c.action, Actor: "x", Quantity: 1, Note: "n", Recipients: []string{"r"}}, now)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("%s from %s: err = %v", c.action, c.state, err)
		}
	}
}

func TestValidation(t *testing.T) {
	ack := newAlert(domain.TierConfirmed)
	ack.State = domain.StateAcknowledged
	pulled := ack
	pulled.State = domain.StatePulled

	cases := []struct {
		name string
		a    domain.Alert
		cmd  Command
	}{
		{"no actor", newAlert(domain.TierConfirmed), Command{Action: Acknowledge}},
		{"unknown action", newAlert(domain.TierConfirmed), Command{Action: "explode", Actor: "x"}},
		{"zero pull", ack, Command{Action: Pull, Actor: "x"}},
		{"over pull", ack, Command{Action: Pull, Actor: "x", Quantity: 6}},
		{"notify nobody", pulled, Command{Action: Notify, Actor: "x", Recipients: []string{"  "}}},
		{"dismiss without reason", newAlert(domain.TierConfirmed), Command{Action: Dismiss, Actor: "x"}},
		{"review ack without note", newAlert(domain.TierReview), Command{Action: Acknowledge, Actor: "x"}},
	}
	for _, c := range cases {
		if _, err := Apply(c.a, lot(5), c.cmd, now); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", c.name, err)
		}
	}
}

func TestNotifyWithNoteOnly(t *testing.T) {
	a := newAlert(domain.TierConfirmed)
	a.State = domain.StatePulled
	r, err := Apply(a, lot(1), Command{Action: Notify, Actor: "x", Note: "lot never left the warehouse"}, now)
	if err != nil || r.Alert.State != domain.StateNotified {
		t.Fatalf("got %+v %v", r, err)
	}
}

func TestDismissReviewMatch(t *testing.T) {
	r, err := Apply(newAlert(domain.TierReview), lot(1), Command{Action: Dismiss, Actor: "x", Note: "different brand on label"}, now)
	if err != nil || r.Alert.State != domain.StateDismissed {
		t.Fatalf("got %+v %v", r, err)
	}
}

func TestApplyDoesNotMutateInput(t *testing.T) {
	a := newAlert(domain.TierConfirmed)
	if _, err := Apply(a, lot(1), Command{Action: Acknowledge, Actor: "x"}, now); err != nil {
		t.Fatal(err)
	}
	if a.State != domain.StateNew || a.Version != 1 {
		t.Errorf("input mutated: %+v", a)
	}
}

func TestAllowed(t *testing.T) {
	if got := Allowed(domain.StateAcknowledged); !reflect.DeepEqual(got, []Action{Pull, Dismiss}) {
		t.Errorf("got %v", got)
	}
	if got := Allowed(domain.StateClosed); len(got) != 0 {
		t.Errorf("closed should be terminal, got %v", got)
	}
}
