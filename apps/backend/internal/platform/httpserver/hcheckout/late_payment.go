// late_payment.go — a payment that arrived after its order stopped waiting
// for it (spec 62 §5.4 п. 4, П-15).
//
// The payment webhook completes a checkout only from pricing_confirmed. When
// the provider confirms money for a checkout that is no longer there —
// abandoned by the buyer (П-2), or expired with its hold — the money has been
// taken all the same. Until П-15 every such payment was parked in
// manual_review in silence. Now, on the webhook's own transaction:
//
//	case (latepayment.Classify)  what arena does
//	buyer_cancelled              full refund, no tickets — the buyer said no
//	delayed_notice               the SAME places/categories at the SAME prices
//	                             and promo are taken again when every one of
//	                             them is still free and the order total equals
//	                             the money taken: the order is paid and the
//	                             tickets go out; otherwise a full refund —
//	                             never a partial delivery
//	after_window                 full refund
//
// A refund is a refunds row in provider_pending (settlement provider, no
// ticket, idempotency key late_payment:<intent>), which refund.sweep sends to
// the provider within minutes through the refund engine. A provider arena
// cannot refund through parks the order in manual_review instead (outcome
// operator_review) — and in every case a late_payments row records what
// happened and the buyer gets a letter (latepayment.JobTypeEmail).
//
// Every write here sits behind a SAVEPOINT: the payment intent's succeeded
// state, written earlier in the same transaction, must survive whatever
// fails here. A re-acquisition that loses a seat rolls back to its savepoint
// and falls through to the refund.
package hcheckout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/latepayment"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// lateReholdMinTTL is the shortest life of a re-acquired hold. The hold only
// has to last until the reservation conversion the same webhook queues turns
// it into a sale, but that is a worker job: a busy or restarting worker must
// not let the hold expire under a payment already taken.
const lateReholdMinTTL = 15 * time.Minute

// WithLatePaymentHold sets the life of a re-acquired hold to the widget
// payment window plus its grace, the expiry the normal path gives a hold
// (hfeed checkout/start), never below lateReholdMinTTL. Returns the receiver
// for chaining.
func (h *Handler) WithLatePaymentHold(window, grace time.Duration) *Handler {
	h.lateHoldTTL = window + grace
	return h
}

// lateReholdTTL is the life of a re-acquired hold for a configured
// window-plus-grace: max(lateReholdMinTTL, configured).
func lateReholdTTL(configured time.Duration) time.Duration {
	return max(lateReholdMinTTL, configured)
}

// errLateAmountMismatch: the re-priced order does not equal the money taken.
var errLateAmountMismatch = errors.New("hcheckout: the re-acquired order total differs from the payment")

// lateSettlement is what settleLatePaymentTx decided.
type lateSettlement struct {
	// handled is false when the checkout's state is not a late payment; the
	// caller then parks it as before.
	handled bool
	// completed: the places were taken again and the checkout completed on
	// this transaction; the caller runs the normal fulfilment tail.
	completed bool
	lateCase  string
	outcome   string
}

