// Package memstore is an in-memory store.Store guarded by one mutex.
// It is used by tests and by the server when no DATABASE_URL is set.
package memstore

import (
	"context"
	"sort"
	"sync"

	"github.com/yashchhabra57/recallroute/internal/domain"
	"github.com/yashchhabra57/recallroute/internal/store"
	"github.com/yashchhabra57/recallroute/internal/workflow"
)

type pair struct {
	recall string
	lot    int64
}

// Store implements store.Store.
type Store struct {
	mu       sync.Mutex
	recalls  map[string]domain.Recall
	hashes   map[string]string
	lots     map[int64]domain.Lot
	alerts   map[int64]domain.Alert
	byPair   map[pair]int64
	events   map[int64][]domain.Event
	nextLot  int64
	nextAl   int64
	nextEvt  int64
	recOrder []string
}

// New returns an empty store.
func New() *Store {
	return &Store{
		recalls: map[string]domain.Recall{},
		hashes:  map[string]string{},
		lots:    map[int64]domain.Lot{},
		alerts:  map[int64]domain.Alert{},
		byPair:  map[pair]int64{},
		events:  map[int64][]domain.Event{},
	}
}

var _ store.Store = (*Store)(nil)

func (s *Store) Close() {}

func (s *Store) UpsertRecalls(_ context.Context, rs []domain.Recall) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var changed []string
	for _, r := range rs {
		h := store.ContentHash(r)
		if old, ok := s.hashes[r.ID]; ok && old == h {
			continue
		}
		if _, ok := s.recalls[r.ID]; !ok {
			s.recOrder = append(s.recOrder, r.ID)
		}
		s.recalls[r.ID] = r
		s.hashes[r.ID] = h
		changed = append(changed, r.ID)
	}
	return changed, nil
}

func (s *Store) ListRecalls(context.Context) ([]domain.Recall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Recall, 0, len(s.recalls))
	for _, id := range s.recOrder {
		out = append(out, s.recalls[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ReportedOn.After(out[j].ReportedOn) })
	return out, nil
}

func (s *Store) GetRecall(_ context.Context, id string) (domain.Recall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recalls[id]
	if !ok {
		return domain.Recall{}, store.ErrNotFound
	}
	return r, nil
}

func (s *Store) AddLots(_ context.Context, lots []domain.Lot) ([]domain.Lot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Lot, len(lots))
	for i, l := range lots {
		s.nextLot++
		l.ID = s.nextLot
		s.lots[l.ID] = l
		out[i] = l
	}
	return out, nil
}

func (s *Store) ListLots(context.Context) ([]domain.Lot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Lot, 0, len(s.lots))
	for _, l := range s.lots {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) GetLot(_ context.Context, id int64) (domain.Lot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.lots[id]
	if !ok {
		return domain.Lot{}, store.ErrNotFound
	}
	return l, nil
}

func (s *Store) CreateAlerts(_ context.Context, as []domain.Alert) ([]domain.Alert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Alert
	for _, a := range as {
		k := pair{a.RecallID, a.LotID}
		if _, dup := s.byPair[k]; dup {
			continue
		}
		if _, ok := s.recalls[a.RecallID]; !ok {
			return nil, store.ErrNotFound
		}
		if _, ok := s.lots[a.LotID]; !ok {
			return nil, store.ErrNotFound
		}
		s.nextAl++
		a.ID = s.nextAl
		if a.Version == 0 {
			a.Version = 1
		}
		if a.State == "" {
			a.State = domain.StateNew
		}
		a.Reasons = append([]string(nil), a.Reasons...)
		s.alerts[a.ID] = a
		s.byPair[k] = a.ID
		out = append(out, a)
	}
	return out, nil
}

func (s *Store) ListAlerts(_ context.Context, state domain.State) ([]domain.Alert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.Alert{}
	for _, a := range s.alerts {
		if state == "" || a.State == state {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (s *Store) GetAlert(_ context.Context, id int64) (domain.Alert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.alerts[id]
	if !ok {
		return domain.Alert{}, store.ErrNotFound
	}
	return a, nil
}

func (s *Store) Events(_ context.Context, alertID int64) ([]domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.alerts[alertID]; !ok {
		return nil, store.ErrNotFound
	}
	return append([]domain.Event{}, s.events[alertID]...), nil
}

func (s *Store) Transition(_ context.Context, alertID int64, expectedVersion int, fn store.TransitionFunc) (workflow.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.alerts[alertID]
	if !ok {
		return workflow.Result{}, store.ErrNotFound
	}
	if a.Version != expectedVersion {
		return workflow.Result{}, store.ErrConflict
	}
	l, ok := s.lots[a.LotID]
	if !ok {
		return workflow.Result{}, store.ErrNotFound
	}
	res, err := fn(a, l)
	if err != nil {
		return workflow.Result{}, err
	}
	l.Quantity -= res.QuarantineQty
	s.lots[l.ID] = l
	s.alerts[alertID] = res.Alert
	s.nextEvt++
	res.Event.ID = s.nextEvt
	s.events[alertID] = append(s.events[alertID], res.Event)
	return res, nil
}
