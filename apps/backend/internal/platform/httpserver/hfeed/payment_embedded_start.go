// payment_embedded_start.go — checkout/start's payment step with the embedded
// checkout in front of the hosted page (spec 65 §3.4, §3.5).
package hfeed

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
)

// paymentStartAnswer is what checkout/start tells the widget about the
// payment: the hosted page to go to, or the embedded form to show.
type paymentStartAnswer struct {
	// redirectURL is the hosted page ("" for an embedded payment).
	redirectURL string
	// embedded is the `payment` block of an embedded payment (nil for the
	// hosted page).
	embedded *EmbeddedPaymentBlock
}

// startPayment runs the payment step of a paid checkout: the embedded
// session when everything allows it, otherwise — and whenever the provider
// definitively refused the embedded session — the hosted page exactly as
// before. On failure it has written the HTTP error and reports false.
func (h *Handler) startPayment(ctx context.Context, w http.ResponseWriter, r *http.Request, in hostedPaymentInput, wantsEmbedded bool) (paymentStartAnswer, bool) {
	if wantsEmbedded && h.embeddedCheckout {
		if block, handled, ok := h.tryEmbeddedPayment(ctx, w, r, in); handled {
			return paymentStartAnswer{embedded: block}, ok
		}
	}
	url, ok := h.startHostedPayment(ctx, w, r, in)
	return paymentStartAnswer{redirectURL: url}, ok
}

// tryEmbeddedPayment creates the embedded session and records its payment
// intent. handled=false means "create the hosted page instead": the payment
// cannot be embedded, or the provider refused the embedded session for good
// (nothing was created, so a hosted page cannot double the payment). An
// unknown outcome (timeout, 5xx) is NOT handed to the hosted page: the
// embedded session may exist, and a second page could be paid twice.
func (h *Handler) tryEmbeddedPayment(ctx context.Context, w http.ResponseWriter, r *http.Request, in hostedPaymentInput) (*EmbeddedPaymentBlock, bool, bool) {
	es, ok := h.paymentStarter.(EmbeddedPaymentStarter)
	if !ok || h.checkoutQueries == nil {
		return nil, false, false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hostedPaymentStartTimeout)
	defer cancel()

	res, err := es.StartEmbeddedCheckout(ctx, h.embeddedCheckoutRequest(ctx, in))
	if err != nil {
		var refused *payments.HostedCheckoutRefusedError
		_, isTyped := AsPaymentStartError(err)
		switch {
		case errors.Is(err, ErrEmbeddedCheckoutUnavailable), isTyped:
			// Not embeddable, or the configuration itself is unusable: the
			// hosted path answers exactly as it always did.
			return nil, false, false
		case errors.As(err, &refused) && refused.Reason == payments.HostedCheckoutRefusalInvalidEmail:
			// The buyer's address, not the mode: the hosted page would be
			// refused the same way. Same answer as the hosted path.
			h.releaseRefusedCheckout(ctx, r, in)
			h.logger.Warn("public_feed_checkout: the payment provider refused the buyer's e-mail address",
				slog.String("code", ErrCodeInvalidEmail),
				slog.String("checkout_session_id", in.Session.ID.String()),
				slog.String("org_id", in.CheckCtx.OrgID.String()),
				slog.String("error", err.Error()),
			)
			writeInvalidEmail(w, r)
			return nil, true, false
		case errors.As(err, &refused):
			// The provider refused the embedded mode for good (a parameter
			// or version it does not take): nothing exists, the buyer gets
			// the hosted page instead of losing the sale.
			h.logger.Warn("public_feed_checkout: the provider refused the embedded checkout; falling back to the hosted page",
				slog.String("checkout_session_id", in.Session.ID.String()),
				slog.String("org_id", in.CheckCtx.OrgID.String()),
				slog.String("error", err.Error()),
			)
			return nil, false, false
		default:
			h.logger.Error("public_feed_checkout: the provider did not create the embedded checkout",
				slog.String("checkout_session_id", in.Session.ID.String()),
				slog.String("org_id", in.CheckCtx.OrgID.String()),
				slog.String("error", err.Error()),
			)
			// 503, never 502: Cloudflare replaces an origin 502 (AGENTS.md).
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				ErrCodePaymentStartFailed, "The payment service could not start this payment. Try again in a minute.", r,
			))
			return nil, true, false
		}
	}

	csID := in.Session.ID
	pi, err := h.checkoutQueries.InsertEmbeddedPaymentIntent(ctx,
		&csID, in.CheckCtx.OrgID, res.Provider, res.SessionID, in.Breakdown.Total, in.Breakdown.Currency,
	)
	if err != nil {
		// Same rule as the hosted path: money against a row that does not
		// exist would be lost, so the buyer gets no form.
		h.logger.Error("public_feed_checkout: recording the embedded payment intent failed; no payment form handed out",
			slog.String("checkout_session_id", csID.String()),
			slog.String("provider_payment_id", res.SessionID),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			ErrCodePaymentStartFailed, "We could not start the payment. Try again in a minute.", r,
		))
		return nil, true, false
	}
	h.recordPageExpiry(ctx, h.checkoutQueries, pi.ID, in.PageExpiresAt)

	// The client secret is deliberately NOT logged.
	h.logger.Info("public_feed_checkout: embedded payment started",
		slog.String("checkout_session_id", csID.String()),
		slog.String("org_id", in.CheckCtx.OrgID.String()),
		slog.String("provider_payment_id", res.SessionID),
		slog.Int64("amount", in.Breakdown.Total),
		slog.String("currency", in.Breakdown.Currency),
	)
	block := res.Block
	return &block, true, true
}

