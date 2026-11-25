// checkout_prepare.go — the early ("prepared") widget order of spec 65.
//
// On a phone the embedded Stripe form takes seconds to become ready, and that
// chain can only start once a Checkout Session exists. So the widget may ask
// checkout/start with "prepare": true when the order sheet OPENS: the hold,
// the order and the embedded Stripe session (ui_mode=elements) are created
// without the buyer, and the buyer is attached at "Pay" through
// POST /v1/public/checkout/{checkout_token}/buyer (checkout_buyer_attach.go).
//
// Only a plain paid cart is prepared: no promo code, no marketing tick, no
// buyer, no consent (all of them depend on the buyer, and the prepared total
// must never depend on the e-mail), a total above zero, and a channel that
// gives the embedded form. Anything else is checkout.prepare_unavailable and
// the widget starts the checkout the classic way at "Pay".
//
// A prepared order never falls back to the hosted page: when the embedded
// session cannot be created, the order is cancelled and the places go back
// at once.
//
// Until a buyer is attached, the order is public_feed with buyer_email NULL;
// the sales funnel, the unpaid-order reminder and the organizer's order lists
// leave such an order out while it is not paid.
package hfeed

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ErrCodePrepareUnavailable refuses a prepared order; the widget then starts
// the checkout the classic way.
const ErrCodePrepareUnavailable = "checkout.prepare_unavailable"

// Reasons in the details of checkout.prepare_unavailable.
const (
	prepareReasonNoCapability   = "capability_missing"
	prepareReasonBuyer          = "buyer_present"
	prepareReasonPromo          = "promo_code_present"
	prepareReasonMarketing      = "marketing_consent_present"
	prepareReasonConsent        = "consent_present"
	prepareReasonNotEmbeddable  = "embedded_unavailable"
	prepareReasonFree           = "free_order"
	prepareReasonPaymentFailure = "payment_start_failed"
)

// embeddedAvailability is the pre-hold check of the embedded mode
// (StripePaymentStarter.EmbeddedCheckoutAvailable).
type embeddedAvailability interface {
	EmbeddedCheckoutAvailable(ctx context.Context, orgID, channelID uuid.UUID) error
}

// prepareRequestRefusal names what a prepare request carries that it must
// not, or "" when it may be prepared.
func prepareRequestRefusal(req PublicFeedCheckoutStartRequest) string {
	switch {
	case !wantsEmbeddedCheckout(req.Capabilities):
		return prepareReasonNoCapability
	case req.Buyer != nil || strings.TrimSpace(req.HolderEmail) != "":
		return prepareReasonBuyer
	case strings.TrimSpace(derefOr(req.PromoCode)) != "":
		return prepareReasonPromo
	case req.MarketingConsent != nil:
		return prepareReasonMarketing
	case req.Consent != nil:
		return prepareReasonConsent
	}
	return ""
}

func writePrepareUnavailable(w http.ResponseWriter, r *http.Request, status int, reason string) {
	httputil.WriteJSON(w, status, httputil.ErrorEnvelopeWithDetails(
		ErrCodePrepareUnavailable,
		"This order cannot be prepared in advance. Start the checkout when the buyer presses Pay.", r,
		map[string]any{"reason": reason},
	))
}

// preparedPaymentAvailable reports whether a new payment of this channel
// would be embedded: the deployment switch, an embedded-capable starter and
// the channel's provider config (browser key, stripe_ui not "hosted"). Read
// on the pool, before the hold transaction opens.
func (h *Handler) preparedPaymentAvailable(ctx context.Context, checkCtx gen.PublicCheckoutContextRow) bool {
	if !h.embeddedCheckout || h.paymentStarter == nil {
		return false
	}
	if _, ok := h.paymentStarter.(EmbeddedPaymentStarter); !ok {
		return false
	}
	av, ok := h.paymentStarter.(embeddedAvailability)
	if !ok {
		return false
	}
	if err := av.EmbeddedCheckoutAvailable(ctx, checkCtx.OrgID, checkCtx.SalesChannelID); err != nil {
		if !errors.Is(err, ErrEmbeddedCheckoutUnavailable) {
			h.logger.Warn("public_feed_checkout: the embedded mode could not be checked; not preparing",
				slog.String("org_id", checkCtx.OrgID.String()),
				slog.String("channel_id", checkCtx.SalesChannelID.String()),
				slog.String("error", err.Error()))
		}
		return false
	}
	return true
}

