// reservation_processor.go implements the background TTL worker for reservations
// (feature #131).
//
// The ReservationProcessor is intended to be called from the arena-worker binary on
// a regular schedule (e.g. every 30–60 seconds). It polls reservations whose TTL has
// elapsed using FOR UPDATE SKIP LOCKED (PostgreSQL job-queue pattern) and processes
// them atomically: releases the held inventory and marks the reservation as expired.
//
// Concurrency model:
//
//	GetExpiredReservations uses FOR UPDATE SKIP LOCKED so that multiple worker
//	instances never double-process the same reservation row.  The lock is
//	acquired in a short transaction (poll + commit) and released before the
//	per-item processing begins, so the row is available to the next poll cycle
//	as soon as the state is updated to 'expired'.
//
// Inventory release:
//
//	For each expired reservation, ReleaseCapacity is called to decrement
//	capacity_held on the inventory_ledger row by the reservation quantity.
//	A failure here is logged but does not abort the batch — the reservation is
//	still transitioned to 'expired'.
package httpserver

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/jackc/pgx/v5"
)

// ReservationProcessor handles background TTL expiration of reservations.
type ReservationProcessor struct {
	pool            PoolDB
	queries         *gen.Queries
	checkoutQueries *gen.Queries // optional; when non-nil, cascades expiry to checkout_sessions
	logger          *slog.Logger
}

// NewReservationProcessor constructs a ReservationProcessor.
// pool must be a *pgxpool.Pool (or any PoolDB implementation) for transaction support.
// queries must be constructed from the same pool for read-only queries outside a tx.
func NewReservationProcessor(pool PoolDB, queries *gen.Queries, logger *slog.Logger) *ReservationProcessor {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReservationProcessor{
		pool:    pool,
		queries: queries,
		logger:  logger,
	}
}

// WithCheckoutQueries sets an optional *gen.Queries instance for cascading
// checkout session expiry when a reservation expires (feature #132).
// When set, expireReservation will call ExpireCheckoutSession for each open
// checkout session linked to the expired reservation.
func (p *ReservationProcessor) WithCheckoutQueries(q *gen.Queries) *ReservationProcessor {
	p.checkoutQueries = q
	return p
}

// ProcessExpiredReservations polls up to limit expired reservations, releases their
// inventory, and marks each one as expired. Returns the number of reservations
// processed and any fatal error that prevented the poll itself (individual processing
// errors are logged but do not abort the batch).
//
// The FOR UPDATE SKIP LOCKED lock is held only during the poll transaction; it is
// released before each per-item processing transaction begins so other workers can
// pick up new items immediately.
func (p *ReservationProcessor) ProcessExpiredReservations(ctx context.Context, limit int32) (int, error) {
	// Step 1: Poll expired reservations inside a transaction to acquire
	// FOR UPDATE SKIP LOCKED row locks.
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("reservation_processor: begin poll tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)
	expired, err := q.GetExpiredReservations(ctx, limit)
	if err != nil {
		return 0, fmt.Errorf("reservation_processor: poll expired reservations: %w", err)
	}

	if len(expired) == 0 {
		// Nothing to do — commit immediately to release the FOR UPDATE locks.
		_ = tx.Commit(ctx)
		return 0, nil
	}

	// Commit the poll transaction. This releases the FOR UPDATE locks so other
	// workers can see the rows on their next poll. We process each item in its
	// own transaction below.
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("reservation_processor: commit poll tx: %w", err)
	}

	// Step 2: Process each reservation in its own transaction.
	processed := 0
	for _, r := range expired {
		if err := p.expireReservation(ctx, r); err != nil {
			p.logger.Error("reservation_processor: expire failed",
				slog.String("reservation_id", r.ID.String()),
				slog.String("session_id", r.SessionID.String()),
				slog.String("error", err.Error()),
			)
			// Non-fatal: continue to the next reservation.
			continue
		}
		processed++
	}

	return processed, nil
}

// expireReservation processes a single expired reservation within its own transaction:
//  1. Releases held capacity on the inventory ledger.
//  2. Transitions the reservation state to 'expired'.
func (p *ReservationProcessor) expireReservation(ctx context.Context, r gen.ReservationRow) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin expire tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	// Release held capacity — non-fatal if it fails (inventory may already be
	// inconsistent, but the reservation must still be marked expired).
	if _, err := q.ReleaseCapacity(ctx, r.SessionID, r.TierID, r.Quantity); err != nil {
		p.logger.Warn("reservation_processor: release capacity failed (non-fatal)",
			slog.String("reservation_id", r.ID.String()),
			slog.String("session_id", r.SessionID.String()),
			slog.String("error", err.Error()),
		)
		// Continue: still mark the reservation as expired even if capacity release fails.
	}

	// Transition the reservation to 'expired'.
	if _, err := q.UpdateReservationState(ctx, r.ID, "expired"); err != nil {
		return fmt.Errorf("update reservation state to expired: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit expire tx: %w", err)
	}

	p.logger.Info("reservation_processor: reservation expired",
		slog.String("reservation_id", r.ID.String()),
		slog.String("session_id", r.SessionID.String()),
		slog.Int("quantity", int(r.Quantity)),
	)

	// ── Cascade expiry to linked checkout sessions (feature #132) ────────────
	// This is best-effort: checkout sessions for an expired reservation should
	// also be expired, but a failure here must not cause the reservation itself
	// to be re-processed.
	if p.checkoutQueries != nil {
		sessions, err := p.checkoutQueries.ListCheckoutSessionsByReservation(ctx, r.ID)
		if err != nil {
			p.logger.Warn("reservation_processor: list checkout sessions failed (non-fatal)",
				slog.String("reservation_id", r.ID.String()),
				slog.String("error", err.Error()),
			)
		} else {
			for _, cs := range sessions {
				if _, err := p.checkoutQueries.ExpireCheckoutSession(ctx, cs.ID); err != nil {
					p.logger.Warn("reservation_processor: expire checkout session failed (non-fatal)",
						slog.String("checkout_session_id", cs.ID.String()),
						slog.String("reservation_id", r.ID.String()),
						slog.String("error", err.Error()),
					)
				} else {
					p.logger.Info("reservation_processor: checkout session expired",
						slog.String("checkout_session_id", cs.ID.String()),
						slog.String("reservation_id", r.ID.String()),
					)
				}
			}
		}
	}

	return nil
}
