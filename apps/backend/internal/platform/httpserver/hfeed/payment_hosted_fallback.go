// payment_hosted_fallback.go — POST /v1/public/checkout/{checkout_token}/hosted
// (spec 65 §3.5): the widget could not run the embedded form (Stripe.js did
// not load within 5 seconds, the host's CSP refused it, initialisation
// failed) and asks for the provider's hosted page on the SAME hold.
//
// Order of work, and why:
//
//  1. The embedded session is expired FIRST. If the provider refuses because
//     it is already paid or a payment on it is being processed, nothing is
//     created: the answer is 409 checkout.payment_in_progress and the widget
//     polls the status. Two open pages on one hold is how a buyer pays twice.
//  2. Only then the hosted page is created on the same hold, with its own
//     payment_intents row; the embedded row is marked superseded (its state
//     is left alone: if it is paid after all, that payment still pays the
//     order — hcheckout/embedded_payments.go).
//
// A provider refuses a page that dies sooner than its floor (Stripe: 30
// minutes), and the hosted page must never outlive the hold. So the fallback
// is only possible while the payment window left is at least that floor;
// later it answers 409 checkout.hosted_fallback_unavailable BEFORE expiring
// anything, and the buyer keeps the embedded session. Errors are 503, never
// 502 (Cloudflare replaces an origin 502, AGENTS.md).
package hfeed

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdpolicy"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Error codes of the hosted fallback.
const (
	// ErrCodeCheckoutNotPayable: the checkout is paid, expired, cancelled or
	// has no embedded payment to replace.
	ErrCodeCheckoutNotPayable = "checkout.not_payable"
	// ErrCodeHostedFallbackUnavailable: too little of the payment window is
	// left for a hosted page the provider would accept.
	ErrCodeHostedFallbackUnavailable = "checkout.hosted_fallback_unavailable"
)

// hostedFallbackFloorMargin is added to the provider's page floor so a page
// created a few seconds after the check is still accepted.
const hostedFallbackFloorMargin = 15 * time.Second

