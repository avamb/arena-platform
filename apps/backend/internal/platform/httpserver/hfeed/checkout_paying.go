// checkout_paying.go — POST /v1/public/checkout/{checkout_token}/paying
// (spec 67 section 4.8): "the payment is in progress".
//
// The widget sends it right before stripe.confirm (before 3-D Secure). The
// hold of the click then gets paying_until = now + paying ONCE, and the order
// does not expire before it (orders.expires_at moved to the later of the two).
// While paying_until has not passed, the expiry job does not close the
// payment page (section 5.3). A repeat changes nothing and answers 200. After
// the expiry job began closing the page (closing_started_at), or once the
// hold's time is over, it is 409 hold.expired.
//
// One transaction, locks in the order of the buyer cancel: the checkout row,
// then the order, then the reservation.
package hfeed

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdpolicy"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// HandlePublicCheckoutPaying serves POST /v1/public/checkout/{checkout_token}/paying.
func (h *Handler) HandlePublicCheckoutPaying(w http.ResponseWriter, r *http.Request) {
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
	cs, err := h.checkoutQueries.GetCheckoutSessionByToken(r.Context(), checkoutToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"checkout.not_found", "We cannot find this checkout. Open the ticket page again.", r,
			))
			return
		}
		h.logger.Error("public_checkout_paying: session lookup failed", slog.String("error", err.Error()))
		h.writePayingUnavailable(w, r)
		return
	}

	res, err := h.markPayingTx(r, cs)
	if err != nil {
		h.logger.Error("public_checkout_paying: mark failed",
			slog.String("checkout_session_id", cs.ID.String()), slog.String("error", err.Error()))
		h.writePayingUnavailable(w, r)
		return
	}
	switch res.outcome {
	case payingPaid:
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"checkout_token": checkoutToken,
			"status":         publicStatusPaid,
		})
		return
	case payingClosed:
		writeHoldExpired(w, r)
		return
	}
	body := map[string]any{
		"checkout_token": checkoutToken,
		"status":         publicStatusPending,
		"paying_until":   nil,
	}
	if res.payingUntil != nil {
		body["paying_until"] = res.payingUntil.UTC().Format(time.RFC3339)
	}
	if res.expiresAt != nil {
		body["expires_at"] = res.expiresAt.UTC().Format(time.RFC3339)
	}
	if res.outcome == payingMarked {
		h.logger.Info("public_checkout_paying: the payment is in progress",
			slog.String("checkout_session_id", cs.ID.String()))
	}
	httputil.WriteJSON(w, http.StatusOK, body)
}

type payingOutcome int

const (
	// payingMarked: paying_until was written now.
	payingMarked payingOutcome = iota
	// payingRepeat: already marked, or a checkout of the old path (no hold
	// key): nothing changed.
	payingRepeat
	// payingPaid: the order is paid; nothing to do.
	payingPaid
	// payingClosed: the order or the hold is over, or the page is being
	// closed.
	payingClosed
)

type payingResult struct {
	outcome     payingOutcome
	payingUntil *time.Time
	expiresAt   *time.Time
}

// markPayingTx writes the mark in one transaction: the checkout row, the
// order and the reservation locked in that order (the buyer cancel's).
func (h *Handler) markPayingTx(r *http.Request, cs gen.CheckoutSessionRow) (payingResult, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return payingResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txq := gen.New(tx)

	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM checkout_sessions WHERE id = $1 FOR UPDATE`, cs.ID).Scan(&state); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return payingResult{outcome: payingClosed}, nil
		}
		return payingResult{}, err
	}
	if state == "completed" {
		return payingResult{outcome: payingPaid}, nil
	}
	o, err := txq.GetOrderByCheckoutSession(ctx, cs.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return payingResult{outcome: payingClosed}, nil
	}
	if err != nil {
		return payingResult{}, err
	}
	ord, err := txq.LockOrderForUpdate(ctx, o.ID, o.OrgID)
	if err != nil {
		return payingResult{}, err
	}
	if ord.Status == ordering.StatusPaid {
		return payingResult{outcome: payingPaid}, nil
	}
	if ord.Status != ordering.StatusPendingPayment || state != "pricing_confirmed" {
		return payingResult{outcome: payingClosed}, nil
	}
	hold, err := txq.LockHoldCheckout(ctx, cs.ReservationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return payingResult{outcome: payingClosed}, nil
	}
	if err != nil {
		return payingResult{}, err
	}
	if !hold.HasHoldToken {
		// The old path has no hold to keep: nothing to write.
		return payingResult{outcome: payingRepeat, expiresAt: ord.ExpiresAt}, nil
	}
	if hold.PayingUntil != nil {
		if hold.ClosingStartedAt != nil {
			return payingResult{outcome: payingClosed}, nil
		}
		return payingResult{outcome: payingRepeat, payingUntil: hold.PayingUntil, expiresAt: ord.ExpiresAt}, nil
	}
	now := time.Now().UTC()
	if !holdLive(hold, now) {
		return payingResult{outcome: payingClosed}, nil
	}
	policy := holdpolicy.FromSnapshot(hold.HoldPolicy)
	until := now.Add(policy.Paying)
	marked, err := txq.SetHoldPayingUntil(ctx, hold.ID, until)
	if errors.Is(err, pgx.ErrNoRows) {
		return payingResult{outcome: payingClosed}, nil
	}
	if err != nil {
		return payingResult{}, err
	}
	// The order must not expire before the hold's payment mark (section 5.2).
	if _, err := txq.ExtendPendingOrderExpiry(ctx, ord.ID, until); err != nil {
		return payingResult{}, err
	}
	orderExp := until
	if ord.ExpiresAt != nil && ord.ExpiresAt.After(until) {
		orderExp = *ord.ExpiresAt
	}
	if err := tx.Commit(ctx); err != nil {
		return payingResult{}, err
	}
	return payingResult{outcome: payingMarked, payingUntil: marked.PayingUntil, expiresAt: &orderExp}, nil
}

func (h *Handler) writePayingUnavailable(w http.ResponseWriter, r *http.Request) {
	// 503, never 502, on this Cloudflare-fronted public route.
	httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
		"checkout.unavailable", "We could not save the payment mark. Try again in a minute.", r,
	))
}
