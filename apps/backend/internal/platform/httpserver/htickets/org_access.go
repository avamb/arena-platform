// org_access.go — organization isolation for the ticket routes that learn the
// organization from the row they load instead of from an {org_id} in the path:
// GET /v1/checkout/{id}/tickets, GET /v1/tickets/{id}/credential and the
// support-console routes under /v1/admin/tickets/{id}.
//
// The decision itself lives in the server (membership rows, API-key org,
// superadmin) and is injected here, exactly like hcheckout.RowOrgAccess, so the
// handlers stay free of the god-object.
package htickets

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// RowOrgAccess reports whether the caller may act on a row owned by orgID.
// write is true for a mutation (a superadmin then needs X-Admin-Reason). When
// it refuses it writes the response itself — the route's own 404
// (notFoundCode / notFoundMessage) for a caller outside the organization,
// never a 403, so an id cannot be probed — and returns false.
type RowOrgAccess func(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, write bool, notFoundCode, notFoundMessage string) bool

// WithRowOrgAccess wires the organization guard. Returns the receiver for
// chaining.
func (h *Handler) WithRowOrgAccess(fn RowOrgAccess) *Handler {
	h.rowOrgAccess = fn
	return h
}

// rowOrgAllowed applies the guard. Without one the route fails closed.
func (h *Handler) rowOrgAllowed(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, write bool, notFoundCode, notFoundMessage string) bool {
	if h.rowOrgAccess == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.org_access_unavailable", "organization access guard is not configured", r,
		))
		return false
	}
	return h.rowOrgAccess(w, r, orgID, write, notFoundCode, notFoundMessage)
}

// ticketOrgAllowed resolves the organization of ticketID (through its session's
// event) and applies the guard. A missing ticket answers the same 404 as a
// foreign one.
func (h *Handler) ticketOrgAllowed(w http.ResponseWriter, r *http.Request, ticketID uuid.UUID, write bool, notFoundCode, notFoundMessage string) bool {
	if h.ticketQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return false
	}
	ctx := r.Context()
	notFound := func() bool {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(notFoundCode, notFoundMessage, r))
		return false
	}
	t, err := h.ticketQueries.GetTicketByID(ctx, ticketID)
	if err == nil {
		row, oerr := h.ticketQueries.GetSessionOrgContext(ctx, t.SessionID)
		if oerr == nil {
			return h.rowOrgAllowed(w, r, row.OrgID, write, notFoundCode, notFoundMessage)
		}
		err = oerr
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	h.logger.Error("tickets: organization lookup failed",
		slog.String("ticket_id", ticketID.String()),
		slog.String("error", err.Error()),
	)
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"ticket.lookup_failed", "failed to resolve the ticket's organization", r,
	))
	return false
}

// checkoutOrgAllowed resolves the organization of a checkout session and
// applies the guard. A missing checkout answers the same 404 as a foreign one.
func (h *Handler) checkoutOrgAllowed(w http.ResponseWriter, r *http.Request, checkoutID uuid.UUID, notFoundCode, notFoundMessage string) bool {
	cs, err := h.ticketQueries.GetCheckoutSessionByID(r.Context(), checkoutID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(notFoundCode, notFoundMessage, r))
			return false
		}
		h.logger.Error("tickets: checkout lookup failed",
			slog.String("checkout_session_id", checkoutID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"ticket.list_failed", "failed to retrieve tickets", r,
		))
		return false
	}
	return h.rowOrgAllowed(w, r, cs.OrgID, false, notFoundCode, notFoundMessage)
}