// HandlePublicCheckoutHostedFallback serves
// POST /v1/public/checkout/{checkout_token}/hosted.
func (h *Handler) HandlePublicCheckoutHostedFallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.checkoutQueries == nil || h.pool == nil {
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
		h.logger.Error("public_checkout_hosted: session lookup failed", slog.String("error", err.Error()))
		h.writeHostedFallbackUnavailable(w, r)
		return
	}
	es, ok := h.paymentStarter.(EmbeddedPaymentStarter)
	if !ok {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			ErrCodePaymentNotConfigured, "Online payment is not set up here yet. Contact the organizer.", r,
		))
		return
	}

	pc, err := h.checkoutQueries.GetCheckoutPaymentContext(ctx, cs.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		h.writeNotPayable(w, r, checkoutStatusToPublic(cs.State))
		return
	}
	if err != nil {
		h.logger.Error("public_checkout_hosted: checkout lookup failed",
			slog.String("checkout_session_id", cs.ID.String()), slog.String("error", err.Error()))
		h.writeHostedFallbackUnavailable(w, r)
		return
	}
	now := time.Now().UTC()
	if !fallbackPayable(pc, now) {
		h.writeNotPayable(w, r, h.currentPublicStatus(ctx, cs))
		return
	}

	intents, err := h.checkoutQueries.ListPaymentIntentsByCheckout(ctx, cs.ID)
	if err != nil {
		h.logger.Error("public_checkout_hosted: payment intents lookup failed",
			slog.String("checkout_session_id", cs.ID.String()), slog.String("error", err.Error()))
		h.writeHostedFallbackUnavailable(w, r)
		return
	}
	for _, pi := range intents {
		if pi.State == "succeeded" {
			h.writeNotPayable(w, r, publicStatusPaid)
			return
		}
	}
	open := newestOpenIntent(intents)
	// A retry after a partial failure: the embedded session was already
	// expired (by an earlier call of this route, or the provider's
	// checkout.session.expired webhook already failed its row with
	// session_expired) but no hosted page was recorded after it. The hold
	// and the order are still payable (fallbackPayable above), so the hosted
	// page is created now instead of answering 409 not_payable.
	alreadyExpired := false
	if open == nil {
		open = expiredEmbeddedWithoutSuccessor(intents)
		if open == nil {
			h.writeNotPayable(w, r, publicStatusPending)
			return
		}
		alreadyExpired = true
	}
	pageExpiry := pc.HoldExpiresAt.Add(-h.paymentGrace)
	// "Pay" ends with the page on the old path; on the short payment step of
	// a hold of the click it ends with the hold, and the new page gets its
	// own life from page_expires_at's rule (spec 67 section 3.3): arena closes
	// it when the hold ends.
	payUntil := pageExpiry
	var newPageExpiry time.Time
	if hw, ok := h.checkoutHoldWindow(ctx, h.checkoutQueries, cs.ID); ok && hw.short {
		if hw.row.ClosingStartedAt != nil {
			h.writeNotPayable(w, r, publicStatusPending)
			return
		}
		newPageExpiry = newHoldPageExpiry(holdpolicy.FromSnapshot(hw.row.HoldPolicy), now)
		pageExpiry = newPageExpiry
		payUntil = pc.HoldExpiresAt
	}
	if open.HostedCheckoutURL != nil && *open.HostedCheckoutURL != "" {
		// Already on a hosted page (a repeated call): the same page again.
		h.writeHostedFallbackAnswer(w, checkoutToken, *open.HostedCheckoutURL, payUntil)
		return
	}
	if !isEmbeddedIntent(*open) {
		h.writeNotPayable(w, r, publicStatusPending)
		return
	}
	if floor := time.Duration(providerPageFloor(open.Provider)) * time.Second; floor > 0 && pageExpiry.Sub(now) < floor+hostedFallbackFloorMargin {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			ErrCodeHostedFallbackUnavailable,
			"The payment page cannot be switched any more. Pay in the form on this page, or start the order again.", r,
		))
		return
	}

	// The provider calls and the writes after them must not die with the
	// buyer's connection: detached, bounded on their own.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hostedPaymentStartTimeout)
	defer cancel()

	// ── 1. Expire the embedded session first ────────────────────────────────
	sessionID := *open.ProviderPaymentID
	if alreadyExpired {
		h.logger.Info("public_checkout_hosted: the embedded session is already expired; creating the hosted page",
			slog.String("checkout_session_id", cs.ID.String()),
			slog.String("payment_intent_id", open.ID.String()))
	} else if expErr := es.ExpireEmbeddedCheckout(pctx, pc.OrgID, pc.ChannelID, open.Provider, sessionID); expErr != nil {
		if !errors.Is(expErr, payments.ErrCheckoutSessionNotExpirable) {
			h.logger.Error("public_checkout_hosted: the provider did not confirm the expiry of the embedded session",
				slog.String("checkout_session_id", cs.ID.String()),
				slog.String("payment_intent_id", open.ID.String()),
				slog.String("error", expErr.Error()))
			h.writeHostedFallbackUnavailable(w, r)
			return
		}
		// Refused: already expired is fine (a retry of this call); paid or
		// a payment being processed is not.
		sess, _, readErr := es.ReadEmbeddedCheckout(pctx, pc.OrgID, pc.ChannelID, open.Provider, sessionID)
		if readErr != nil || sess.Status != payments.EmbeddedSessionExpired {
			h.logger.Info("public_checkout_hosted: the embedded session cannot be expired; a payment is in progress",
				slog.String("checkout_session_id", cs.ID.String()),
				slog.String("payment_intent_id", open.ID.String()))
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				ErrCodePaymentInProgress,
				"Your payment is already being processed. Check the order status in a minute.", r,
			))
			return
		}
	}

	// ── 2. The hosted page on the same hold ─────────────────────────────────
	returnBase, rErr := h.returnURLPolicy.Resolve(derefOr(pc.ReturnURL))
	if rErr != nil {
		h.logger.Error("public_checkout_hosted: no page to return the buyer to",
			slog.String("checkout_session_id", cs.ID.String()), slog.String("error", rErr.Error()))
		h.writeHostedFallbackUnavailable(w, r)
		return
	}
	orderID := pc.OrderID
	productName := strings.TrimSpace(pc.EventName)
	if productName == "" {
		productName = "Tickets"
	}
	result, err := h.paymentStarter.StartHostedCheckout(pctx, HostedCheckoutRequest{
		OrgID:             pc.OrgID,
		ChannelID:         pc.ChannelID,
		CheckoutSessionID: cs.ID,
		CheckoutToken:     checkoutToken,
		OrderID:           &orderID,
		Amount:            derefInt64(pc.Total),
		Currency:          derefOr(pc.Currency),
		ProductName:       productName,
		BuyerEmail:        derefOr(pc.BuyerEmail),
		BuyerName:         derefOr(pc.BuyerName),
		BuyerPhone:        derefOr(pc.BuyerPhone),
		ReturnURL:         returnBase,
		ExpiresAtUnix:     pageExpiry.Unix(),
		BuyerLocale:       derefOr(pc.BuyerLocale),
		ExpectProvider:    open.Provider,
		IdempotencyKey:    cs.ID.String() + hostedFallbackIdempotencySuffix,
	})
	if err != nil || result.AttemptRecorded {
		errText := "the provider recorded an attempt the fallback does not handle"
		if err != nil {
			errText = err.Error()
		}
		// The hold is kept: the sweeps release it, as for an abandoned cart.
		h.logger.Error("public_checkout_hosted: the hosted page was not created",
			slog.String("checkout_session_id", cs.ID.String()), slog.String("error", errText))
		h.writeHostedFallbackUnavailable(w, r)
		return
	}

	if err := h.recordHostedFallback(pctx, pc, open.ID, result, newPageExpiry); err != nil {
		h.logger.Error("public_checkout_hosted: recording the hosted page failed; not handing it out",
			slog.String("checkout_session_id", cs.ID.String()),
			slog.String("provider_payment_id", result.SessionID),
			slog.String("error", err.Error()))
		h.writeHostedFallbackUnavailable(w, r)
		return
	}
	h.logger.Info("public_checkout_hosted: the embedded session was replaced by a hosted page",
		slog.String("checkout_session_id", cs.ID.String()),
		slog.String("superseded_payment_intent_id", open.ID.String()),
		slog.String("provider_payment_id", result.SessionID))
	h.writeHostedFallbackAnswer(w, checkoutToken, result.RedirectURL, payUntil)
}