// settleLatePaymentTx handles a succeeded payment whose checkout csID could
// not be completed. cur is the checkout as re-read on tx.
func (h *Handler) settleLatePaymentTx(
	ctx context.Context,
	tx pgx.Tx,
	txq *gen.Queries,
	pi gen.PaymentIntentRow,
	cur gen.CheckoutSessionRow,
	paidAt *time.Time,
) (lateSettlement, error) {
	var ord *gen.OrderRow
	if o, err := txq.GetOrderByCheckoutSession(ctx, cur.ID); err == nil {
		ord = &o
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return lateSettlement{}, fmt.Errorf("late payment: load order: %w", err)
	}
	orderStatus, windowEnd := "", time.Time{}
	if ord != nil {
		orderStatus = ord.Status
		if ord.ExpiresAt != nil {
			windowEnd = *ord.ExpiresAt
		}
	}
	lateCase, ok := latepayment.Classify(cur.State, orderStatus, paidAt, windowEnd)
	if !ok {
		return lateSettlement{}, nil
	}
	out := lateSettlement{handled: true, lateCase: lateCase}
	reason := ""

	if lateCase == latepayment.CaseDelayedNotice && ord != nil {
		err := savepoint(ctx, tx, func(sp pgx.Tx) error {
			return h.reholdForLatePaymentTx(ctx, gen.New(sp), pi, cur, *ord)
		})
		if err == nil {
			out.completed, out.outcome = true, latepayment.OutcomeReacquired
		} else {
			reason = reholdRefusalReason(err)
			h.logger.Warn("late payment: the places could not be taken again; the payment is refunded",
				slog.String("payment_intent_id", pi.ID.String()),
				slog.String("checkout_session_id", cur.ID.String()),
				slog.String("reason", reason),
				slog.String("error", err.Error()))
		}
	}

	var refundID *uuid.UUID
	if !out.completed {
		var orderID *uuid.UUID
		source := ""
		if ord != nil {
			id := ord.ID
			orderID, source = &id, ord.Source
		}
		if refunds.RouteFor(registryModules{}, pi.Provider, source) == refunds.RouteArenaProvider {
			err := savepoint(ctx, tx, func(sp pgx.Tx) error {
				id, ierr := insertLateRefund(ctx, sp, pi, orderID, lateCase)
				refundID = id
				return ierr
			})
			if err == nil {
				out.outcome = latepayment.OutcomeRefundRequested
			} else {
				refundID = nil
				h.logger.Error("late payment: the refund could not be recorded; parked for the operator",
					slog.String("payment_intent_id", pi.ID.String()), slog.String("error", err.Error()))
			}
		}
		if out.outcome == "" {
			out.outcome = latepayment.OutcomeOperatorReview
			if err := h.parkCheckoutForManualReview(ctx, tx, cur.ID); err != nil {
				h.logger.Error("late payment: park for manual_review failed; payment state kept",
					slog.String("checkout_session_id", cur.ID.String()), slog.String("error", err.Error()))
			}
		}
	}

	if err := savepoint(ctx, tx, func(sp pgx.Tx) error {
		return recordLatePayment(ctx, sp, pi, cur, ord, out, reason, refundID)
	}); err != nil {
		h.logger.Error("late payment: recording it failed; the buyer letter is not queued",
			slog.String("payment_intent_id", pi.ID.String()), slog.String("error", err.Error()))
	}
	h.logger.Warn("late payment settled",
		slog.String("payment_intent_id", pi.ID.String()),
		slog.String("checkout_session_id", cur.ID.String()),
		slog.String("case", out.lateCase),
		slog.String("outcome", out.outcome))
	return out, nil
}

// reholdRefusalReason is the stored reason of a failed re-acquisition.
func reholdRefusalReason(err error) string {
	var sc *SeatConflictsError
	var ce *CapacityError
	switch {
	case errors.As(err, &sc):
		return "seats_taken"
	case errors.As(err, &ce):
		return "sold_out"
	case errors.Is(err, errLateAmountMismatch):
		return "amount_mismatch"
	}
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) && pgErr.SQLState() == pgUniqueViolation {
		return "another_open_order"
	}
	return "rehold_failed"
}

