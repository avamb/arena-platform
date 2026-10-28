package shop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
// A payment that arrives after the order's hold ended (the order expired,
// was cancelled, or is still pending past its expiry) never oversells: the
// order's variants are locked and their availability read again. When every
// tracked line is still there the order is paid as usual (metadata.paid_after
// names the status it had); when one is not, the order is parked in
// manual_review with the payment recorded, nothing taken off the shelf, and
// v1.shop.order.manual_review written for the operator.
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
	holdLive := o.Status == StatusPendingPayment && o.ExpiresAt != nil && o.ExpiresAt.After(time.Now())
	if !holdLive {
		after := o.Status
		if after == StatusPendingPayment {
			after = "hold_expired"
		}
		meta["paid_after"] = after
		if err := q.LockShopOrderVariants(ctx, o.ID); err != nil {
			return true, fmt.Errorf("shop: lock the late order's goods: %w", err)
		}
		lines, err := q.ShopOrderShortages(ctx, o.ID)
		if err != nil {
			return true, fmt.Errorf("shop: read the late order's goods: %w", err)
		}
		if short := shortLines(lines); len(short) > 0 {
			s.logger.Warn("shop: late payment for goods that are gone; order parked for review",
				"order_id", o.ID.String(), "status", o.Status)
			return true, s.parkForReviewTx(ctx, tx, q, o, meta, short)
		}
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
	if o.Status != StatusPendingPayment {
		return true, nil
	}
	if _, err := q.SetShopOrderStatus(ctx, o.ID, StatusExpired, nil, []byte(`{"expired_by":"payment_failed"}`)); err != nil {
		return true, fmt.Errorf("shop: expire order: %w", err)
	}
	if err := q.JournalShopOrderHold(ctx, o.ID, o.OrgID, MoveRelease); err != nil {
		return true, fmt.Errorf("shop: journal release: %w", err)
	}
	return true, nil
}