// expiredEmbeddedWithoutSuccessor returns the checkout's NEWEST payment
// intent when it is an embedded session that failed only because its session
// expired (failure_code session_expired), or nil. Being the newest means no
// hosted page was recorded after it. A declined or otherwise failed embedded
// payment is not recovered here.
func expiredEmbeddedWithoutSuccessor(intents []gen.PaymentIntentRow) *gen.PaymentIntentRow {
	// ListPaymentIntentsByCheckout returns newest first.
	if len(intents) == 0 {
		return nil
	}
	newest := &intents[0]
	if newest.State != "failed" || newest.FailureCode == nil || *newest.FailureCode != paymentFailureSessionExpired {
		return nil
	}
	if !isEmbeddedIntent(*newest) {
		return nil
	}
	return newest
}

// paymentFailureSessionExpired is the payment_intents.failure_code the
// webhook stamps on a provider session that expired unpaid
// (hcheckout.failureCodeSessionExpired).
const paymentFailureSessionExpired = "session_expired"

// fallbackPayable reports whether the checkout can still take a payment: an
// order waiting for it, its checkout confirmed and its hold live.
func fallbackPayable(pc gen.CheckoutPaymentContextRow, now time.Time) bool {
	if pc.CheckoutState != "pricing_confirmed" || pc.OrderStatus != "pending_payment" {
		return false
	}
	if pc.ReservationState != "draft" && pc.ReservationState != "active" {
		return false
	}
	return pc.HoldExpiresAt.After(now) && pc.Total != nil && *pc.Total > 0
}

// recordHostedFallback writes the hosted page's payment intent and marks the
// embedded one superseded, in one transaction. A retry after a lost answer
// finds the same page (same idempotency key) already recorded and reuses it.
// pageExpiresAt, when set, is the page_expires_at of the new page (a hold of
// the click, spec 67 section 3.2).
func (h *Handler) recordHostedFallback(ctx context.Context, pc gen.CheckoutPaymentContextRow, supersededID uuid.UUID, result *HostedCheckoutResult, pageExpiresAt time.Time) error {
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	if _, err := q.GetPaymentIntentByProviderID(ctx, result.SessionID); errors.Is(err, pgx.ErrNoRows) {
		csID := pc.CheckoutSessionID
		pi, err := q.InsertHostedPaymentIntent(ctx, &csID, pc.OrgID, result.Provider, result.SessionID,
			derefInt64(pc.Total), derefOr(pc.Currency), result.RedirectURL)
		if err != nil {
			return err
		}
		if !pageExpiresAt.IsZero() {
			if _, err := q.SetPaymentIntentPageExpiresAt(ctx, pi.ID, pageExpiresAt); err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	}
	if _, err := q.MarkPaymentIntentSuperseded(ctx, supersededID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (h *Handler) writeHostedFallbackAnswer(w http.ResponseWriter, token, redirectURL string, pageExpiry time.Time) {
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"checkout_token":     token,
		"redirect_url":       redirectURL,
		"payment_expires_at": pageExpiry.UTC().Format(time.RFC3339),
	})
}

func (h *Handler) writeNotPayable(w http.ResponseWriter, r *http.Request, status string) {
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
		ErrCodeCheckoutNotPayable, "This order cannot be paid any more. Check the order status.", r,
		map[string]any{"status": status},
	))
}

func (h *Handler) writeHostedFallbackUnavailable(w http.ResponseWriter, r *http.Request) {
	// 503, never 502, on this Cloudflare-fronted public route.
	httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
		ErrCodePaymentStartFailed, "The payment service could not open the payment page. Try again in a minute.", r,
	))
}
