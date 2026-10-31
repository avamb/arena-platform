package shop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// FulfillPaidOrderTx is called by the payment webhook, inside ITS
// transaction, once a payment intent without a checkout session succeeded.
// handled=false means the payment is not a shop payment (the caller goes on
// as before). For a shop order it marks the order paid, turns the hold into
// a sale and writes v1.shop.order.paid, all on tx. It is idempotent: an order
// already paid (or shipped, refunded) is left alone, and so is one parked
// because its goods were gone (late_payment_no_stock: the money is already
// recorded and the operator decides, see Resolve). An order parked because its
// PAYMENT was not confirmed (payment_unconfirmed, SB-04) is the one parked
// order this call moves: the payment has now succeeded, so it goes through the
// ordinary checks below and is paid, or parked again as a late payment whose
// goods are gone.
//
// Every payment, on time or late, locks the order's variants in id order and
// reads their free stock again, leaving the order's own hold out: when every
// tracked line is still there the order is paid (a late one with
// metadata.paid_after naming the status it had); when one is not, the order
// is parked in manual_review with the payment recorded, nothing taken off the
// shelf, and v1.shop.order.manual_review written for the operator. Whether
// the hold was still live is decided in SQL on the transaction's clock, the
// clock the held quantities use, never on the Go clock.
func (s *Service) FulfillPaidOrderTx(ctx context.Context, tx pgx.Tx, paymentIntentID uuid.UUID, meta map[string]any) (handled bool, err error) {
	q := gen.New(tx)
	o, err := q.GetShopOrderByPaymentIntentForUpdate(ctx, paymentIntentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("shop: load order of payment %s: %w", paymentIntentID, err)
	}
	switch o.Status {
	case StatusPaid, StatusShipped, StatusRefunded, StatusPartiallyRefunded:
		return true, nil
	case StatusManualReview:
		if !reviewReasonIs(o, ReviewPaymentUnconfirmed) {
			return true, nil
		}
	}
	if meta == nil {
		meta = map[string]any{}
	}
	if err := q.LockShopOrderVariants(ctx, o.ID); err != nil {
		return true, fmt.Errorf("shop: lock the paid order's goods: %w", err)
	}
	holdLive, err := q.ShopOrderHoldLive(ctx, o.ID)
	if err != nil {
		return true, fmt.Errorf("shop: read the order's hold: %w", err)
	}
	if !holdLive {
		after := o.Status
		switch after {
		case StatusPendingPayment:
			after = "hold_expired"
		case StatusManualReview:
			after = "payment_review"
		}
		meta["paid_after"] = after
	}
	lines, err := q.ShopOrderShortages(ctx, o.ID)
	if err != nil {
		return true, fmt.Errorf("shop: read the paid order's goods: %w", err)
	}
	if short := shortLines(lines); len(short) > 0 {
		s.logger.Warn("shop: payment for goods that are gone; order parked for review",
			"order_id", o.ID.String(), "status", o.Status, "hold_live", holdLive)
		return true, s.parkForReviewTx(ctx, tx, q, o, meta, short)
	}
	if !holdLive {
		s.logger.Warn("shop: payment arrived after the order's hold ended; the goods are still there",
			"order_id", o.ID.String(), "status", o.Status)
	}
	if _, err := s.markPaidTx(ctx, tx, q, o, meta); err != nil {
		return true, err
	}
	return true, nil
}

// shortLines are the lines a late order cannot be served from: the stock
// left after every live hold is below the quantity ordered.
func shortLines(lines []gen.ShopOrderLine) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if free := l.Stock - l.Held; free < l.Qty {
			out = append(out, map[string]any{
				"variant_id": l.VariantID.String(), "ordered": l.Qty, "available": max(free, 0),
			})
		}
	}
	return out
}

