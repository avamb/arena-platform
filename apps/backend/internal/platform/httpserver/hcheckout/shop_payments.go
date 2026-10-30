// shop_payments.go — the payment webhook's hand-off to the merchandise shop
// (internal/platform/shop, spec 08_architecture/47 section 3).
//
// A shop order is paid through the same hosted-checkout providers and the
// same webhook routes as a ticket checkout, but its payment_intents row has
// no checkout_session_id: shop_orders.payment_intent_id points at it
// instead. processPaymentWebhook advances the intent exactly as for tickets
// and then, for an intent without a checkout session, asks the shop to
// follow on the same transaction. The interface keeps this package free of
// a shop import (and the shop free of an HTTP one).
package hcheckout

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ShopPayments is what the webhook needs from the shop: both calls run on the
// webhook's transaction and report handled=false for a payment that is not a
// shop payment. *shop.Service implements it.
type ShopPayments interface {
	FulfillPaidOrderTx(ctx context.Context, tx pgx.Tx, paymentIntentID uuid.UUID, meta map[string]any) (handled bool, err error)
	MarkPaymentFailedTx(ctx context.Context, tx pgx.Tx, paymentIntentID uuid.UUID) (handled bool, err error)
	MarkPaymentReviewTx(ctx context.Context, tx pgx.Tx, paymentIntentID uuid.UUID, meta map[string]any) (handled bool, err error)
}

// WithShopPayments wires the shop. Without it a payment intent without a
// checkout session is advanced and nothing else happens (the behaviour
// before the shop existed).
func (h *Handler) WithShopPayments(s ShopPayments) *Handler {
	h.shopPayments = s
	return h
}

// applyShopPayment hands a shop payment's new state to the shop: succeeded
// pays the order, failed expires it, manual_review (the amount the provider
// reports differs from the order's, the payment status is not trusted)
// parks the order in manual_review too, so the expiry sweep never expires
// an order whose money may have arrived and the operator sees it under the
// manual-review tab (SA-09). Any other state (processing) leaves the order
// pending.
func (h *Handler) applyShopPayment(ctx context.Context, tx pgx.Tx, paymentIntentID uuid.UUID, updated gen.PaymentIntentRow, req webhookPaymentIntentRequest) (bool, error) {
	if h.shopPayments == nil || updated.CheckoutSessionID != nil {
		return false, nil
	}
	switch updated.State {
	case "succeeded":
		return h.shopPayments.FulfillPaidOrderTx(ctx, tx, paymentIntentID, map[string]any{
			"payment_intent_id":   paymentIntentID.String(),
			"provider_payment_id": req.ProviderPaymentID,
			"event_type":          req.EventType,
		})
	case "failed":
		return h.shopPayments.MarkPaymentFailedTx(ctx, tx, paymentIntentID)
	case "manual_review":
		return h.shopPayments.MarkPaymentReviewTx(ctx, tx, paymentIntentID, map[string]any{
			"payment_intent_id":   paymentIntentID.String(),
			"provider_payment_id": req.ProviderPaymentID,
			"event_type":          req.EventType,
		})
	default:
		return false, nil
	}
}
