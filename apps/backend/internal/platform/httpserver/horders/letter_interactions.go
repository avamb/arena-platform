package horders

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// HandleLetterInteractions serves
// GET /v1/organizations/{org_id}/orders/{order_id}/letter-interactions
// (U-7): every open, proxy open and click Brevo reported on the order's
// letters, with the reader's user agent. It is dispute evidence and personal
// data; this route is the only place the user agent is read, and none of it
// is logged here. The order is loaded inside the path organization, so an
// order of another organization is the route's own 404 orders.not_found. An
// organization API key is refused (403
// orders.letter_interactions_api_key_forbidden), like the consents route:
// the data goes to people of the organization and to the platform operator,
// never to an integration.
func (h *Handler) HandleLetterInteractions(w http.ResponseWriter, r *http.Request) {
	if actor, ok := auth.ActorFromContext(r.Context()); ok && actor.IsService() {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"orders.letter_interactions_api_key_forbidden",
			"an API key cannot read the reader's browser data of a letter; a member of the organization reads it signed in",
			r,
		))
		return
	}
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
		h.logger.Error("horders: letter interactions order lookup failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to load order", r))
		return
	}
	db := h.queries.DB()
	if db == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "orders store not configured", r))
		return
	}
	letters, err := emailstatus.OrderLetterInteractions(ctx, db, order.ID, orgID)
	if err != nil {
		// The error text holds no row data; the order id is not personal.
		h.logger.Error("horders: letter interactions lookup failed",
			slog.String("order_id", orderID.String()), slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to load the letter interactions", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"order_id": order.ID.String(),
		"letters":  letters,
	})
}