// parkForReviewTx records a payment whose goods are gone: status manual_review
// with review_reason late_payment_no_stock and paid_at (the money was taken),
// the short lines in metadata, the hold's release journaled when the order was
// still pending, and the outbox event.
func (s *Service) parkForReviewTx(ctx context.Context, tx pgx.Tx, q *gen.Queries, o gen.ShopOrderRow, meta map[string]any, short []map[string]any) error {
	meta["short_items"] = short
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	parked, err := q.SetShopOrderReview(ctx, o.ID, ReviewLatePaymentNoStock, true, raw)
	if err != nil {
		return fmt.Errorf("shop: park order for review: %w", err)
	}
	if o.Status == StatusPendingPayment {
		if err := q.JournalShopOrderHold(ctx, o.ID, o.OrgID, MoveRelease); err != nil {
			return fmt.Errorf("shop: journal release: %w", err)
		}
	}
	return s.appendEvent(ctx, tx, EventOrderManualReview, parked, map[string]any{
		"reason": ReviewLatePaymentNoStock, "short_items": short,
	})
}

// markPaidTx is the one "this order is paid" write: status, stock, journal,
// outbox event.
func (s *Service) markPaidTx(ctx context.Context, tx pgx.Tx, q *gen.Queries, o gen.ShopOrderRow, meta map[string]any) (gen.ShopOrderRow, error) {
	var raw json.RawMessage
	if len(meta) > 0 {
		b, err := json.Marshal(meta)
		if err != nil {
			return gen.ShopOrderRow{}, err
		}
		raw = b
	}
	paid, err := q.SetShopOrderStatus(ctx, o.ID, StatusPaid, nil, raw)
	if err != nil {
		return gen.ShopOrderRow{}, fmt.Errorf("shop: mark order paid: %w", err)
	}
	if err := s.applySaleTx(ctx, q, o); err != nil {
		return gen.ShopOrderRow{}, err
	}
	if err := s.appendEvent(ctx, tx, EventOrderPaid, paid, map[string]any{
		"subtotal": paid.Subtotal, "shipping_fee": paid.ShippingFee,
	}); err != nil {
		return gen.ShopOrderRow{}, err
	}
	s.enqueueOrderEmailTx(ctx, tx, EmailReceived, paid)
	return paid, nil
}

// applySaleTx takes the order's goods off the shelf, locking the variant
// rows in id order first.
func (s *Service) applySaleTx(ctx context.Context, q *gen.Queries, o gen.ShopOrderRow) error {
	if err := q.LockShopOrderVariants(ctx, o.ID); err != nil {
		return fmt.Errorf("shop: lock the paid goods: %w", err)
	}
	if err := q.ApplyShopOrderSale(ctx, o.ID, o.OrgID); err != nil {
		return fmt.Errorf("shop: take the paid goods off the shelf: %w", err)
	}
	return nil
}

// The two causes of a manual_review order (shop_orders.review_reason, migration
// 0180). They need different people to do different things: a late payment
// whose goods are gone has its money (ship late, or refund), an unconfirmed
// payment may have none (settle the payment first).
const (
	// ReviewLatePaymentNoStock: the money was taken, the hold had ended and the
	// goods are gone. paid_at is set.
	ReviewLatePaymentNoStock = "late_payment_no_stock"
	// ReviewPaymentUnconfirmed: the provider's report is not trusted (SA-09),
	// the money may not have arrived. paid_at stays empty.
	ReviewPaymentUnconfirmed = "payment_unconfirmed"
)

// reviewReasonIs: the order is parked in manual_review for that reason.
func reviewReasonIs(o gen.ShopOrderRow, reason string) bool {
	return o.Status == StatusManualReview && o.ReviewReason != nil && *o.ReviewReason == reason
}

