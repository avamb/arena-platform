// buyer_cancel.go — the buyer cancels an unpaid order (spec 62 §5.4 п. 3,
// П-2): "Back" on the provider's page, or the site's own cancel button.
//
// One transaction, retried whole on a deadlock like every hold mutation:
//
//  1. the checkout row is locked FIRST, then the order — the same order the
//     payment webhook takes them in (CompleteCheckoutSession, then MarkPaid),
//     so a cancel racing a payment waits instead of deadlocking;
//  2. the state is decided from the rows read under those locks: a checkout
//     already completed (or an order already paid) is NOT cancelled — the
//     buyer sees "thank you"; an expired one is left as it is (its places
//     went back with the sweep); an abandoned one answers as cancelled again
//     (idempotent);
//  3. the hold goes back through releaseHoldTx — the one release path shared
//     with ReleaseHold, which keeps the lock order sessions → inventory_ledger
//     and the seat/GA/legacy capacity branches in one place;
//  4. the checkout becomes 'abandoned', the order 'cancelled'
//     (order_events.cancelled reason buyer_cancelled), and an audit row is
//     written on the same transaction.
//
// The payment intent is deliberately LEFT OPEN: if the provider still
// confirms a payment after this (the buyer paid on another tab, the provider
// did not answer the pre-check), that event must reach the webhook state
// machine, which turns it into a full refund (late_payment.go). A terminal
// intent would swallow it.
//
// Asking the provider whether the order was paid is the caller's job, done
// BEFORE this function (a network call never runs inside the transaction).
package hcheckout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// BuyerCancelReason is the order_events.cancelled reason of a buyer's cancel.
const BuyerCancelReason = "buyer_cancelled"

// BuyerCancelOutcome is what CancelUnpaidCheckout did.
type BuyerCancelOutcome string

// Outcomes of CancelUnpaidCheckout.
const (
	// BuyerCancelCancelled: the hold went back, the order is cancelled.
	BuyerCancelCancelled BuyerCancelOutcome = "cancelled"
	// BuyerCancelAlreadyCancelled: a repeat; nothing was written.
	BuyerCancelAlreadyCancelled BuyerCancelOutcome = "already_cancelled"
	// BuyerCancelPaid: the order was paid first; nothing was cancelled.
	BuyerCancelPaid BuyerCancelOutcome = "paid"
	// BuyerCancelExpired: the order had already expired; nothing to do.
	BuyerCancelExpired BuyerCancelOutcome = "expired"
	// BuyerCancelUnderReview: an operator holds the order (manual_review).
	BuyerCancelUnderReview BuyerCancelOutcome = "under_review"
)

// BuyerCancelInput names the checkout and who asked.
type BuyerCancelInput struct {
	CheckoutSessionID uuid.UUID
	// ActorLabel goes to the audit metadata (never actor_id, which is a
	// uuid column): e.g. "buyer:checkout_token".
	ActorLabel string
	// Via is the client channel, for the audit trail.
	Via string
	IP  string
	Now time.Time
}

// ErrCheckoutNotFound: no checkout session with that id.
var ErrCheckoutNotFound = errors.New("hcheckout: checkout session not found")

// BuyerCancelResult is the outcome and the order it concerned.
type BuyerCancelResult struct {
	Outcome BuyerCancelOutcome
	OrderID *uuid.UUID
}

