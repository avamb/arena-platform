// checkout_buyer_cancel.go — POST /v1/public/checkout/{checkout_token}/cancel
// (spec 62 §5.4 п. 3, П-2).
//
// The buyer pressed "Back" on the provider's page, or the site's own cancel
// button: the order that was not paid is closed and its places go back on
// sale at once, instead of when the payment window ends.
//
// Before anything is written the provider is asked about every open payment
// attempt of the checkout (outside any transaction):
//
//   - paid: the order is completed through the webhook's own state machine
//     (ConfirmedPaymentApplier) and the answer is "paid" — the buyer sees
//     "thank you", nothing is cancelled;
//   - still processing (a pre-authorization, a page mid-payment): 409
//     checkout.payment_in_progress, nothing is cancelled;
//   - unpaid, unknown, or the provider did not answer: the order is
//     cancelled. A payment confirmed later is refunded in full
//     (hcheckout late_payment.go, "buyer cancelled first");
//   - a provider arena cannot ask (no read-back by reference, e.g. Stripe)
//     whose page was handed out: 409 checkout.payment_in_progress — its own
//     page expiry ends the attempt.
//
// Idempotent: a repeat answers the same state with cancelled=true and writes
// nothing. The answer is always the public status the order now has; the
// site then shows it (GET /v1/public/checkout/{token} answers the same).
package hfeed

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ErrCodePaymentInProgress answers a cancel while a payment may still land.
const ErrCodePaymentInProgress = "checkout.payment_in_progress"

// hostedPaymentReader is the read-back half of the payment starter
// (StripePaymentStarter.LookupHostedPayment).
type hostedPaymentReader interface {
	LookupHostedPayment(ctx context.Context, orgID uuid.UUID, provider, reference string) (HostedPaymentLookup, error)
}

// WithPaymentApplier wires the webhook state machine the cancel route uses
// when the provider says the order was paid after all.
func (h *Handler) WithPaymentApplier(apply ConfirmedPaymentApplier) *Handler {
	h.paymentApplier = apply
	return h
}

// precheckVerdict is what the provider pre-check decided.
type precheckVerdict int

const (
	precheckCancel precheckVerdict = iota
	precheckPaid
	precheckInProgress
)

// HandlePublicCheckoutCancel serves POST /v1/public/checkout/{checkout_token}/cancel.
func (h *Handler) HandlePublicCheckoutCancel(w http.ResponseWriter, r *http.Request) {
	if h.checkoutQueries == nil || h.reservationQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}
	checkoutToken := chi.URLParam(r, "checkout_token")
	if !h.enforceRateLimit(w, r, "checkout.rate_limited", h.rl.CheckCheckoutToken, checkoutToken) {
		return
	}
	ctx := r.Context()

	cs, err := h.checkoutQueries.GetCheckoutSessionByToken(ctx, checkoutToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"checkout.not_found", "We cannot find this checkout. Open the ticket page again.", r,
			))
			return
		}
		h.logger.Error("public_checkout_cancel: session lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"checkout.lookup_failed", "We could not load this checkout. Try again in a minute.", r,
		))
		return
	}

	// Terminal states need no provider call.
	switch cs.State {
	case "completed":
		h.writeCancelAnswer(w, checkoutToken, "paid", false)
		return
	case "abandoned":
		h.writeCancelAnswer(w, checkoutToken, "cancelled", true)
		return
	case "expired":
		h.writeCancelAnswer(w, checkoutToken, "expired", false)
		return
	}

	switch h.precheckOpenPayments(ctx, cs) {
	case precheckPaid:
		h.writeCancelAnswer(w, checkoutToken, "paid", false)
		return
	case precheckInProgress:
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			ErrCodePaymentInProgress,
			"Your payment is still being processed, so the order cannot be cancelled now. Check the order status in a minute.", r,
		))
		return
	}

	via, _ := audit.ClientChannelFromContext(ctx)
	res, err := hcheckout.CancelUnpaidCheckout(ctx, h.pool, h.checkoutQueries, h.audit, hcheckout.BuyerCancelInput{
		CheckoutSessionID: cs.ID,
		ActorLabel:        "buyer:checkout_token",
		Via:               via,
		IP:                h.clientIP(r),
		Now:               time.Now().UTC(),
	})
	if err != nil {
		h.logger.Error("public_checkout_cancel: cancel failed",
			slog.String("checkout_session_id", cs.ID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"checkout.cancel_failed", "We could not cancel the order. Try again in a minute.", r,
		))
		return
	}
	switch res.Outcome {
	case hcheckout.BuyerCancelCancelled:
		h.logger.Info("public_checkout_cancel: the buyer cancelled an unpaid order",
			slog.String("checkout_session_id", cs.ID.String()))
		h.writeCancelAnswer(w, checkoutToken, publicStatusCancelled, true)
	case hcheckout.BuyerCancelAlreadyCancelled:
		h.writeCancelAnswer(w, checkoutToken, publicStatusCancelled, true)
	case hcheckout.BuyerCancelPaid:
		h.writeCancelAnswer(w, checkoutToken, publicStatusPaid, false)
	case hcheckout.BuyerCancelExpired:
		h.writeCancelAnswer(w, checkoutToken, publicStatusExpired, false)
	default:
		// An operator holds the order, or it moved on (refunded): answer
		// exactly what GET /v1/public/checkout/{token} reports, through the
		// same mapping (needs_help for manual_review).
		h.writeCancelAnswer(w, checkoutToken, h.currentPublicStatus(ctx, cs), false)
	}
}

