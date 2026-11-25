// embedded_payments.go — the webhook rules an embedded checkout (spec 65 §3)
// adds: one checkout may carry TWO provider sessions on the same hold (an
// embedded session and the hosted page that replaced it, hfeed's
// POST /v1/public/checkout/{token}/hosted).
//
//   - Either session's "completed and paid" event pays the order. A
//     superseded session stays findable by its cs_… id and non-terminal.
//   - The order is paid once. A second paid session is a double charge: its
//     intent goes to manual_review with failure code double_payment, which the
//     ops watchdog alerts on, and nothing is fulfilled again.
//   - Once the order is paid, every other open page of the hold is closed.
//   - payment_intent.* events never move a checkout-session row (§3.6).
package hcheckout

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// failureCodeDoublePayment is payment_intents.failure_code of a second
// payment for a checkout that another payment already paid.
const failureCodeDoublePayment = "double_payment"

// checkoutSessionIDPrefix starts every provider Checkout Session id arena
// files a hosted or embedded payment under (Stripe cs_…).
const checkoutSessionIDPrefix = "cs_"

// paymentIntentEventPrefix starts every provider event about the payment
// object behind a Checkout Session (Stripe payment_intent.*).
const paymentIntentEventPrefix = "payment_intent."

// otherPagesExpireTimeout bounds the post-commit closing of the other pages.
const otherPagesExpireTimeout = 15 * time.Second

// paymentIntentEventOnCheckoutSession reports whether a payment_intent.*
// event reached a row created from a Checkout Session. Such a row is moved
// only by checkout.session.* events (spec 65 §3.6).
func paymentIntentEventOnCheckoutSession(eventType string, pi gen.PaymentIntentRow) bool {
	if !strings.HasPrefix(eventType, paymentIntentEventPrefix) {
		return false
	}
	return pi.ProviderPaymentID != nil && strings.HasPrefix(*pi.ProviderPaymentID, checkoutSessionIDPrefix)
}

// completedByAnotherPayment reports whether a completed checkout was paid by
// a payment intent other than paidNow. A checkout completed without one (a
// free order) does not count.
func completedByAnotherPayment(cs gen.CheckoutSessionRow, paidNow uuid.UUID) bool {
	if cs.PaymentIntentID == nil {
		return false
	}
	other := strings.TrimSpace(*cs.PaymentIntentID)
	return other != "" && other != paidNow.String()
}

// parkDoublePaymentTx moves a second payment of an already paid checkout to
// manual_review on the webhook's own transaction. The caller answers 500 on
// an error, so the provider redelivers: a lost double charge is worse than a
// retried webhook.
func parkDoublePaymentTx(ctx context.Context, txQ *gen.Queries, paymentIntentID uuid.UUID) (gen.PaymentIntentRow, error) {
	code := failureCodeDoublePayment
	msg := "the checkout was already paid by another payment; refund this one by hand"
	return txQ.UpdatePaymentIntentState(ctx, paymentIntentID, "manual_review", nil, nil, &code, &msg, nil, nil)
}

// expireOtherPaymentPages closes every OTHER open provider session of a
// checkout that paidNow has just paid. A superseded row is skipped: its
// session was expired before it was superseded. Best effort, after the
// commit: every failure is logged and nothing else changes.
func (h *Handler) expireOtherPaymentPages(ctx context.Context, checkoutSessionID, paidNow uuid.UUID) {
	if h.paymentIntentQueries == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), otherPagesExpireTimeout)
	defer cancel()
	intents, err := h.paymentIntentQueries.ListPaymentIntentsByCheckout(ctx, checkoutSessionID)
	if err != nil {
		h.logger.Warn("webhook: listing the other payment pages of a paid checkout failed",
			slog.String("checkout_session_id", checkoutSessionID.String()), slog.String("error", err.Error()))
		return
	}
	for _, other := range intents {
		if other.ID == paidNow || isTerminalPaymentIntentState(other.State) || other.State == "manual_review" {
			continue
		}
		if other.FailureCode != nil && *other.FailureCode == gen.PaymentIntentFailureSuperseded {
			continue
		}
		if other.ProviderPaymentID == nil || !strings.HasPrefix(*other.ProviderPaymentID, checkoutSessionIDPrefix) {
			continue
		}
		if err := h.expireProviderSession(ctx, other); err != nil {
			h.logger.Warn("webhook: closing another open payment page of a paid checkout failed",
				slog.String("checkout_session_id", checkoutSessionID.String()),
				slog.String("payment_intent_id", other.ID.String()),
				slog.String("error", err.Error()))
			continue
		}
		h.logger.Info("webhook: closed another open payment page of a paid checkout",
			slog.String("checkout_session_id", checkoutSessionID.String()),
			slog.String("payment_intent_id", other.ID.String()))
	}
}

// expireProviderSession closes one provider session through the module of
// the organization's config: an embedded session through
// EmbeddedCheckoutProvider, a hosted one through HostedCheckoutExpirer.
func (h *Handler) expireProviderSession(ctx context.Context, pi gen.PaymentIntentRow) error {
	cfg, cfgErr := ResolveProviderConfig(ctx, h.orgQueries, pi.OrgID, pi.Provider)
	if cfgErr != nil {
		return cfgErr
	}
	opts := h.moduleOptions
	opts.Mode = cfg.Mode
	module, err := paymodules.Registry().Build(pi.Provider, payments.SecretsFromJSON(cfg.Secrets), opts)
	if err != nil {
		return err
	}
	hosted := pi.HostedCheckoutURL != nil && *pi.HostedCheckoutURL != ""
	if emb, ok := module.(payments.EmbeddedCheckoutProvider); ok && !hosted {
		return emb.ExpireEmbeddedCheckoutSession(ctx, *pi.ProviderPaymentID)
	}
	if exp, ok := module.(payments.HostedCheckoutExpirer); ok {
		return exp.ExpireCheckoutSession(ctx, *pi.ProviderPaymentID)
	}
	return nil
}
