package holdclose

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// ActorHoldClose is the order_events actor of an order this job expired.
const ActorHoldClose = "system:" + JobType

// PGStore is the production Store.
type PGStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewPGStore builds a PGStore on pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool, q: gen.New(pool)}
}

// ListCandidates implements Store.
func (s *PGStore) ListCandidates(ctx context.Context, providers []string, staleBefore time.Time, limit int32) ([]gen.HoldCloseCandidate, error) {
	return s.q.ListHoldCloseCandidates(ctx, providers, staleBefore, limit)
}

// Claim implements Store.
func (s *PGStore) Claim(ctx context.Context, reservationID uuid.UUID, staleBefore time.Time) (time.Time, time.Time, bool, error) {
	mark, expiresAt, err := s.q.ClaimHoldForClose(ctx, reservationID, staleBefore)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	return mark, expiresAt, true, nil
}

// ClearMark implements Store.
func (s *PGStore) ClearMark(ctx context.Context, reservationID uuid.UUID, mark time.Time) error {
	return s.q.ClearHoldCloseMark(ctx, reservationID, mark)
}

// OpenIntents implements Store.
func (s *PGStore) OpenIntents(ctx context.Context, reservationID uuid.UUID, providers []string) ([]gen.OpenPageIntent, error) {
	return s.q.ListOpenPageIntents(ctx, reservationID, providers)
}

// Release implements Store: step 5 of section 5.2 in one transaction. The
// reservation row is locked first; ExpireHoldForOrderTx then takes sessions,
// the seats and inventory_ledger in the order every hold mutation uses.
func (s *PGStore) Release(ctx context.Context, reservationID uuid.UUID, mark time.Time, intentIDs []uuid.UUID) (bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("holdclose: begin release: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var (
		state   string
		current *time.Time
	)
	if err := tx.QueryRow(ctx,
		`SELECT state, closing_started_at FROM reservations WHERE id = $1 FOR UPDATE`,
		reservationID).Scan(&state, &current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("holdclose: lock reservation: %w", err)
	}
	if (state != "draft" && state != "active") || current == nil || !current.Equal(mark) {
		// Converted, cancelled, or the claim was taken back: not ours.
		return false, tx.Commit(ctx)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE payment_intents
		SET    state           = 'failed',
		       failed_at       = now(),
		       updated_at      = now(),
		       failure_code    = $2,
		       failure_message = 'the payment page was closed when the seat hold expired'
		WHERE  id = ANY($1::uuid[])
		  AND  state IN ('created', 'requires_action')`,
		intentIDs, FailureCodeSessionExpired); err != nil {
		return false, fmt.Errorf("holdclose: fail payment attempts: %w", err)
	}

	txq := gen.New(tx)
	orderRows, err := tx.Query(ctx,
		`SELECT id FROM orders WHERE reservation_id = $1 AND status = 'pending_payment'`, reservationID)
	if err != nil {
		return false, fmt.Errorf("holdclose: list orders: %w", err)
	}
	orderIDs, err := pgx.CollectRows(orderRows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return false, fmt.Errorf("holdclose: read orders: %w", err)
	}
	now := time.Now().UTC()
	for _, id := range orderIDs {
		if _, _, err := ordering.ExpireIfStillPending(ctx, txq, ordering.ExpireIfStillPendingInput{
			OrderID: id, Actor: ActorHoldClose, Now: now,
		}); err != nil {
			return false, err
		}
	}

	csRows, err := tx.Query(ctx,
		`SELECT id FROM checkout_sessions
		 WHERE reservation_id = $1 AND state NOT IN ('completed', 'abandoned', 'expired')`, reservationID)
	if err != nil {
		return false, fmt.Errorf("holdclose: list checkouts: %w", err)
	}
	csIDs, err := pgx.CollectRows(csRows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return false, fmt.Errorf("holdclose: read checkouts: %w", err)
	}
	for _, id := range csIDs {
		if _, err := txq.ExpireCheckoutSession(ctx, id); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("holdclose: expire checkout %s: %w", id, err)
		}
	}

	if err := hcheckout.ExpireHoldForOrderTx(ctx, txq, reservationID); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("holdclose: commit release: %w", err)
	}
	return true, nil
}

var _ Store = (*PGStore)(nil)