// MarkPaymentReviewTx is called by the payment webhook, inside its
// transaction, when a shop order's payment intent was parked in manual_review
// (the paid amount differs from the intent's, the provider's payment status
// is not one arena trusts). The money may or may not have arrived, so the
// order must not be left pending, where the expiry sweep would free its goods
// and expire it under a buyer who may have paid: it is parked in
// manual_review like a late payment whose goods are gone (the hold is
// released, v1.shop.order.manual_review is written, the order shows under
// the manual-review tab) and no letter goes out. Its paid_at stays empty: nothing is known to be paid
// (SB-02). When the payment then succeeds (the provider's next webhook, or the
// operator's transition of the payment intent) FulfillPaidOrderTx pays the
// order; when it fails, MarkPaymentFailedTx ends it. Until then the operator
// can ship it only after the payment succeeded (Ship).
// handled=false: not a shop payment. An order that is not pending any more
// is left alone.
func (s *Service) MarkPaymentReviewTx(ctx context.Context, tx pgx.Tx, paymentIntentID uuid.UUID, meta map[string]any) (handled bool, err error) {
	q := gen.New(tx)
	o, err := q.GetShopOrderByPaymentIntentForUpdate(ctx, paymentIntentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("shop: load order of payment %s: %w", paymentIntentID, err)
	}
	if o.Status != StatusPendingPayment {
		return true, nil
	}
	if meta == nil {
		meta = map[string]any{}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return true, err
	}
	if err := q.LockShopOrderVariants(ctx, o.ID); err != nil {
		return true, fmt.Errorf("shop: lock the parked order's goods: %w", err)
	}
	parked, err := q.SetShopOrderReview(ctx, o.ID, ReviewPaymentUnconfirmed, false, raw)
	if err != nil {
		return true, fmt.Errorf("shop: park order for payment review: %w", err)
	}
	if err := q.JournalShopOrderHold(ctx, o.ID, o.OrgID, MoveRelease); err != nil {
		return true, fmt.Errorf("shop: journal release: %w", err)
	}
	s.logger.Warn("shop: payment parked in manual_review; order parked with it", "order_id", o.ID.String())
	return true, s.appendEvent(ctx, tx, EventOrderManualReview, parked, map[string]any{
		"reason": ReviewPaymentUnconfirmed,
	})
}

// MarkPaymentFailedTx is called by the payment webhook, inside its
// transaction, when a shop order's payment failed for good (the hosted page
// expired unpaid, an asynchronous payment failed). A pending order expires
// and its goods go back on sale. handled=false: not a shop payment.
func (s *Service) MarkPaymentFailedTx(ctx context.Context, tx pgx.Tx, paymentIntentID uuid.UUID) (handled bool, err error) {
	q := gen.New(tx)
	o, err := q.GetShopOrderByPaymentIntentForUpdate(ctx, paymentIntentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("shop: load order of payment %s: %w", paymentIntentID, err)
	}
	if reviewReasonIs(o, ReviewPaymentUnconfirmed) {
		// The payment was parked for review and the operator then decided it
		// failed: nothing was paid and the goods went back on sale when the
		// order was parked, so the order simply ends.
		if _, err := q.SetShopOrderStatus(ctx, o.ID, StatusExpired, nil, []byte(`{"expired_by":"payment_failed"}`)); err != nil {
			return true, fmt.Errorf("shop: expire order: %w", err)
		}
		return true, nil
	}
	if o.Status != StatusPendingPayment {
		return true, nil
	}
	if _, err := q.SetShopOrderStatus(ctx, o.ID, StatusExpired, nil, []byte(`{"expired_by":"payment_failed"}`)); err != nil {
		return true, fmt.Errorf("shop: expire order: %w", err)
	}
	if err := q.LockShopOrderVariants(ctx, o.ID); err != nil {
		return true, fmt.Errorf("shop: lock the expired order's goods: %w", err)
	}
	if err := q.JournalShopOrderHold(ctx, o.ID, o.OrgID, MoveRelease); err != nil {
		return true, fmt.Errorf("shop: journal release: %w", err)
	}
	return true, nil
}