// currentPublicStatus is the public status GET /v1/public/checkout/{token}
// reports for cs: the session state refined by its order through the one
// mapping (checkoutStatusToPublic, then publicOrderStatus).
func (h *Handler) currentPublicStatus(ctx context.Context, cs gen.CheckoutSessionRow) string {
	status := checkoutStatusToPublic(cs.State)
	if ord, err := h.checkoutQueries.GetOrderByCheckoutSession(ctx, cs.ID); err == nil {
		status = publicOrderStatus(status, ord.Status)
	}
	return status
}

func (h *Handler) writeCancelAnswer(w http.ResponseWriter, token, status string, cancelled bool) {
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"checkout_token": token,
		"status":         status,
		"cancelled":      cancelled,
	})
}

// precheckOpenPayments asks the provider about every open payment attempt.
func (h *Handler) precheckOpenPayments(ctx context.Context, cs gen.CheckoutSessionRow) precheckVerdict {
	intents, err := h.checkoutQueries.ListPaymentIntentsByCheckout(ctx, cs.ID)
	if err != nil {
		// Nothing can be read: the cancel goes on, a payment that still
		// lands is refunded (spec: "the provider did not answer").
		h.logger.Warn("public_checkout_cancel: payment attempts could not be listed; cancelling anyway",
			slog.String("checkout_session_id", cs.ID.String()), slog.String("error", err.Error()))
		return precheckCancel
	}
	reader, canRead := h.paymentStarter.(hostedPaymentReader)
	for _, pi := range intents {
		if pi.State == "succeeded" {
			return precheckPaid
		}
		if pi.State == "failed" {
			continue
		}
		readable := false
		if entry, ok := hostedEntry(pi.Provider); ok && entry.Descriptor.Capabilities.ReferenceIsPaymentID {
			readable = canRead && pi.ProviderPaymentID != nil && *pi.ProviderPaymentID != ""
		}
		if !readable {
			if pi.HostedCheckoutURL != nil && *pi.HostedCheckoutURL != "" {
				return precheckInProgress
			}
			continue
		}
		lk, lerr := reader.LookupHostedPayment(ctx, pi.OrgID, pi.Provider, *pi.ProviderPaymentID)
		if lerr != nil {
			h.logger.Warn("public_checkout_cancel: the provider did not answer; cancelling anyway",
				slog.String("payment_intent_id", pi.ID.String()), slog.String("error", lerr.Error()))
			continue
		}
		switch lk.Result.State {
		case payments.HostedPaymentSettled:
			if lk.Result.Event.Kind != payments.EventPaymentSucceeded {
				continue
			}
			if h.paymentApplier == nil {
				return precheckInProgress
			}
			src := hcheckout.ConfirmedPaymentSource{OrgID: pi.OrgID, ConfigID: lk.ConfigID, Provider: lk.Provider}
			if aerr := h.paymentApplier(ctx, src, lk.Result.Event); aerr != nil {
				h.logger.Error("public_checkout_cancel: the provider reports the order paid, applying it failed",
					slog.String("payment_intent_id", pi.ID.String()), slog.String("error", aerr.Error()))
				return precheckInProgress
			}
			return precheckPaid
		case payments.HostedPaymentPending:
			return precheckInProgress
		}
	}
	return precheckCancel
}
