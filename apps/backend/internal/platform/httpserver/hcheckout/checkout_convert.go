// checkout_convert.go — atomic reservation → 'converted' transition on checkout
// completion (feature #360, PR2-04 BLOCKER).
//
// # Problem
//
// Before this fix, SellReservationSeatsTx and ConfirmCapacity had zero
// production callers on the checkout-completion path. As a result:
//
//   - Paid seats stayed in state 'held' in session_seats.
//   - capacity_held was never moved to capacity_sold in inventory_ledger.
//   - The reservation remained in state 'draft' or 'active'.
//
// The TTL worker's GetExpiredReservations query selects reservations with
// state IN ('draft', 'active') AND expires_at < now(). When the TTL elapsed,
// it released the held seats back to 'available' and decremented capacity_held
// — even though valid tickets already existed for those seats. This allowed the
// same seats to be resold while valid tickets existed.
//
// # Fix
//
// convertReservationTx performs all three operations in one transaction:
//
//  1. sellReservationSeatsTx: held → sold for each session_seat row linked to
//     the reservation. Idempotent: already-sold seats are counted but not
//     re-transitioned, so webhook replays are safe.
//  2. ConfirmCapacity: moves reservation.Quantity units from capacity_held →
//     capacity_sold in inventory_ledger.
//  3. UpdateReservationState(id, "converted"): moves the reservation out of
//     the ('draft'|'active') set polled by GetExpiredReservations.
//
// The function is idempotent: if the reservation is already 'converted' it
// returns nil immediately so replayed webhooks and retried API calls are safe.
//
// # Call sites
//
// HandleCompleteCheckout (free branch):
//
//	after CompleteFreeCheckoutSession succeeds.
//
// HandleCompleteCheckout (paid branch):
//
//	after CompleteCheckoutSession succeeds.
//
// HandlePaymentWebhook (payment.succeeded event):
//
//	after issuing tickets, before returning 200 to the provider.
//
// All three call sites treat a convertReservationTx error as non-fatal (the
// checkout/payment state is already persisted), but log it at Error so alerts
// fire and the on-call engineer can inspect the affected reservation.
package hcheckout

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// convertReservationTx atomically transitions a reservation to 'converted'
// after checkout completion. It:
//
//  1. Sells all held seats (session_seats held → sold via sellReservationSeatsTx).
//  2. Confirms capacity (inventory_ledger capacity_held → capacity_sold via
//     ConfirmCapacity).
//  3. Sets reservation.state = 'converted' via UpdateReservationState.
//
// All three operations run in a single database transaction so no partial
// state is possible.
//
// Idempotent: returns nil immediately when the reservation is already in
// 'converted', 'expired', or 'cancelled' state (any terminal state other than
// the unprocessed hold states).
//
// Returns an error if any DB operation fails. Callers SHOULD log at Error
// and continue — the checkout/payment state is already persisted; a failed
// convert can be retried by re-triggering the completion path.
func (h *Handler) convertReservationTx(ctx context.Context, reservationID uuid.UUID) error {
	if h.reservationQueries == nil || h.inventoryQueries == nil || h.pool == nil {
		// Dependencies unavailable (e.g. unit-test server with nil queries).
		// Skip conversion silently — callers log at their own level.
		return nil
	}

	// Load the reservation to obtain session_id, tier_id, quantity, and current state.
	reservation, err := h.reservationQueries.GetReservationByID(ctx, reservationID)
	if err != nil {
		return fmt.Errorf("convertReservationTx: get reservation %s: %w", reservationID, err)
	}

	// Already in a terminal / converted state — pure idempotent replay.
	switch reservation.State {
	case "converted", "expired", "cancelled":
		return nil
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("convertReservationTx: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	// Step 1: Transition held seats → sold.
	// GA reservations have no reservation_seats rows — sellReservationSeatsTx
	// is a cheap no-op for them. Idempotent: already-sold seats are counted
	// separately so webhook replays do not fail.
	sold, alreadySold, err := sellReservationSeatsTx(ctx, q, reservation.SessionID, reservationID)
	if err != nil {
		return fmt.Errorf("convertReservationTx: sell seats for reservation %s: %w", reservationID, err)
	}
	if sold > 0 || alreadySold > 0 {
		slog.InfoContext(ctx, "convertReservationTx: seats transitioned",
			slog.String("reservation_id", reservationID.String()),
			slog.Int("sold", sold),
			slog.Int("already_sold", alreadySold),
		)
	}

	// Step 2: Move capacity from held → sold in inventory_ledger.
	// ConfirmCapacity(sessionID, tierID, amount) decrements capacity_held by
	// amount and increments capacity_sold by the same amount atomically.
	if _, err := q.ConfirmCapacity(ctx, reservation.SessionID, reservation.TierID, reservation.Quantity); err != nil {
		return fmt.Errorf("convertReservationTx: confirm capacity for reservation %s: %w", reservationID, err)
	}

	// Step 3: Mark the reservation as converted so GetExpiredReservations
	// (which filters on state IN ('draft','active')) can never select it again.
	if _, err := q.UpdateReservationState(ctx, reservationID, "converted"); err != nil {
		return fmt.Errorf("convertReservationTx: update state to converted for reservation %s: %w", reservationID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("convertReservationTx: commit tx for reservation %s: %w", reservationID, err)
	}

	slog.InfoContext(ctx, "convertReservationTx: reservation converted",
		slog.String("reservation_id", reservationID.String()),
		slog.String("session_id", reservation.SessionID.String()),
		slog.Int("quantity", int(reservation.Quantity)),
	)

	return nil
}
