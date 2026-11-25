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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/txretry"
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

// Release implements Store: step 5 of section 5.2 in one transaction,
// retried whole on 40P01/40001. The locks follow the one order of a change of
// an existing hold: the reservation row, its checkouts, its pending orders,
// its open payment attempts, then (ExpireHoldForOrderTx) sessions, the seats
// and inventory_ledger.
//
// Under the reservation lock it refuses (released=false, the mark cleared,
// the hold tried again on the next pass) when the hold has an open payment
// attempt that is not in intentIDs: a page recorded after OpenIntents was
// read is open at the provider and must be closed first.
func (s *PGStore) Release(ctx context.Context, reservationID uuid.UUID, mark time.Time, intentIDs []uuid.UUID) (bool, error) {
	var released bool
	err := txretry.Do(ctx, txretry.DefaultAttempts, func() error {
		var err error
		released, err = s.releaseOnce(ctx, reservationID, mark, intentIDs)
		return err
	})
	return released, err
}

func (s *PGStore) releaseOnce(ctx context.Context, reservationID uuid.UUID, mark time.Time, intentIDs []uuid.UUID) (bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("holdclose: begin release: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 1. The reservation row.
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

	txq := gen.New(tx)
	// 2. Its checkouts, 3. its pending orders, 4. its open attempts.
	checkouts, err := txq.LockCheckoutSessionsOfReservation(ctx, reservationID)
	if err != nil {
		return false, fmt.Errorf("holdclose: lock checkouts: %w", err)
	}
	orderIDs, err := txq.LockPendingOrdersOfReservation(ctx, reservationID)
	if err != nil {
		return false, fmt.Errorf("holdclose: lock orders: %w", err)
	}
	open, err := txq.LockOpenAttemptsOfReservation(ctx, reservationID)
	if err != nil {
		return false, fmt.Errorf("holdclose: lock payment attempts: %w", err)
	}
	closed := make(map[uuid.UUID]struct{}, len(intentIDs))
	for _, id := range intentIDs {
		closed[id] = struct{}{}
	}
	for _, id := range open {
		if _, ok := closed[id]; !ok {
			// A page recorded after the close began: take the claim back,
			// the next pass closes it with the others.
			if err := txq.ClearHoldCloseMark(ctx, reservationID, mark); err != nil {
				return false, fmt.Errorf("holdclose: clear mark: %w", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return false, fmt.Errorf("holdclose: commit cleared mark: %w", err)
			}
			return false, nil
		}
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

	now := time.Now().UTC()
	for _, id := range orderIDs {
		if _, _, err := ordering.ExpireIfStillPending(ctx, txq, ordering.ExpireIfStillPendingInput{
			OrderID: id, Actor: ActorHoldClose, Now: now,
		}); err != nil {
			return false, err
		}
	}

	for _, c := range checkouts {
		if c.State == "completed" || c.State == "abandoned" || c.State == "expired" {
			continue
		}
		if _, err := txq.ExpireCheckoutSession(ctx, c.ID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("holdclose: expire checkout %s: %w", c.ID, err)
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
