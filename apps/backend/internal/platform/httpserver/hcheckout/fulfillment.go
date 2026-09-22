// fulfillment.go — what happens, atomically, once a checkout session has
// reached 'completed'.
//
// Two callers need EXACTLY the same four writes and must not drift apart:
//
//   - HandlePaymentIntentWebhook, when a provider confirms the money
//     (payment_intents.go Step 2b → 4);
//   - the public/widget checkout/start endpoint, when the cart total is 0 and
//     there is no money to wait for — a free order is complete the moment it
//     is confirmed, and answering the buyer a "pay here" URL for a zero
//     charge would be a dead end (hfeed, checkout.free_order path).
//
// Everything here is idempotent: the ticket-issuance worker job
// (IssueTicketsForCheckout, feature #366) detects an already-complete set,
// ordering.MarkPaid on an already-paid order is a no-op, and the
// reservation-conversion job silently skips an already-converted hold. A
// replay therefore costs a couple of no-op worker runs and nothing else.
package hcheckout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/convertjob"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/issuejob"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// insertWorkerJobSQL enqueues a durable worker job on the caller's
// transaction. Both jobs below are keyed purely by their JSON payload — there
// is no FK back to checkout_sessions/reservations — which is why a test
// fixture that deletes those parents must sweep worker_jobs FIRST (AGENTS.md).
const insertWorkerJobSQL = `
	INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
	VALUES ($1, $2::jsonb, $3, 'pending', now())`

// FulfillmentOutcome reports which of the non-fatal steps actually happened,
// so a caller can log honestly rather than assume.
type FulfillmentOutcome struct {
	// OrderMarkedPaid is false when the checkout session predates the order
	// aggregate (no orders row) or the order was already paid.
	OrderMarkedPaid bool
	// ConvertJobEnqueued is false when the reservation-conversion job could
	// not be enqueued — non-fatal, because the inline conversion and the TTL
	// worker both still run.
	ConvertJobEnqueued bool
}

// FulfillCompletedCheckoutTx performs the four writes that follow a checkout
// session reaching 'completed', on the CALLER's transaction:
//
//  1. enqueue checkout.issue_tickets — the durable ticket-issuance job;
//  2. mark the order aggregate paid (ordering.MarkPaid);
//  3. enqueue checkout.convert_reservation — held seats → sold.
//
// Only step 1 is fatal: without it the buyer has paid and will never receive
// a ticket, so its failure must roll the whole transaction back and let the
// provider redeliver. Steps 2 and 3 degrade — an order row lagging behind, or
// a conversion left to the inline call and the TTL worker, are both
// recoverable; refusing to issue tickets for money already captured is not.
//
// paidPayload is stamped onto the order_events 'paid' row (the payment intent
// and provider event for a real payment, or the reason a free order needed no
// payment at all).
//
// The caller MUST have completed the session first (CompleteCheckoutSession,
// or established that it is already 'completed'). This function does not
// check that — it is the shared tail, not the guard.
func FulfillCompletedCheckoutTx(
	ctx context.Context,
	tx pgx.Tx,
	txq *gen.Queries,
	checkoutSessionID uuid.UUID,
	paidPayload map[string]any,
) (FulfillmentOutcome, error) {
	var out FulfillmentOutcome

	// ── 1. Ticket issuance (fatal on failure) ────────────────────────────────
	jobPayload, err := json.Marshal(issuejob.Payload{CheckoutSessionID: checkoutSessionID.String()})
	if err != nil {
		return out, fmt.Errorf("marshal issue-tickets payload: %w", err)
	}
	if _, err := tx.Exec(ctx, insertWorkerJobSQL, issuejob.JobType, jobPayload, 5); err != nil {
		return out, fmt.Errorf("enqueue %s: %w", issuejob.JobType, err)
	}

	// ── 2. Order aggregate → paid (non-fatal) ────────────────────────────────
	// pgx.ErrNoRows means the session predates the order aggregate; those
	// sessions still issue tickets, they simply have no order to mark.
	ord, ordErr := txq.GetOrderByCheckoutSession(ctx, checkoutSessionID)
	switch {
	case errors.Is(ordErr, pgx.ErrNoRows):
	case ordErr != nil:
		return out, fmt.Errorf("order lookup: %w", ordErr)
	default:
		if _, paidErr := ordering.MarkPaid(ctx, txq, ordering.PaidInput{
			OrderID: ord.ID,
			OrgID:   ord.OrgID,
			Actor:   ordering.ActorSystem,
			Payload: paidPayload,
		}); paidErr != nil {
			return out, fmt.Errorf("mark order paid: %w", paidErr)
		}
		out.OrderMarkedPaid = true

		// ── 2b. Promo redemption (bookkeeping) ───────────────────────────────
		// The money has moved, so an over-limit code is recorded, not refused
		// — same stance as PAY_ORDER's payRedeemPromo. One row per order
		// (migration 0108), so a replayed webhook is a no-op here too.
		if err := recordPromoRedemptionTx(ctx, txq, ord); err != nil {
			return out, fmt.Errorf("promo redemption: %w", err)
		}
	}

	// ── 3. Reservation conversion job (non-fatal) ────────────────────────────
	cs, csErr := txq.GetCheckoutSessionByID(ctx, checkoutSessionID)
	if csErr != nil {
		return out, fmt.Errorf("checkout session lookup: %w", csErr)
	}
	convPayload, err := json.Marshal(convertjob.Payload{ReservationID: cs.ReservationID.String()})
	if err != nil {
		return out, fmt.Errorf("marshal convert payload: %w", err)
	}
	if _, err := tx.Exec(ctx, insertWorkerJobSQL, convertjob.JobType, convPayload, 5); err != nil {
		return out, fmt.Errorf("enqueue %s: %w", convertjob.JobType, err)
	}
	out.ConvertJobEnqueued = true

	return out, nil
}