// reholdForLatePaymentTx takes the expired checkout's places again on a new
// reservation, at the prices the buyer paid, re-opens the order and
// completes the checkout. Lock order as every hold mutation: the sessions
// row (version bump) first, then inventory_ledger, then the places.
func (h *Handler) reholdForLatePaymentTx(ctx context.Context, q *gen.Queries, pi gen.PaymentIntentRow, cs gen.CheckoutSessionRow, ord gen.OrderRow) error {
	if cs.Subtotal == nil || cs.Total == nil || cs.Currency == nil {
		return errors.New("late payment: the checkout has no price snapshot")
	}
	if *cs.Total != pi.Amount {
		return errLateAmountMismatch
	}
	orig, err := q.GetReservationByID(ctx, cs.ReservationID)
	if err != nil {
		return fmt.Errorf("load reservation: %w", err)
	}
	seats, err := q.ListReservationSeats(ctx, orig.ID)
	if err != nil {
		return fmt.Errorf("list seats: %w", err)
	}
	gaLines, err := q.ListReservationGAItems(ctx, orig.ID)
	if err != nil {
		return fmt.Errorf("list GA lines: %w", err)
	}
	if len(seats) == 0 && len(gaLines) == 0 {
		// A legacy hold without lines cannot be re-priced line by line.
		return errors.New("late payment: a legacy hold without lines")
	}
	// The prices the buyer paid, per seat, from the order's own lines.
	items, err := q.ListOrderItemsByOrder(ctx, ord.ID)
	if err != nil {
		return fmt.Errorf("list order items: %w", err)
	}
	seatPrices := make(map[uuid.UUID]int64, len(items))
	for _, it := range items {
		if it.SessionSeatID != nil {
			seatPrices[*it.SessionSeatID] = it.UnitPrice
		}
	}

	version, err := q.IncrementSessionSeatStatusVersion(ctx, orig.SessionID)
	if err != nil {
		return fmt.Errorf("bump seat_status_version: %w", err)
	}
	var locked []gen.SessionSeatRow
	var seatQty int32
	if len(seats) > 0 {
		keys := make([]string, len(seats))
		for i, s := range seats {
			keys[i] = s.SeatKey
		}
		normalized, _, nerr := NormalizeSeatKeys(keys)
		if nerr != nil || len(normalized) == 0 {
			return errors.New("late payment: unreadable seat keys")
		}
		seatQty = int32(len(normalized)) //nolint:gosec // bounded by the hold's seats
		if locked, err = q.LockSessionSeatsForHold(ctx, orig.SessionID, normalized); err != nil {
			return fmt.Errorf("lock seats: %w", err)
		}
		if conflicts := SeatConflicts(normalized, locked); len(conflicts) > 0 {
			return &SeatConflictsError{Conflicts: conflicts}
		}
		if _, err := q.ReserveCapacity(ctx, orig.SessionID, nil, seatQty); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return &CapacityError{Requested: seatQty}
			}
			return fmt.Errorf("reserve seat capacity: %w", err)
		}
	}
	var gaQty int32
	for _, g := range gaLines {
		gaQty += g.Quantity
	}
	if gaQty > 0 {
		if _, err := q.ReserveCapacity(ctx, orig.SessionID, nil, gaQty); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return &CapacityError{Requested: gaQty}
			}
			return fmt.Errorf("reserve GA capacity: %w", err)
		}
	}
	var tierID *uuid.UUID
	if len(seats) == 0 && len(gaLines) == 1 {
		id := gaLines[0].TierID
		tierID = &id
	}
	expires := time.Now().UTC().Add(lateReholdTTL(h.lateHoldTTL))
	newRes, err := q.InsertReservation(ctx, orig.OrgID, orig.ChannelID, orig.SessionID, tierID, nil, seatQty+gaQty, expires)
	if err != nil {
		return fmt.Errorf("insert reservation: %w", err)
	}
	for _, s := range locked {
		if _, err := q.HoldSessionSeat(ctx, s.ID, newRes.ID, version); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return &SeatConflictsError{Conflicts: []map[string]string{{"seat_key": s.SeatKey, "status": "unavailable"}}}
			}
			return fmt.Errorf("hold seat %s: %w", s.SeatKey, err)
		}
		if err := q.InsertReservationSeat(ctx, newRes.ID, s.ID); err != nil {
			return fmt.Errorf("link seat %s: %w", s.SeatKey, err)
		}
	}
	for _, g := range gaLines {
		id := g.TierID
		if _, err := AllocateGAUnitsTx(ctx, q, orig.SessionID, newRes.ID, version,
			[]GAUnitLine{{TierID: &id, Quantity: g.Quantity}}); err != nil {
			return err
		}
		if err := q.InsertReservationGAItem(ctx, newRes.ID, g.TierID, g.Quantity, g.UnitPrice); err != nil {
			return fmt.Errorf("insert GA line: %w", err)
		}
	}

	if _, err := q.UpdateCheckoutSessionReservationAndReset(ctx, cs.ID, newRes.ID); err != nil {
		return fmt.Errorf("re-point checkout: %w", err)
	}
	if _, err := q.ConfirmCheckoutSession(ctx, cs.ID,
		*cs.Subtotal, derefI64(cs.Discount), derefI64(cs.PlatformFee), derefI64(cs.ProviderFee),
		derefI64(cs.Tax), *cs.Total, *cs.Currency, cs.PromoCodeID); err != nil {
		return fmt.Errorf("re-confirm checkout: %w", err)
	}
	if ord.Status == ordering.StatusExpired {
		reopened, err := q.ReopenExpiredOrderForLatePayment(ctx, ord.ID)
		if err != nil {
			return fmt.Errorf("re-open order: %w", err)
		}
		if !reopened {
			return errors.New("late payment: the order changed meanwhile")
		}
	} else if ord.Status != ordering.StatusPendingPayment {
		return fmt.Errorf("late payment: order in status %s", ord.Status)
	}
	updated, err := ordering.UpdateOrderFromCheckout(ctx, q, ordering.UpdateInput{
		OrderID: ord.ID,
		OrgID:   ord.OrgID,
		CreateInput: ordering.CreateInput{
			CheckoutSessionID: cs.ID,
			EventID:           ord.EventID,
			Actor:             ordering.ActorSystem,
			ExternalRef:       ord.ExternalRef,
			ChargePercentBP:   ord.ChargePercentBP,
			SeatUnitPrices:    seatPrices,
			BuyerName:         ord.BuyerName,
			BuyerEmail:        ord.BuyerEmail,
			BuyerPhone:        ord.BuyerPhone,
		},
	})
	if err != nil {
		return fmt.Errorf("re-price order: %w", err)
	}
	if updated.Order.Total != pi.Amount || !sameCurrency(updated.Order.Currency, pi.Currency) {
		return errLateAmountMismatch
	}
	if _, err := q.CompleteCheckoutSession(ctx, cs.ID, pi.ID.String(), pi.Provider); err != nil {
		return fmt.Errorf("complete checkout: %w", err)
	}
	return nil
}