// embeddedCheckoutRequest is the embedded session request of a just-confirmed
// checkout, shared by checkout/start and the prepared order (spec 65). A
// prepared order has no buyer yet, so its e-mail, name and phone are empty
// and Stripe takes the address from the widget (updateEmail) before confirm.
func (h *Handler) embeddedCheckoutRequest(ctx context.Context, in hostedPaymentInput) HostedCheckoutRequest {
	return HostedCheckoutRequest{
		OrgID:             in.CheckCtx.OrgID,
		ChannelID:         in.CheckCtx.SalesChannelID,
		CheckoutSessionID: in.Session.ID,
		CheckoutToken:     in.CheckoutToken,
		OrderID:           in.OrderID,
		Amount:            in.Breakdown.Total,
		Currency:          in.Breakdown.Currency,
		ProductName:       h.hostedLineItemName(ctx, in.CheckCtx),
		BuyerEmail:        in.Buyer.Email,
		BuyerName:         derefOr(in.Buyer.Name),
		BuyerPhone:        derefOr(in.Buyer.Phone),
		ReturnURL:         in.ReturnBase,
		ExpiresAtUnix:     in.pageExpiry(h.paymentGrace).Unix(),
		BuyerLocale:       in.Buyer.Locale,
	}
}

// writeEmbeddedStartAnswer writes the 201 of an embedded checkout/start and,
// when the request carried an Idempotency-Key (П-19), stores the replayable
// copy itself WITHOUT the client secret: the middleware would otherwise keep
// the whole body, secret included, in idempotency_keys for a day (spec 65
// §3.7: the secret is never stored). A replay therefore answers the same
// order with an empty payment.client_secret, and the widget reads the secret
// from GET /v1/public/checkout/{token}.
func (h *Handler) writeEmbeddedStartAnswer(w http.ResponseWriter, r *http.Request, body map[string]any, block *EmbeddedPaymentBlock) {
	body["payment"] = block
	httputil.WriteJSON(w, http.StatusCreated, body)

	if _, _, _, _, ok := idempotency.FromContext(r.Context()); !ok {
		return
	}
	// Whatever happens below, the middleware must not save the body itself.
	defer idempotency.MarkPersisted(w)
	redacted := make(map[string]any, len(body))
	for k, v := range body {
		redacted[k] = v
	}
	stored := *block
	stored.ClientSecret = ""
	redacted["payment"] = stored
	raw, err := json.Marshal(redacted)
	if err != nil || h.pool == nil {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		h.logger.Warn("public_feed_checkout: storing the idempotent answer failed", slog.String("error", err.Error()))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := idempotency.SaveTx(ctx, tx, w, idempotency.StoredResponse{
		Status:      http.StatusCreated,
		ContentType: "application/json",
		Body:        raw,
	}); err != nil {
		h.logger.Warn("public_feed_checkout: storing the idempotent answer failed", slog.String("error", err.Error()))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.logger.Warn("public_feed_checkout: storing the idempotent answer failed", slog.String("error", err.Error()))
	}
}
