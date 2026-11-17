// Package storetest is a contract test suite every store.Store must pass.
package storetest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/yashchhabra57/recallroute/internal/domain"
	"github.com/yashchhabra57/recallroute/internal/lotcode"
	"github.com/yashchhabra57/recallroute/internal/store"
	"github.com/yashchhabra57/recallroute/internal/workflow"
)

// Run runs the suite. newStore must return an empty store.
func Run(t *testing.T, newStore func(t *testing.T) store.Store) {
	tests := map[string]func(*testing.T, store.Store){
		"UpsertRecallsIsIdempotent":      testUpsertIdempotent,
		"RecallRoundTrip":                testRecallRoundTrip,
		"LotsRoundTrip":                  testLotsRoundTrip,
		"CreateAlertsSkipsDuplicates":    testCreateAlertsDedup,
		"ListAlertsFiltersByState":       testListAlertsFilter,
		"TransitionAppliesAtomically":    testTransition,
		"TransitionVersionConflict":      testTransitionConflict,
		"TransitionErrorChangesNothing":  testTransitionError,
		"ConcurrentTransitionsOneWinner": testConcurrentTransitions,
		"NotFound":                       testNotFound,
	}
	for name, fn := range tests {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			defer s.Close()
			fn(t, s)
		})
	}
}

var ctx = context.Background()

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func recall(id string) domain.Recall {
	return domain.Recall{
		ID: id, Firm: "Northwoods Creamery LLC", Product: "Cheese curds UPC 071234001019",
		Reason: "Listeria", Classification: "Class I", Status: "Ongoing",
		CodeInfo: "Lot 2341A through 2349A", GTINs: []string{"00071234001019"},
		Spec:        lotcode.Parse("Lot 2341A through 2349A"),
		InitiatedOn: date(2026, 11, 2), ReportedOn: date(2026, 11, 11), Source: "openfda",
	}
}

func lot(name string, qty int) domain.Lot {
	return domain.Lot{SKU: "CUR-12", Name: name, Brand: "Northwoods", GTIN: "00071234001019",
		LotCode: "2345A", BestBy: date(2026, 12, 10), Quantity: qty, Location: "Cooler B"}
}

// seed creates one recall, one lot (qty 24) and one new alert.
func seed(t *testing.T, s store.Store) (domain.Recall, domain.Lot, domain.Alert) {
	t.Helper()
	r := recall("F-1")
	if _, err := s.UpsertRecalls(ctx, []domain.Recall{r}); err != nil {
		t.Fatal(err)
	}
	lots, err := s.AddLots(ctx, []domain.Lot{lot("Cheddar Curds", 24)})
	if err != nil {
		t.Fatal(err)
	}
	as, err := s.CreateAlerts(ctx, []domain.Alert{{RecallID: r.ID, LotID: lots[0].ID, Tier: domain.TierConfirmed, Score: 1, Reasons: []string{"barcode matches"}}})
	if err != nil || len(as) != 1 {
		t.Fatalf("seed alerts: %v %v", as, err)
	}
	return r, lots[0], as[0]
}

func testUpsertIdempotent(t *testing.T, s store.Store) {
	got, err := s.UpsertRecalls(ctx, []domain.Recall{recall("F-1"), recall("F-2")})
	if err != nil || !reflect.DeepEqual(got, []string{"F-1", "F-2"}) {
		t.Fatalf("first upsert: %v %v", got, err)
	}
	got, err = s.UpsertRecalls(ctx, []domain.Recall{recall("F-1"), recall("F-2")})
	if err != nil || len(got) != 0 {
		t.Fatalf("unchanged upsert should report nothing: %v %v", got, err)
	}
	changed := recall("F-2")
	changed.Status = "Terminated"
	got, err = s.UpsertRecalls(ctx, []domain.Recall{recall("F-1"), changed})
	if err != nil || !reflect.DeepEqual(got, []string{"F-2"}) {
		t.Fatalf("changed upsert: %v %v", got, err)
	}
	r, _ := s.GetRecall(ctx, "F-2")
	if r.Status != "Terminated" {
		t.Errorf("status not updated: %s", r.Status)
	}
	all, _ := s.ListRecalls(ctx)
	if len(all) != 2 {
		t.Errorf("list = %d", len(all))
	}
}