// CancelUnpaidCheckout runs the buyer's cancel. auditW may be nil (tests);
// a configured writer's failure fails the cancel.
func CancelUnpaidCheckout(ctx context.Context, pool TxStarter, q *gen.Queries, auditW audit.Writer, in BuyerCancelInput) (BuyerCancelResult, error) {
	if pool == nil || q == nil {
		return BuyerCancelResult{}, errors.New("hcheckout: CancelUnpaidCheckout requires a pool and queries")
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	var out BuyerCancelResult
	err := retryOnSerializationFailure(ctx, holdMutationRetryAttempts, func() error {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("hcheckout: begin buyer cancel tx: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		res, write, err := cancelUnpaidCheckoutTx(ctx, tx, q.WithTx(tx), auditW, in)
		if err != nil {
			return err
		}
		if write {
			if err := tx.Commit(ctx); err != nil {
				return fmt.Errorf("hcheckout: commit buyer cancel: %w", err)
			}
		}
		out = res
		return nil
	})
	return out, err
}

// cancelUnpaidCheckoutTx is the transaction body; write reports whether
// anything must be committed.
func cancelUnpaidCheckoutTx(ctx context.Context, tx pgx.Tx, txq *gen.Queries, auditW audit.Writer, in BuyerCancelInput) (BuyerCancelResult, bool, error) {
	var state string
	var reservationID, orgID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT state, reservation_id, org_id FROM checkout_sessions WHERE id = $1 FOR UPDATE`,
		in.CheckoutSessionID).Scan(&state, &reservationID, &orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BuyerCancelResult{}, false, ErrCheckoutNotFound
		}
		return BuyerCancelResult{}, false, fmt.Errorf("hcheckout: lock checkout: %w", err)
	}

	var res BuyerCancelResult
	var ord *gen.OrderRow
	if o, err := txq.GetOrderByCheckoutSession(ctx, in.CheckoutSessionID); err == nil {
		locked, lerr := txq.LockOrderForUpdate(ctx, o.ID, o.OrgID)
		if lerr != nil {
			return BuyerCancelResult{}, false, fmt.Errorf("hcheckout: lock order: %w", lerr)
		}
		ord = &locked
		id := locked.ID
		res.OrderID = &id
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return BuyerCancelResult{}, false, fmt.Errorf("hcheckout: load order: %w", err)
	}

	switch {
	case state == "completed" || (ord != nil && ord.Status == ordering.StatusPaid):
		res.Outcome = BuyerCancelPaid
		return res, false, nil
	case state == "abandoned" || (ord != nil && ord.Status == ordering.StatusCancelled):
		res.Outcome = BuyerCancelAlreadyCancelled
		return res, false, nil
	case state == "expired" || (ord != nil && ord.Status == ordering.StatusExpired):
		res.Outcome = BuyerCancelExpired
		return res, false, nil
	case state == "manual_review" || (ord != nil && ord.Status != ordering.StatusPendingPayment):
		res.Outcome = BuyerCancelUnderReview
		return res, false, nil
	}

	released := true
	if _, err := releaseHoldTx(ctx, txq, reservationID); err != nil {
		var nr *NotReleasableError
		switch {
		case errors.As(err, &nr) && nr.State == "converted":
			// Converted means sold: the payment won the race.
			res.Outcome = BuyerCancelPaid
			return res, false, nil
		case errors.As(err, &nr), errors.Is(err, ErrHoldNotFound):
			// Already expired or released by a sweep: there is nothing to
			// give back, the records are still closed below.
			released = false
		default:
			return BuyerCancelResult{}, false, err
		}
	}
	if _, err := txq.AbandonCheckoutSession(ctx, in.CheckoutSessionID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return BuyerCancelResult{}, false, fmt.Errorf("hcheckout: abandon checkout: %w", err)
	}
	if ord != nil {
		if _, err := ordering.Cancel(ctx, txq, ordering.CancelInput{
			OrderID: ord.ID, OrgID: ord.OrgID, Actor: ordering.ActorSystem,
			Reason: BuyerCancelReason, Now: in.Now,
		}); err != nil {
			return BuyerCancelResult{}, false, fmt.Errorf("hcheckout: cancel order: %w", err)
		}
	}
	if auditW != nil {
		meta := map[string]any{
			"checkout_session_id": in.CheckoutSessionID.String(),
			"reservation_id":      reservationID.String(),
			"hold_released":       released,
			"actor_label":         in.ActorLabel,
			"via":                 in.Via,
			"org_id":              orgID.String(),
		}
		if ord != nil {
			meta["order_id"] = ord.ID.String()
			meta["order_number"] = ord.SystemID
		}
		if err := auditW.WriteTx(ctx, tx, audit.Event{
			OccurredAt: in.Now, ActorType: "anonymous",
			Action: "v1.checkout.buyer_cancel", ResourceType: "checkout_session",
			ResourceID: in.CheckoutSessionID.String(), IP: in.IP, Metadata: meta,
		}); err != nil {
			return BuyerCancelResult{}, false, fmt.Errorf("hcheckout: audit buyer cancel: %w", err)
		}
	}
	res.Outcome = BuyerCancelCancelled
	return res, true, nil
}
