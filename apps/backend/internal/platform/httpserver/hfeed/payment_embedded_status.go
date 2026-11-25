// payment_embedded_status.go — the embedded payment form in the order status
// (GET /v1/public/checkout/{token}, spec 65 §3.7).
package hfeed

import (
	"context"
	"log/slog"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// embeddedReadTimeout bounds the provider read behind one status request.
const embeddedReadTimeout = 10 * time.Second

// intentSuperseded reports whether a payment intent was replaced by a newer
// page on the same checkout (the hosted fallback of an embedded session).
func intentSuperseded(pi gen.PaymentIntentRow) bool {
	return pi.FailureCode != nil && *pi.FailureCode == gen.PaymentIntentFailureSuperseded
}

// isOpenIntent reports whether a payment intent may still be paid on: not
// terminal, not parked, not superseded.
func isOpenIntent(pi gen.PaymentIntentRow) bool {
	switch pi.State {
	case "succeeded", "failed", "manual_review":
		return false
	}
	return !intentSuperseded(pi)
}

// isEmbeddedIntent reports whether an open payment intent is an embedded
// session: a provider session id and no hosted page.
func isEmbeddedIntent(pi gen.PaymentIntentRow) bool {
	if pi.ProviderPaymentID == nil || *pi.ProviderPaymentID == "" {
		return false
	}
	if pi.HostedCheckoutURL != nil && *pi.HostedCheckoutURL != "" {
		return false
	}
	entry, ok := hostedEntry(pi.Provider)
	return ok && entry.Descriptor.Capabilities.EmbeddedCheckout
}

// newestOpenIntent is the newest payment intent of the checkout the buyer
// may still pay on, or nil.
func newestOpenIntent(intents []gen.PaymentIntentRow) *gen.PaymentIntentRow {
	// ListPaymentIntentsByCheckout returns newest first.
	for i := range intents {
		if isOpenIntent(intents[i]) {
			return &intents[i]
		}
	}
	return nil
}

// pendingEmbeddedPayment returns the embedded payment form of the checkout's
// newest open payment, or nil. The client secret is read back from the
// provider with the organization's own key on every call: it is never stored
// and never logged. Best effort: the status renders without it.
func (h *Handler) pendingEmbeddedPayment(ctx context.Context, cs gen.CheckoutSessionRow) *EmbeddedPaymentBlock {
	es, ok := h.paymentStarter.(EmbeddedPaymentStarter)
	if !ok || h.checkoutQueries == nil {
		return nil
	}
	intents, err := h.checkoutQueries.ListPaymentIntentsByCheckout(ctx, cs.ID)
	if err != nil {
		return nil
	}
	open := newestOpenIntent(intents)
	if open == nil || !isEmbeddedIntent(*open) {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, embeddedReadTimeout)
	defer cancel()
	sess, block, err := es.ReadEmbeddedCheckout(rctx, cs.OrgID, cs.ChannelID, open.Provider, *open.ProviderPaymentID)
	if err != nil {
		h.logger.Warn("public_checkout_status: reading the embedded payment back failed",
			slog.String("checkout_session_id", cs.ID.String()),
			slog.String("payment_intent_id", open.ID.String()),
			slog.String("error", err.Error()),
		)
		return nil
	}
	if sess.Status != payments.EmbeddedSessionOpen || block.ClientSecret == "" || block.PublishableKey == "" {
		return nil
	}
	return &block
}