func testRecallRoundTrip(t *testing.T, s store.Store) {
	want := recall("F-9")
	want.GTINsInferred = true
	if _, err := s.UpsertRecalls(ctx, []domain.Recall{want}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRecall(ctx, "F-9")
	if err != nil {
		t.Fatal(err)
	}
	if !got.InitiatedOn.Equal(want.InitiatedOn) || !got.ReportedOn.Equal(want.ReportedOn) {
		t.Errorf("dates: %v %v", got.InitiatedOn, got.ReportedOn)
	}
	got.InitiatedOn, got.ReportedOn = want.InitiatedOn, want.ReportedOn
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}
}

func testLotsRoundTrip(t *testing.T, s store.Store) {
	noDate := lot("Mystery can", 3)
	noDate.BestBy = time.Time{}
	noDate.GTIN = ""
	in := []domain.Lot{lot("A", 1), noDate}
	out, err := s.AddLots(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].ID == 0 || out[1].ID == out[0].ID {
		t.Fatalf("ids not assigned: %+v", out)
	}
	got, err := s.GetLot(ctx, out[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.BestBy.IsZero() || got.GTIN != "" || got.Quantity != 3 || got.Name != "Mystery can" {
		t.Errorf("got %+v", got)
	}
	a, _ := s.GetLot(ctx, out[0].ID)
	if !a.BestBy.Equal(date(2026, 12, 10)) {
		t.Errorf("best-by = %v", a.BestBy)
	}
	all, _ := s.ListLots(ctx)
	if len(all) != 2 || all[0].ID != out[0].ID {
		t.Errorf("list = %+v", all)
	}
}

func testCreateAlertsDedup(t *testing.T, s store.Store) {
	r, l, a := seed(t, s)
	if a.State != domain.StateNew || a.Version != 1 || !reflect.DeepEqual(a.Reasons, []string{"barcode matches"}) {
		t.Errorf("seeded alert = %+v", a)
	}
	again, err := s.CreateAlerts(ctx, []domain.Alert{{RecallID: r.ID, LotID: l.ID, Tier: domain.TierProbable}})
	if err != nil || len(again) != 0 {
		t.Errorf("duplicate created: %+v %v", again, err)
	}
	if _, err := s.CreateAlerts(ctx, []domain.Alert{{RecallID: "nope", LotID: l.ID, Tier: domain.TierProbable}}); err == nil {
		t.Error("alert for unknown recall should fail")
	}
}

func testListAlertsFilter(t *testing.T, s store.Store) {
	_, l, a := seed(t, s)
	if _, err := s.UpsertRecalls(ctx, []domain.Recall{recall("F-2")}); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateAlerts(ctx, []domain.Alert{{RecallID: "F-2", LotID: l.ID, Tier: domain.TierProbable}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(ctx, a.ID, 1, ack); err != nil {
		t.Fatal(err)
	}
	all, _ := s.ListAlerts(ctx, "")
	if len(all) != 2 || all[0].ID != b[0].ID {
		t.Errorf("all (newest first) = %+v", all)
	}
	news, _ := s.ListAlerts(ctx, domain.StateNew)
	if len(news) != 1 || news[0].ID != b[0].ID {
		t.Errorf("new = %+v", news)
	}
}

func ack(a domain.Alert, l domain.Lot) (workflow.Result, error) {
	return workflow.Apply(a, l, workflow.Command{Action: workflow.Acknowledge, Actor: "sam"}, time.Now().UTC())
}

func pull(n int) store.TransitionFunc {
	return func(a domain.Alert, l domain.Lot) (workflow.Result, error) {
		return workflow.Apply(a, l, workflow.Command{Action: workflow.Pull, Actor: "sam", Quantity: n}, time.Now().UTC())
	}
}

func testTransition(t *testing.T, s store.Store) {
	_, l, a := seed(t, s)
	if _, err := s.Transition(ctx, a.ID, 1, ack); err != nil {
		t.Fatal(err)
	}
	res, err := s.Transition(ctx, a.ID, 2, pull(20))
	if err != nil {
		t.Fatal(err)
	}
	if res.Alert.State != domain.StatePulled || res.Alert.PulledQty != 20 || res.Event.ID == 0 {
		t.Errorf("result = %+v", res)
	}
	gotLot, _ := s.GetLot(ctx, l.ID)
	if gotLot.Quantity != 4 {
		t.Errorf("lot quantity = %d, want 4", gotLot.Quantity)
	}
	gotAlert, _ := s.GetAlert(ctx, a.ID)
	if gotAlert.Version != 3 || gotAlert.PulledQty != 20 {
		t.Errorf("alert = %+v", gotAlert)
	}
	evs, err := s.Events(ctx, a.ID)
	if err != nil || len(evs) != 2 {
		t.Fatalf("events = %+v %v", evs, err)
	}
	if evs[0].Action != "acknowledge" || evs[1].Quantity != 20 || evs[1].From != domain.StateAcknowledged {
		t.Errorf("events = %+v", evs)
	}
	notify := func(a domain.Alert, l domain.Lot) (workflow.Result, error) {
		return workflow.Apply(a, l, workflow.Command{Action: workflow.Notify, Actor: "sam", Recipients: []string{"Hope Pantry"}}, time.Now().UTC())
	}
	if _, err := s.Transition(ctx, a.ID, 3, notify); err != nil {
		t.Fatal(err)
	}
	evs, _ = s.Events(ctx, a.ID)
	if !reflect.DeepEqual(evs[2].Recipients, []string{"Hope Pantry"}) {
		t.Errorf("recipients = %v", evs[2].Recipients)
	}
}

func testTransitionConflict(t *testing.T, s store.Store) {
	_, _, a := seed(t, s)
	if _, err := s.Transition(ctx, a.ID, 5, ack); !errors.Is(err, store.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func testTransitionError(t *testing.T, s store.Store) {
	_, l, a := seed(t, s)
	_, err := s.Transition(ctx, a.ID, 1, pull(5)) // pull from "new" is invalid
	if !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Fatalf("err = %v", err)
	}
	got, _ := s.GetAlert(ctx, a.ID)
	gotLot, _ := s.GetLot(ctx, l.ID)
	evs, _ := s.Events(ctx, a.ID)
	if got.Version != 1 || got.State != domain.StateNew || gotLot.Quantity != 24 || len(evs) != 0 {
		t.Errorf("state changed after failed transition: %+v %+v %v", got, gotLot, evs)
	}
}

func testConcurrentTransitions(t *testing.T, s store.Store) {
	_, _, a := seed(t, s)
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.Transition(ctx, a.ID, 1, ack)
		}(i)
	}
	wg.Wait()
	ok, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, store.ErrConflict):
			conflicts++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || conflicts != n-1 {
		t.Errorf("ok=%d conflicts=%d", ok, conflicts)
	}
	evs, _ := s.Events(ctx, a.ID)
	if len(evs) != 1 {
		t.Errorf("events = %d, want 1", len(evs))
	}
}

func testNotFound(t *testing.T, s store.Store) {
	if _, err := s.GetRecall(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetRecall: %v", err)
	}
	if _, err := s.GetLot(ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetLot: %v", err)
	}
	if _, err := s.GetAlert(ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetAlert: %v", err)
	}
	if _, err := s.Events(ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Events: %v", err)
	}
	if _, err := s.Transition(ctx, 999, 1, ack); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Transition: %v", err)
	}
}