// startPreparedPayment creates the embedded session of a prepared order and
// records its payment intent. Any failure cancels the order and releases the
// hold (the client secret never reached the browser, so no session created
// at the provider can be paid; it dies at its own expiry) and answers 409
// checkout.prepare_unavailable. It never creates a hosted page.
func (h *Handler) startPreparedPayment(ctx context.Context, w http.ResponseWriter, r *http.Request, in hostedPaymentInput) (*EmbeddedPaymentBlock, bool) {
	// The provider call and the writes after it must not die with the
	// buyer's connection: detached, bounded on their own.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hostedPaymentStartTimeout)
	defer cancel()

	es, ok := h.paymentStarter.(EmbeddedPaymentStarter)
	if !ok || !h.embeddedCheckout || h.checkoutQueries == nil {
		h.releaseRefusedCheckout(pctx, r, in)
		writePrepareUnavailable(w, r, http.StatusConflict, prepareReasonNotEmbeddable)
		return nil, false
	}
	if in.KeepHold && holdTooLateForPage(in.ExpiresAt, time.Now().UTC()) {
		// No provider page for a hold about to end: the order is closed, the
		// seats stay in the hold until it ends.
		h.releaseRefusedCheckout(pctx, r, in)
		writeHoldExpired(w, r)
		return nil, false
	}

	res, err := es.StartEmbeddedCheckout(pctx, h.embeddedCheckoutRequest(pctx, in))
	if err != nil {
		h.logger.Warn("public_feed_checkout: the embedded session of a prepared order was not created; the order is cancelled",
			slog.String("checkout_session_id", in.Session.ID.String()),
			slog.String("org_id", in.CheckCtx.OrgID.String()),
			slog.String("error", err.Error()),
		)
		h.releaseRefusedCheckout(pctx, r, in)
		reason := prepareReasonPaymentFailure
		if errors.Is(err, ErrEmbeddedCheckoutUnavailable) {
			reason = prepareReasonNotEmbeddable
		}
		writePrepareUnavailable(w, r, http.StatusConflict, reason)
		return nil, false
	}

	csID := in.Session.ID
	// The attempt and its page expiry in one row; on the hold path under the
	// hold's row lock, refused when the hold closed meanwhile.
	_, err = h.recordPageAttempt(pctx, newPageAttempt(in, res.Provider, res.SessionID, ""))
	if errors.Is(err, errHoldClosedForPage) {
		h.refuseClosedHoldPage(pctx, w, r, in.CheckCtx.OrgID, in.CheckCtx.SalesChannelID, csID, res.Provider, res.SessionID)
		return nil, false
	}
	if err != nil {
		// Money against a row that does not exist would be lost: the
		// session is closed at the provider (best effort, it was never
		// handed out) and the order is cancelled.
		h.logger.Error("public_feed_checkout: recording the payment intent of a prepared order failed; the order is cancelled",
			slog.String("checkout_session_id", csID.String()),
			slog.String("provider_payment_id", res.SessionID),
			slog.String("error", err.Error()),
		)
		if expErr := es.ExpireEmbeddedCheckout(pctx, in.CheckCtx.OrgID, in.CheckCtx.SalesChannelID, res.Provider, res.SessionID); expErr != nil &&
			!errors.Is(expErr, payments.ErrCheckoutSessionNotExpirable) {
			h.logger.Warn("public_feed_checkout: closing the unrecorded embedded session failed; it ends at its own expiry",
				slog.String("checkout_session_id", csID.String()),
				slog.String("provider_payment_id", res.SessionID),
				slog.String("error", expErr.Error()),
			)
		}
		h.releaseRefusedCheckout(pctx, r, in)
		writePrepareUnavailable(w, r, http.StatusConflict, prepareReasonPaymentFailure)
		return nil, false
	}

	// The client secret is deliberately NOT logged.
	h.logger.Info("public_feed_checkout: prepared order with an embedded payment",
		slog.String("checkout_session_id", csID.String()),
		slog.String("org_id", in.CheckCtx.OrgID.String()),
		slog.String("provider_payment_id", res.SessionID),
		slog.Int64("amount", in.Breakdown.Total),
		slog.String("currency", in.Breakdown.Currency),
	)
	block := res.Block
	return &block, true
}
