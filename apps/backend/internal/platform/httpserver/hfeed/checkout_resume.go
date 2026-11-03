// checkout_resume.go — GET /v1/public/checkout/{checkout_token}/resume, the
// link of the unpaid-order reminder (spec 51-1 section 3.2).
//
// The letter never carries the provider's page itself: that page dies with
// the payment window, while a letter is read later. This route decides at
// click time:
//
//   - the order is unpaid and its payment page still open → 303 to that page;
//   - anything else (paid, expired, cancelled, no payment page) → 303 to the
//     buyer's own page with ?checkout_token=, where the widget shows the
//     order and, when the places are still free, offers to take them again.
//
// An unknown token goes to the fallback tickets page: a printed or forwarded
// letter must never end on a JSON error.
package hfeed

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// HandleCheckoutResume serves GET /v1/public/checkout/{checkout_token}/resume.
func (h *Handler) HandleCheckoutResume(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	token := strings.TrimSpace(chi.URLParam(r, "checkout_token"))
	if !h.enforceRateLimit(w, r, "checkout.rate_limited", h.rl.CheckCheckoutToken, token) {
		return
	}
	fallback := func() {
		if base, err := h.returnURLPolicy.Resolve(""); err == nil && base != "" {
			http.Redirect(w, r, base, http.StatusSeeOther) //nolint:gosec // G710: the configured fallback page
			return
		}
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"checkout.link_invalid", "This link is no longer valid. Open the ticket page again.", r))
	}
	if !returnTokenPattern.MatchString(token) || h.checkoutQueries == nil {
		fallback()
		return
	}
	ctx := r.Context()
	cs, err := h.checkoutQueries.GetCheckoutSessionByToken(ctx, token)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Error("checkout_resume: session lookup failed", slog.String("error", err.Error()))
		}
		fallback()
		return
	}
	var (
		returnURL string
		status    string
		expires   *time.Time
	)
	if err := h.checkoutQueries.DB().QueryRow(ctx, `
		SELECT COALESCE(cs.return_url, ''), COALESCE(o.status, ''), o.expires_at
		FROM   checkout_sessions cs LEFT JOIN orders o ON o.checkout_session_id = cs.id
		WHERE  cs.id = $1`, cs.ID).Scan(&returnURL, &status, &expires); err != nil {
		h.logger.Error("checkout_resume: order lookup failed", slog.String("error", err.Error()))
		fallback()
		return
	}
	if status == "pending_payment" && expires != nil && time.Now().Before(*expires) {
		if pay := h.pendingPaymentURL(ctx, cs.ID); pay != "" {
			h.logger.Info("checkout_resume: buyer back to the payment page", slog.String("checkout_session_id", cs.ID.String()))
			http.Redirect(w, r, pay, http.StatusSeeOther) //nolint:gosec // G710: the provider page arena created for this checkout
			return
		}
	}
	base, err := h.returnURLPolicy.Resolve(returnURL)
	if err != nil || base == "" {
		fallback()
		return
	}
	// base came out of ReturnURLPolicy.Resolve: its origin is on the allow-list
	// or is the configured fallback, never a raw value.
	http.Redirect(w, r, ReturnURLWithToken(base, token), http.StatusSeeOther) //nolint:gosec // G710: allow-list validated above
}
