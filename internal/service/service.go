// Package service is the application layer: it runs the matcher when
// recalls or lots arrive, persists alerts, applies workflow commands and
// publishes events for live clients.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/yashchhabra57/recallroute/internal/domain"
	"github.com/yashchhabra57/recallroute/internal/match"
	"github.com/yashchhabra57/recallroute/internal/store"
	"github.com/yashchhabra57/recallroute/internal/workflow"
)

// Publisher receives domain events (the WebSocket hub in production).
type Publisher interface {
	Publish(typ string, data any)
}

type nopPublisher struct{}

func (nopPublisher) Publish(string, any) {}

// Service wires the pieces together.
type Service struct {
	store store.Store
	pub   Publisher
	now   func() time.Time
	log   *slog.Logger
}

// Option configures a Service.
type Option func(*Service)

// WithClock overrides time.Now (tests).
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.log = l } }

// New builds a Service. pub may be nil.
func New(st store.Store, pub Publisher, opts ...Option) *Service {
	if pub == nil {
		pub = nopPublisher{}
	}
	s := &Service{store: st, pub: pub, now: func() time.Time { return time.Now().UTC() }, log: slog.Default()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// IngestSummary reports what an ingest did.
type IngestSummary struct {
	Received  int            `json:"received"`
	Changed   int            `json:"changed"`
	NewAlerts []domain.Alert `json:"new_alerts"`
}

// IngestRecalls stores recalls and matches the new or changed ones
// against current inventory.
func (s *Service) IngestRecalls(ctx context.Context, rs []domain.Recall) (IngestSummary, error) {
	sum := IngestSummary{Received: len(rs), NewAlerts: []domain.Alert{}}
	changed, err := s.store.UpsertRecalls(ctx, rs)
	if err != nil {
		return sum, err
	}
	sum.Changed = len(changed)
	if len(changed) == 0 {
		return sum, nil
	}
	lots, err := s.store.ListLots(ctx)
	if err != nil {
		return sum, err
	}
	ix := match.NewIndex(lots)
	byID := make(map[string]domain.Recall, len(rs))
	for _, r := range rs {
		byID[r.ID] = r
	}
	var cands []match.Candidate
	for _, id := range changed {
		cands = append(cands, ix.Match(byID[id])...)
	}
	created, err := s.createAlerts(ctx, cands)
	if err != nil {
		return sum, err
	}
	sum.NewAlerts = created
	s.pub.Publish("recalls.ingested", map[string]int{"received": sum.Received, "changed": sum.Changed, "new_alerts": len(created)})
	s.log.Info("ingested recalls", "received", sum.Received, "changed", sum.Changed, "new_alerts", len(created))
	return sum, nil
}

// ImportSummary reports what a lot import did.
type ImportSummary struct {
	Imported  int            `json:"imported"`
	NewAlerts []domain.Alert `json:"new_alerts"`
}

// ImportLots stores lots and checks just those lots against every
// active recall, so stock received after a recall is still caught.
func (s *Service) ImportLots(ctx context.Context, lots []domain.Lot) (ImportSummary, error) {
	sum := ImportSummary{NewAlerts: []domain.Alert{}}
	saved, err := s.store.AddLots(ctx, lots)
	if err != nil {
		return sum, err
	}
	sum.Imported = len(saved)
	recalls, err := s.store.ListRecalls(ctx)
	if err != nil {
		return sum, err
	}
	ix := match.NewIndex(saved)
	var cands []match.Candidate
	for _, r := range recalls {
		cands = append(cands, ix.Match(r)...)
	}
	created, err := s.createAlerts(ctx, cands)
	if err != nil {
		return sum, err
	}
	sum.NewAlerts = created
	s.pub.Publish("lots.imported", map[string]int{"imported": sum.Imported, "new_alerts": len(created)})
	return sum, nil
}

func (s *Service) createAlerts(ctx context.Context, cands []match.Candidate) ([]domain.Alert, error) {
	if len(cands) == 0 {
		return []domain.Alert{}, nil
	}
	now := s.now()
	as := make([]domain.Alert, len(cands))
	for i, c := range cands {
		as[i] = domain.Alert{
			RecallID: c.RecallID, LotID: c.LotID, Tier: c.Tier, Score: c.Score,
			Reasons: c.Reasons, State: domain.StateNew, Version: 1,
			CreatedAt: now, UpdatedAt: now,
		}
	}
	created, err := s.store.CreateAlerts(ctx, as)
	if err != nil {
		return nil, err
	}
	for _, a := range created {
		s.pub.Publish("alert.created", a)
	}
	if created == nil {
		created = []domain.Alert{}
	}
	return created, nil
}

// AlertView is an alert with everything a volunteer needs to act on it.
type AlertView struct {
	domain.Alert
	Recall  domain.Recall     `json:"recall"`
	Lot     domain.Lot        `json:"lot"`
	Events  []domain.Event    `json:"events"`
	Actions []workflow.Action `json:"actions"`
}

// Alert loads one alert with its recall, lot and history.
func (s *Service) Alert(ctx context.Context, id int64) (AlertView, error) {
	a, err := s.store.GetAlert(ctx, id)
	if err != nil {
		return AlertView{}, err
	}
	return s.view(ctx, a)
}

func (s *Service) view(ctx context.Context, a domain.Alert) (AlertView, error) {
	r, err := s.store.GetRecall(ctx, a.RecallID)
	if err != nil {
		return AlertView{}, fmt.Errorf("load recall %s: %w", a.RecallID, err)
	}
	l, err := s.store.GetLot(ctx, a.LotID)
	if err != nil {
		return AlertView{}, fmt.Errorf("load lot %d: %w", a.LotID, err)
	}
	evs, err := s.store.Events(ctx, a.ID)
	if err != nil {
		return AlertView{}, err
	}
	acts := workflow.Allowed(a.State)
	if acts == nil {
		acts = []workflow.Action{}
	}
	return AlertView{Alert: a, Recall: r, Lot: l, Events: evs, Actions: acts}, nil
}

// Alerts lists alerts in a state ("" = all) with their recall and lot.
func (s *Service) Alerts(ctx context.Context, state domain.State) ([]AlertView, error) {
	as, err := s.store.ListAlerts(ctx, state)
	if err != nil {
		return nil, err
	}
	out := make([]AlertView, 0, len(as))
	for _, a := range as {
		v, err := s.view(ctx, a)
		if err != nil {
			return nil, err
		}
		v.Events = nil // list view: keep payload small
		out = append(out, v)
	}
	return out, nil
}

// Transition applies a workflow command if the alert is still at
// expectedVersion.
func (s *Service) Transition(ctx context.Context, id int64, expectedVersion int, cmd workflow.Command) (AlertView, error) {
	now := s.now()
	res, err := s.store.Transition(ctx, id, expectedVersion, func(a domain.Alert, l domain.Lot) (workflow.Result, error) {
		return workflow.Apply(a, l, cmd, now)
	})
	if err != nil {
		return AlertView{}, err
	}
	v, err := s.view(ctx, res.Alert)
	if err != nil {
		return AlertView{}, err
	}
	s.pub.Publish("alert.updated", map[string]any{"alert": res.Alert, "event": res.Event})
	return v, nil
}

// Recalls lists stored recalls.
func (s *Service) Recalls(ctx context.Context) ([]domain.Recall, error) {
	return s.store.ListRecalls(ctx)
}

// Lots lists inventory.
func (s *Service) Lots(ctx context.Context) ([]domain.Lot, error) { return s.store.ListLots(ctx) }
