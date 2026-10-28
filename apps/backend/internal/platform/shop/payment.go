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
// already paid (or shipped, refunded) is left alone.
//
// A payment that arrives after its order expired or was cancelled is still
// recorded as paid (the money was taken); metadata.paid_after names the
// status it had, and the stock may go below zero (an oversell the operator
// sees in the order list).
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
	}
	if meta == nil {
		meta = map[string]any{}
	}
	if o.Status != StatusPendingPayment {
		meta["paid_after"] = o.Status
		s.logger.Warn("shop: payment arrived for an order that no longer held its goods",
			"order_id", o.ID.String(), "status", o.Status)
	}
	if _, err := s.markPaidTx(ctx, tx, q, o, meta); err != nil {
		return true, err
	}
	return true, nil
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
	if err := q.ApplyShopOrderSale(ctx, o.ID, o.OrgID); err != nil {
		return gen.ShopOrderRow{}, fmt.Errorf("shop: take the paid goods off the shelf: %w", err)
	}
	if err := s.appendEvent(ctx, tx, EventOrderPaid, paid, map[string]any{
		"subtotal": paid.Subtotal, "shipping_fee": paid.ShippingFee,
	}); err != nil {
		return gen.ShopOrderRow{}, err
	}
	return paid, nil
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
