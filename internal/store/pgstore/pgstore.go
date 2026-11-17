// Package pgstore is the PostgreSQL implementation of store.Store, built
// on pgx v5 and a connection pool.
package pgstore

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yashchhabra57/recallroute/internal/domain"
	"github.com/yashchhabra57/recallroute/internal/store"
	"github.com/yashchhabra57/recallroute/internal/workflow"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store implements store.Store on PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

var _ store.Store = (*Store)(nil)

// Open connects, runs pending migrations and returns a ready store.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("pgstore: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: ping: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// migrationLockID is an arbitrary constant for pg_advisory_lock so two
// server replicas starting at once do not both run migrations.
const migrationLockID = 727_100_001

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("pgstore: migration lock: %w", err)
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockID) //nolint:errcheck

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("pgstore: create schema_migrations: %w", err)
	}

	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		v, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("pgstore: bad migration name %q", base)
		}
		var applied bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, v).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrationFS.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("pgstore: migration %s: %w", base, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, v); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// nullDate maps the zero time to SQL NULL.
func nullDate(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func fromNullDate(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *Store) UpsertRecalls(ctx context.Context, rs []domain.Recall) ([]string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var changed []string
	for _, r := range rs {
		spec, err := json.Marshal(r.Spec)
		if err != nil {
			return nil, err
		}
		gtins := r.GTINs
		if gtins == nil {
			gtins = []string{}
		}
		var id string
		err = tx.QueryRow(ctx, `
			INSERT INTO recalls (id, event_id, firm, product, reason, classification, status,
				distribution, code_info, gtins, gtins_inferred, spec, initiated_on, reported_on,
				source, content_hash)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
			ON CONFLICT (id) DO UPDATE SET
				event_id = EXCLUDED.event_id, firm = EXCLUDED.firm, product = EXCLUDED.product,
				reason = EXCLUDED.reason, classification = EXCLUDED.classification,
				status = EXCLUDED.status, distribution = EXCLUDED.distribution,
				code_info = EXCLUDED.code_info, gtins = EXCLUDED.gtins,
				gtins_inferred = EXCLUDED.gtins_inferred, spec = EXCLUDED.spec,
				initiated_on = EXCLUDED.initiated_on, reported_on = EXCLUDED.reported_on,
				source = EXCLUDED.source, content_hash = EXCLUDED.content_hash,
				ingested_at = now()
			WHERE recalls.content_hash <> EXCLUDED.content_hash
			RETURNING id`,
			r.ID, r.EventID, r.Firm, r.Product, r.Reason, r.Classification, r.Status,
			r.Distribution, r.CodeInfo, gtins, r.GTINsInferred, spec,
			nullDate(r.InitiatedOn), nullDate(r.ReportedOn), r.Source, store.ContentHash(r),
		).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // unchanged
		}
		if err != nil {
			return nil, fmt.Errorf("pgstore: upsert recall %s: %w", r.ID, err)
		}
		changed = append(changed, id)
	}
	return changed, tx.Commit(ctx)
}

const recallCols = `id, event_id, firm, product, reason, classification, status, distribution,
	code_info, gtins, gtins_inferred, spec, initiated_on, reported_on, source`

func scanRecall(row pgx.Row) (domain.Recall, error) {
	var r domain.Recall
	var spec []byte
	var init, rep *time.Time
	err := row.Scan(&r.ID, &r.EventID, &r.Firm, &r.Product, &r.Reason, &r.Classification,
		&r.Status, &r.Distribution, &r.CodeInfo, &r.GTINs, &r.GTINsInferred, &spec, &init, &rep, &r.Source)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(spec, &r.Spec); err != nil {
		return r, err
	}
	r.InitiatedOn, r.ReportedOn = fromNullDate(init), fromNullDate(rep)
	return r, nil
}

func (s *Store) ListRecalls(ctx context.Context) ([]domain.Recall, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+recallCols+` FROM recalls ORDER BY reported_on DESC NULLS LAST, seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Recall{}
	for rows.Next() {
		r, err := scanRecall(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRecall(ctx context.Context, id string) (domain.Recall, error) {
	r, err := scanRecall(s.pool.QueryRow(ctx, `SELECT `+recallCols+` FROM recalls WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, store.ErrNotFound
	}
	return r, err
}

func (s *Store) AddLots(ctx context.Context, lots []domain.Lot) ([]domain.Lot, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	out := make([]domain.Lot, len(lots))
	for i, l := range lots {
		var recv time.Time
		err := tx.QueryRow(ctx, `
			INSERT INTO lots (sku, name, brand, gtin, lot_code, best_by, quantity, location, received_on)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8, COALESCE($9::timestamptz, now()))
			RETURNING id, received_on`,
			l.SKU, l.Name, l.Brand, l.GTIN, l.LotCode, nullDate(l.BestBy), l.Quantity, l.Location, nullDate(l.ReceivedOn),
		).Scan(&l.ID, &recv)
		if err != nil {
			return nil, fmt.Errorf("pgstore: insert lot: %w", err)
		}
		l.ReceivedOn = recv.UTC()
		out[i] = l
	}
	return out, tx.Commit(ctx)
}

const lotCols = `id, sku, name, brand, gtin, lot_code, best_by, quantity, location, received_on`

func scanLot(row pgx.Row) (domain.Lot, error) {
	var l domain.Lot
	var bb *time.Time
	err := row.Scan(&l.ID, &l.SKU, &l.Name, &l.Brand, &l.GTIN, &l.LotCode, &bb, &l.Quantity, &l.Location, &l.ReceivedOn)
	l.BestBy = fromNullDate(bb)
	l.ReceivedOn = l.ReceivedOn.UTC()
	return l, err
}

