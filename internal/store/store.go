// Package store defines persistence for recalls, inventory lots, alerts
// and their audit events. Two implementations exist: memstore (tests,
// demos) and pgstore (PostgreSQL). Both pass the same contract suite in
// storetest.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/yashchhabra57/recallroute/internal/domain"
	"github.com/yashchhabra57/recallroute/internal/workflow"
)

var (
	// ErrNotFound is returned when an ID does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict is returned when an alert changed since the caller read
	// it (optimistic concurrency check failed).
	ErrConflict = errors.New("store: alert was changed by someone else")
)

// TransitionFunc computes a workflow step from the locked alert and lot.
type TransitionFunc func(domain.Alert, domain.Lot) (workflow.Result, error)

// Store is the persistence boundary.
type Store interface {
	// UpsertRecalls inserts new recalls and updates ones whose content
	// changed. It returns the IDs that were inserted or changed, which
	// are the ones that need re-matching.
	UpsertRecalls(ctx context.Context, rs []domain.Recall) ([]string, error)
	ListRecalls(ctx context.Context) ([]domain.Recall, error)
	GetRecall(ctx context.Context, id string) (domain.Recall, error)

	// AddLots stores lots and returns them with IDs assigned.
	AddLots(ctx context.Context, lots []domain.Lot) ([]domain.Lot, error)
	ListLots(ctx context.Context) ([]domain.Lot, error)
	GetLot(ctx context.Context, id int64) (domain.Lot, error)

	// CreateAlerts inserts alerts, silently skipping any (recall, lot)
	// pair that already has one. It returns only the newly created alerts.
	CreateAlerts(ctx context.Context, as []domain.Alert) ([]domain.Alert, error)
	// ListAlerts returns alerts newest first; state "" means all.
	ListAlerts(ctx context.Context, state domain.State) ([]domain.Alert, error)
	GetAlert(ctx context.Context, id int64) (domain.Alert, error)
	Events(ctx context.Context, alertID int64) ([]domain.Event, error)

	// Transition locks the alert and its lot, runs fn, and if the alert's
	// version still equals expectedVersion, saves the new alert, removes
	// QuarantineQty from the lot and appends the event, all atomically.
	Transition(ctx context.Context, alertID int64, expectedVersion int, fn TransitionFunc) (workflow.Result, error)

	Close()
}

// ContentHash fingerprints the fields of a recall that affect matching,
// so re-ingesting an unchanged feed is a no-op.
func ContentHash(r domain.Recall) string {
	b, _ := json.Marshal(struct {
		Firm, Product, Status, CodeInfo, Classification string
		GTINs                                           []string
	}{r.Firm, r.Product, r.Status, r.CodeInfo, r.Classification, r.GTINs})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
