// checkout_payment_info.go — POST /v1/public/checkout/{checkout_token}/payment-info
// (spec 68 section 3.2): the buyer's payment form is complete.
//
// The widget sends it once when the embedded Stripe Payment Element reports
// complete: true. No card data travels, only the signal. The route queues
// AddPaymentInfo ("api:<checkout id>") for Meta / GA4 from what the checkout
// stored: its explicit yes to tracking, its buyer context and its order's
// cart (queueStoredCheckoutEvent). Without the stored yes nothing is queued.
// A repeat is a no-op: the event id dedups it. The answer is 204 whatever
// was queued; an unknown token is 404.
package hfeed

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/conversions"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// checkoutPaymentInfoMaxBody is how much of the (empty) body is drained.
const checkoutPaymentInfoMaxBody = 1024

// HandlePublicCheckoutPaymentInfo serves
// POST /v1/public/checkout/{checkout_token}/payment-info.
func (h *Handler) HandlePublicCheckoutPaymentInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.checkoutQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}
	checkoutToken := chi.URLParam(r, "checkout_token")
	if !h.enforceRateLimit(w, r, "checkout.rate_limited", h.rl.CheckCheckoutToken, checkoutToken) {
		return
	}
	// The body is empty or {}: nothing of it is read.
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, checkoutPaymentInfoMaxBody))
	ctx := r.Context()
	cs, err := h.checkoutQueries.GetCheckoutSessionByToken(ctx, checkoutToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"checkout.not_found", "We cannot find this checkout. Open the ticket page again.", r,
			))
			return
		}
		h.logger.Error("public_checkout_payment_info: session lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"checkout.unavailable", "We could not read this checkout. Try again in a minute.", r,
		))
		return
	}
	// A checkout without an order, or one no longer waiting for its payment,
	// has nothing to report; the answer is the same.
	pc, err := h.checkoutQueries.GetCheckoutPaymentContext(ctx, cs.ID)
	switch {
	case err == nil && pc.OrderStatus == ordering.StatusPendingPayment:
		h.queueStoredCheckoutEvent(ctx, cs.ID, pc, conversions.KindAddPaymentInfo, paymentInfoIDPfx+cs.ID.String())
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		h.logger.Warn("public_checkout_payment_info: checkout lookup failed",
			slog.String("checkout_session_id", cs.ID.String()), slog.String("error", err.Error()))
	}
	w.WriteHeader(http.StatusNoContent)
}
