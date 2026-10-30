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
// already paid (or shipped, refunded, parked for review) is left alone.
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
	case StatusPaid, StatusShipped, StatusRefunded, StatusPartiallyRefunded, StatusManualReview:
		return true, nil
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
		if after == StatusPendingPayment {
			after = "hold_expired"
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

// parkForReviewTx records a late payment whose goods are gone: status
// manual_review with paid_at, the reason in metadata, the hold's release
// journaled when the order was still pending, and the outbox event.
func (s *Service) parkForReviewTx(ctx context.Context, tx pgx.Tx, q *gen.Queries, o gen.ShopOrderRow, meta map[string]any, short []map[string]any) error {
	meta["review_reason"] = "stock_short"
	meta["short_items"] = short
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	parked, err := q.SetShopOrderStatus(ctx, o.ID, StatusManualReview, nil, raw)
	if err != nil {
		return fmt.Errorf("shop: park order for review: %w", err)
	}
	if o.Status == StatusPendingPayment {
		if err := q.JournalShopOrderHold(ctx, o.ID, o.OrgID, MoveRelease); err != nil {
			return fmt.Errorf("shop: journal release: %w", err)
		}
	}
	return s.appendEvent(ctx, tx, EventOrderManualReview, parked, map[string]any{
		"reason": "stock_short", "short_items": short,
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

// ReviewReasonPayment is metadata.review_reason of an order whose PAYMENT, not
// its stock, needs a human (SA-09).
const ReviewReasonPayment = "payment_manual_review"

// MarkPaymentReviewTx is called by the payment webhook, inside its
// transaction, when a shop order's payment intent was parked in manual_review
// (the paid amount differs from the intent's, the provider's payment status
// is not one arena trusts). The money may or may not have arrived, so the
// order must not be left pending, where the expiry sweep would free its goods
// and expire it under a buyer who may have paid: it is parked in
// manual_review like a late payment whose goods are gone (the hold is
// released, v1.shop.order.manual_review is written, the order shows under
// the manual-review tab) and no letter goes out. The operator then ships it
// (the goods are taken off the shelf if they are free) or refunds it.
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
	meta["review_reason"] = ReviewReasonPayment
	raw, err := json.Marshal(meta)
	if err != nil {
		return true, err
	}
	if err := q.LockShopOrderVariants(ctx, o.ID); err != nil {
		return true, fmt.Errorf("shop: lock the parked order's goods: %w", err)
	}
	parked, err := q.SetShopOrderStatus(ctx, o.ID, StatusManualReview, nil, raw)
	if err != nil {
		return true, fmt.Errorf("shop: park order for payment review: %w", err)
	}
	if err := q.JournalShopOrderHold(ctx, o.ID, o.OrgID, MoveRelease); err != nil {
		return true, fmt.Errorf("shop: journal release: %w", err)
	}
	s.logger.Warn("shop: payment parked in manual_review; order parked with it", "order_id", o.ID.String())
	return true, s.appendEvent(ctx, tx, EventOrderManualReview, parked, map[string]any{
		"reason": ReviewReasonPayment,
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
	if o.Status == StatusManualReview && paymentReviewParked(o) {
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

// paymentReviewParked: the order was parked by MarkPaymentReviewTx (its
// payment, not its stock, is what waits for a human).
func paymentReviewParked(o gen.ShopOrderRow) bool {
	var m struct {
		Reason string `json:"review_reason"`
	}
	return json.Unmarshal(o.Metadata, &m) == nil && m.Reason == ReviewReasonPayment
}