// CompleteFreeCheckoutTx is the whole "there is nothing to pay" path: it
// completes the checkout session and runs the shared fulfilment tail, on the
// caller's transaction.
//
// A zero-total cart has no payment_intents row and no provider — the session
// records payment_provider 'none' so a later reader can tell a free order
// from one whose provider reference was lost.
//
// Returns pgx.ErrNoRows when the session is not in 'pricing_confirmed' (it
// was already completed, expired, or parked); the caller decides what that
// means for its own response.
func CompleteFreeCheckoutTx(
	ctx context.Context,
	tx pgx.Tx,
	txq *gen.Queries,
	checkoutSessionID uuid.UUID,
) (FulfillmentOutcome, error) {
	if _, err := txq.CompleteCheckoutSession(ctx, checkoutSessionID, "", freeCheckoutProvider); err != nil {
		return FulfillmentOutcome{}, err
	}
	return FulfillCompletedCheckoutTx(ctx, tx, txq, checkoutSessionID, map[string]any{
		"reason": "free_order",
	})
}

// freeCheckoutProvider is recorded as checkout_sessions.payment_provider for
// a zero-total order: no provider was ever involved.
const freeCheckoutProvider = "none"

// recordPromoRedemptionTx writes the redemption of a paid order's promo code
// on the caller's transaction. A promo deleted since the cart was priced is
// not an error — the discount was honoured, there is just no row to count it
// against. Nothing is written for an order without a code.
func recordPromoRedemptionTx(ctx context.Context, txq *gen.Queries, ord gen.OrderRow) error {
	if ord.PromoCodeID == nil {
		return nil
	}
	promo, err := txq.GetPromoCodeByIDForUpdate(ctx, *ord.PromoCodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("promo lookup %s: %w", ord.PromoCodeID.String(), err)
	}
	reservationID, orderID, channelID := ord.ReservationID, ord.ID, ord.ChannelID
	return txq.InsertPromoCodeRedemption(
		ctx, promo.ID, nil, &reservationID, ord.Discount, ord.Subtotal,
		&orderID, ord.CustomerID, &channelID,
	)
}
