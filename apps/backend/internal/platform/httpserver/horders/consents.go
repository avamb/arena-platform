package horders

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// orderConsent is one buyer consent of an order (P2, migration 0155) as the
// organization reads it: dispute evidence, including the buyer's IP address
// and browser string at purchase.
type orderConsent struct {
	ID                    string  `json:"id"`
	CheckoutSessionID     string  `json:"checkout_session_id"`
	Kind                  string  `json:"kind"`
	TermsVersion          string  `json:"terms_version"`
	Locale                *string `json:"locale"`
	AcceptedAt            string  `json:"accepted_at"`
	ClientIP              *string `json:"client_ip"`
	UserAgent             *string `json:"user_agent"`
	WithdrawalNoticeShown bool    `json:"withdrawal_notice_shown"`
}

func orderConsentFromRow(c gen.CheckoutConsentRow) orderConsent {
	return orderConsent{
		ID:                    c.ID.String(),
		CheckoutSessionID:     c.CheckoutSessionID.String(),
		Kind:                  c.Kind,
		TermsVersion:          c.TermsVersion,
		Locale:                c.Locale,
		AcceptedAt:            c.AcceptedAt.UTC().Format(time.RFC3339),
		ClientIP:              c.ClientIP,
		UserAgent:             c.UserAgent,
		WithdrawalNoticeShown: c.WithdrawalNoticeShown,
	}
}

// HandleConsents serves GET /v1/organizations/{org_id}/orders/{order_id}/consents
// (P2): the consents the buyer gave at checkout. The order is loaded inside
// the path organization, so an order of another organization is the route's
// own 404 orders.not_found. The answer carries the buyer's IP address and
// user agent; this route is the only place they are read, and neither is
// logged here.
func (h *Handler) HandleConsents(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "orders store not configured", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	orderID, ok := httputil.UUIDPathParam(w, r, "order_id")
	if !ok {
		return
	}
	ctx := r.Context()
	order, err := h.queries.GetOrderByID(ctx, orderID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound,
				httputil.ErrorEnvelope("orders.not_found", "order not found", r))
			return
		}
		h.logger.Error("horders: consents order lookup failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to load order", r))
		return
	}
	rows, err := h.queries.ListCheckoutConsentsBySession(ctx, orgID, order.CheckoutSessionID)
	if err != nil {
		h.logger.Error("horders: consents lookup failed",
			slog.String("order_id", orderID.String()), slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to load the consents", r))
		return
	}
	out := make([]orderConsent, 0, len(rows))
	for _, c := range rows {
		out = append(out, orderConsentFromRow(c))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"order_id": order.ID.String(),
		"consents": out,
	})
}