func derefI64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func sameCurrency(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// lateRefundKey is the refund's idempotency key: one late refund per payment.
func lateRefundKey(paymentIntentID uuid.UUID) string {
	return "late_payment:" + paymentIntentID.String()
}

// insertLateRefund records the full refund of pi in provider_pending, once.
func insertLateRefund(ctx context.Context, tx pgx.Tx, pi gen.PaymentIntentRow, orderID *uuid.UUID, lateCase string) (*uuid.UUID, error) {
	reason := "Late payment: " + lateCase
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO refunds (payment_intent_id, org_id, order_id, amount, currency, reason,
		                     state, approved_at, settlement, provider, origin, cancel_ticket, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, 'provider_pending', now(), 'provider', $7, 'arena', false, $8)
		ON CONFLICT (org_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
		RETURNING id`,
		pi.ID, pi.OrgID, orderID, pi.Amount, pi.Currency, reason, pi.Provider, lateRefundKey(pi.ID)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id FROM refunds WHERE org_id = $1 AND idempotency_key = $2`,
			pi.OrgID, lateRefundKey(pi.ID)).Scan(&id)
	}
	if err != nil {
		return nil, fmt.Errorf("insert late refund: %w", err)
	}
	return &id, nil
}

// recordLatePayment writes the late_payments row and queues the buyer's
// letter, once per payment.
func recordLatePayment(ctx context.Context, tx pgx.Tx, pi gen.PaymentIntentRow, cs gen.CheckoutSessionRow,
	ord *gen.OrderRow, out lateSettlement, reason string, refundID *uuid.UUID) error {
	var orderID *uuid.UUID
	if ord != nil {
		id := ord.ID
		orderID = &id
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO late_payments (payment_intent_id, org_id, checkout_session_id, order_id,
		                           late_case, outcome, reason, refund_id, amount, currency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (payment_intent_id) DO NOTHING`,
		pi.ID, pi.OrgID, cs.ID, orderID, out.lateCase, out.outcome, reason, refundID, pi.Amount, pi.Currency)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	payload, _ := json.Marshal(latepayment.JobPayload{PaymentIntentID: pi.ID.String()})
	// A re-acquired order's ticket letter says everything; the buyer gets
	// the late-payment letter only when the money goes back.
	if out.outcome == latepayment.OutcomeReacquired {
		return nil
	}
	_, err = tx.Exec(ctx, insertWorkerJobSQL, latepayment.JobTypeEmail, payload, 5)
	return err
}
