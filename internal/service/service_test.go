package service

import (
	"context"
	"errors"
	"os"
	"sort"
	"sync"
	"testing"

	"github.com/yashchhabra57/recallroute/internal/domain"
	"github.com/yashchhabra57/recallroute/internal/feed"
	"github.com/yashchhabra57/recallroute/internal/inventory"
	"github.com/yashchhabra57/recallroute/internal/store"
	"github.com/yashchhabra57/recallroute/internal/store/memstore"
	"github.com/yashchhabra57/recallroute/internal/workflow"
)

type recorder struct {
	mu    sync.Mutex
	types []string
}

func (r *recorder) Publish(typ string, _ any) {
	r.mu.Lock()
	r.types = append(r.types, typ)
	r.mu.Unlock()
}

func (r *recorder) count(typ string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, t := range r.types {
		if t == typ {
			n++
		}
	}
	return n
}

var ctx = context.Background()

func loadFixtures(t *testing.T) ([]domain.Recall, []domain.Lot) {
	t.Helper()
	f, err := os.Open("../../testdata/openfda_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rs, err := feed.ParseOpenFDA(f)
	if err != nil {
		t.Fatal(err)
	}
	g, err := os.Open("../../testdata/inventory_sample.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	lots, rowErrs, err := inventory.ParseCSV(g)
	if err != nil || len(rowErrs) > 0 {
		t.Fatal(err, rowErrs)
	}
	return rs, lots
}

type key struct {
	Recall string
	SKU    string
	Lot    string
	Tier   domain.Tier
}

func alertKeys(t *testing.T, s *Service) []key {
	t.Helper()
	views, err := s.Alerts(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	out := make([]key, len(views))
	for i, v := range views {
		out[i] = key{v.RecallID, v.Lot.SKU, v.Lot.LotCode, v.Tier}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Recall != out[j].Recall {
			return out[i].Recall < out[j].Recall
		}
		return out[i].Lot < out[j].Lot
	})
	return out
}

var wantKeys = []key{
	{"F-1101-2026", "CUR-12", "", domain.TierProbable},
	{"F-1101-2026", "CUR-12", "2345A", domain.TierConfirmed},
	{"F-1102-2026", "CRK-8", "L5531", domain.TierConfirmed},
	{"F-1103-2026", "OAT-42", "P-7781", domain.TierReview},
	{"F-1104-2026", "WFS-8", "24-123", domain.TierConfirmed},
}

func TestRecallsThenLotsEqualsLotsThenRecalls(t *testing.T) {
	rs, lots := loadFixtures(t)

	a := New(memstore.New(), nil)
	if _, err := a.ImportLots(ctx, lots); err != nil {
		t.Fatal(err)
	}
	sum, err := a.IngestRecalls(ctx, rs)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Received != 5 || sum.Changed != 5 || len(sum.NewAlerts) != 5 {
		t.Errorf("summary = %+v", sum)
	}

	b := New(memstore.New(), nil)
	if _, err := b.IngestRecalls(ctx, rs); err != nil {
		t.Fatal(err)
	}
	imp, err := b.ImportLots(ctx, lots)
	if err != nil {
		t.Fatal(err)
	}
	if imp.Imported != 9 || len(imp.NewAlerts) != 5 {
		t.Errorf("import = %+v", imp)
	}

	ka, kb := alertKeys(t, a), alertKeys(t, b)
	for i := range wantKeys {
		if i >= len(ka) || ka[i] != wantKeys[i] {
			t.Fatalf("lots-first alerts = %+v\nwant %+v", ka, wantKeys)
		}
		if i >= len(kb) || kb[i] != wantKeys[i] {
			t.Fatalf("recalls-first alerts = %+v\nwant %+v", kb, wantKeys)
		}
	}
}

func TestReingestIsNoop(t *testing.T) {
	rs, lots := loadFixtures(t)
	rec := &recorder{}
	s := New(memstore.New(), rec)
	s.ImportLots(ctx, lots)
	s.IngestRecalls(ctx, rs)
	sum, err := s.IngestRecalls(ctx, rs)
	if err != nil || sum.Changed != 0 || len(sum.NewAlerts) != 0 {
		t.Errorf("re-ingest = %+v %v", sum, err)
	}
	if rec.count("alert.created") != 5 {
		t.Errorf("alert.created events = %d", rec.count("alert.created"))
	}
}

func TestTransitionUpdatesLotAndPublishes(t *testing.T) {
	rs, lots := loadFixtures(t)
	rec := &recorder{}
	s := New(memstore.New(), rec)
	s.ImportLots(ctx, lots)
	sum, _ := s.IngestRecalls(ctx, rs)

	var target domain.Alert
	for _, a := range sum.NewAlerts {
		if a.RecallID == "F-1102-2026" {
			target = a
		}
	}
	v, err := s.Transition(ctx, target.ID, 1, workflow.Command{Action: workflow.Acknowledge, Actor: "sam"})
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.Transition(ctx, target.ID, v.Version, workflow.Command{Action: workflow.Pull, Actor: "sam", Quantity: 60})
	if err != nil {
		t.Fatal(err)
	}
	if v.Lot.Quantity != 0 || v.PulledQty != 60 || len(v.Events) != 2 {
		t.Errorf("view = %+v", v)
	}
	if len(v.Actions) != 2 || v.Actions[0] != workflow.Pull || v.Actions[1] != workflow.Notify {
		t.Errorf("actions after pull = %v, want [pull notify]", v.Actions)
	}
	if rec.count("alert.updated") != 2 {
		t.Errorf("alert.updated = %d", rec.count("alert.updated"))
	}

	// Stale version is rejected.
	if _, err := s.Transition(ctx, target.ID, 1, workflow.Command{Action: workflow.Notify, Actor: "x", Note: "n"}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("stale err = %v", err)
	}
}

func TestPulledLotNoLongerMatchesNewRecalls(t *testing.T) {
	_, lots := loadFixtures(t)
	s := New(memstore.New(), nil)
	s.ImportLots(ctx, lots[:1]) // curds lot 2345A, qty 24
	r := domain.Recall{ID: "F-X", Status: "Ongoing", GTINs: []string{"00071234001019"}}
	r.Spec.All = true
	sum, _ := s.IngestRecalls(ctx, []domain.Recall{r})
	a := sum.NewAlerts[0]
	s.Transition(ctx, a.ID, 1, workflow.Command{Action: workflow.Acknowledge, Actor: "x"})
	s.Transition(ctx, a.ID, 2, workflow.Command{Action: workflow.Pull, Actor: "x", Quantity: 24})

	r2 := r
	r2.ID = "F-Y"
	sum2, _ := s.IngestRecalls(ctx, []domain.Recall{r2})
	if len(sum2.NewAlerts) != 0 {
		t.Errorf("empty lot matched again: %+v", sum2.NewAlerts)
	}
}