func (s *Store) ListLots(ctx context.Context) ([]domain.Lot, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+lotCols+` FROM lots ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Lot{}
	for rows.Next() {
		l, err := scanLot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) GetLot(ctx context.Context, id int64) (domain.Lot, error) {
	l, err := scanLot(s.pool.QueryRow(ctx, `SELECT `+lotCols+` FROM lots WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return l, store.ErrNotFound
	}
	return l, err
}

func (s *Store) CreateAlerts(ctx context.Context, as []domain.Alert) ([]domain.Alert, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var out []domain.Alert
	for _, a := range as {
		if a.State == "" {
			a.State = domain.StateNew
		}
		if a.Version == 0 {
			a.Version = 1
		}
		reasons := a.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO alerts (recall_id, lot_id, tier, score, reasons, state, version, pulled_qty, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8, COALESCE($9::timestamptz, now()), COALESCE($9::timestamptz, now()))
			ON CONFLICT (recall_id, lot_id) DO NOTHING
			RETURNING id, created_at, updated_at`,
			a.RecallID, a.LotID, string(a.Tier), a.Score, reasons, string(a.State), a.Version, a.PulledQty, nullDate(a.CreatedAt),
		).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // duplicate
		}
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation
				return nil, fmt.Errorf("%w: %s", store.ErrNotFound, pgErr.Detail)
			}
			return nil, err
		}
		a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
		a.Reasons = reasons
		out = append(out, a)
	}
	return out, tx.Commit(ctx)
}

const alertCols = `id, recall_id, lot_id, tier, score, reasons, state, version, pulled_qty, created_at, updated_at`

func scanAlert(row pgx.Row) (domain.Alert, error) {
	var a domain.Alert
	var tier, state string
	err := row.Scan(&a.ID, &a.RecallID, &a.LotID, &tier, &a.Score, &a.Reasons, &state, &a.Version, &a.PulledQty, &a.CreatedAt, &a.UpdatedAt)
	a.Tier, a.State = domain.Tier(tier), domain.State(state)
	a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
	return a, err
}

func (s *Store) ListAlerts(ctx context.Context, state domain.State) ([]domain.Alert, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+alertCols+` FROM alerts WHERE ($1 = '' OR state = $1) ORDER BY id DESC`, string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Alert{}
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAlert(ctx context.Context, id int64) (domain.Alert, error) {
	a, err := scanAlert(s.pool.QueryRow(ctx, `SELECT `+alertCols+` FROM alerts WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, store.ErrNotFound
	}
	return a, err
}

func (s *Store) Events(ctx context.Context, alertID int64) ([]domain.Event, error) {
	if _, err := s.GetAlert(ctx, alertID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, alert_id, action, from_state, to_state, actor, note, quantity, recipients, at
		FROM alert_events WHERE alert_id = $1 ORDER BY id`, alertID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Event{}
	for rows.Next() {
		var e domain.Event
		var from, to string
		if err := rows.Scan(&e.ID, &e.AlertID, &e.Action, &from, &to, &e.Actor, &e.Note, &e.Quantity, &e.Recipients, &e.At); err != nil {
			return nil, err
		}
		e.From, e.To, e.At = domain.State(from), domain.State(to), e.At.UTC()
		if len(e.Recipients) == 0 {
			e.Recipients = nil
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Transition(ctx context.Context, alertID int64, expectedVersion int, fn store.TransitionFunc) (workflow.Result, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return workflow.Result{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Row locks serialize concurrent transitions on the same alert; the
	// version check then turns the losers into ErrConflict instead of
	// letting them overwrite the winner.
	a, err := scanAlert(tx.QueryRow(ctx, `SELECT `+alertCols+` FROM alerts WHERE id = $1 FOR UPDATE`, alertID))
	if errors.Is(err, pgx.ErrNoRows) {
		return workflow.Result{}, store.ErrNotFound
	}
	if err != nil {
		return workflow.Result{}, err
	}
	if a.Version != expectedVersion {
		return workflow.Result{}, store.ErrConflict
	}
	l, err := scanLot(tx.QueryRow(ctx, `SELECT `+lotCols+` FROM lots WHERE id = $1 FOR UPDATE`, a.LotID))
	if err != nil {
		return workflow.Result{}, err
	}

	res, err := fn(a, l)
	if err != nil {
		return workflow.Result{}, err
	}

	if _, err := tx.Exec(ctx, `UPDATE alerts SET state = $2, version = $3, pulled_qty = $4, updated_at = $5 WHERE id = $1`,
		a.ID, string(res.Alert.State), res.Alert.Version, res.Alert.PulledQty, res.Alert.UpdatedAt); err != nil {
		return workflow.Result{}, err
	}
	if res.QuarantineQty != 0 {
		if _, err := tx.Exec(ctx, `UPDATE lots SET quantity = quantity - $2 WHERE id = $1`, l.ID, res.QuarantineQty); err != nil {
			return workflow.Result{}, err
		}
	}
	recips := res.Event.Recipients
	if recips == nil {
		recips = []string{}
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO alert_events (alert_id, action, from_state, to_state, actor, note, quantity, recipients, at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
		a.ID, res.Event.Action, string(res.Event.From), string(res.Event.To), res.Event.Actor,
		res.Event.Note, res.Event.Quantity, recips, res.Event.At,
	).Scan(&res.Event.ID); err != nil {
		return workflow.Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return workflow.Result{}, err
	}
	return res, nil
}

// Reset truncates all tables. Tests only.
func (s *Store) Reset(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `TRUNCATE alert_events, alerts, lots, recalls RESTART IDENTITY CASCADE`)
	return err
}
