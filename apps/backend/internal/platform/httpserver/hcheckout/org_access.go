// org_access.go — organization isolation for the routes of this package
// that learn the organization from the row they load instead of from an
// {org_id} in the path: /v1/refunds/{id} (read, approve, reject),
// POST /v1/refunds (the payment intent named in the body),
// /v1/payment-intents/{id} (read, transition) and POST /v1/payment-intents
// (the org_id named in the body).
//
// Until 2026-10-09 (PAY-00, spec 36 §8) these routes checked only a scope
// such as refund.create, which every owner and every organization API key
// may hold: an organizer could read, create and approve refunds on another
// organization's payment by UUID. The decision itself lives in the server
// (membership rows, API-key org, superadmin with X-Admin-Reason for a
// write) and is injected here, so the handlers stay free of the god-object.
package hcheckout

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// RowOrgAccess reports whether the caller may act on a row owned by orgID.
// write is true for a mutation (a superadmin then needs X-Admin-Reason, as on
// every org-scoped override route). When it refuses it writes the response
// itself — the route's own 404 (notFoundCode / notFoundMessage) for a caller
// outside the organization, never a 403, so an id cannot be probed — and
// returns false.
type RowOrgAccess func(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, write bool, notFoundCode, notFoundMessage string) bool

// WithRowOrgAccess wires the organization guard. Returns the receiver for
// chaining.
func (h *Handler) WithRowOrgAccess(fn RowOrgAccess) *Handler {
	h.rowOrgAccess = fn
	return h
}

// rowOrgAllowed applies the guard. Without one the route fails closed: a
// handler built without the server's wiring must not hand out another
// organization's money rows.
func (h *Handler) rowOrgAllowed(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, write bool, notFoundCode, notFoundMessage string) bool {
	if h.rowOrgAccess == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.org_access_unavailable", "organization access guard is not configured", r,
		))
		return false
	}
	return h.rowOrgAccess(w, r, orgID, write, notFoundCode, notFoundMessage)
}

// checkoutOrgAllowed applies the guard to a checkout session row. A refusal
// is the route's own 404, so a foreign id reads like a missing one.
func (h *Handler) checkoutOrgAllowed(w http.ResponseWriter, r *http.Request, id uuid.UUID, write bool) bool {
	if h.checkoutQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return false
	}
	cs, err := h.checkoutQueries.GetCheckoutSessionByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"checkout.not_found", "checkout session not found", r,
			))
			return false
		}
		h.logger.Error("checkout: org lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.get_failed", "failed to retrieve checkout session", r,
		))
		return false
	}
	return h.rowOrgAllowed(w, r, cs.OrgID, write, "checkout.not_found", "checkout session not found")
}

// reservationOrgAllowed applies the guard to a reservation row.
func (h *Handler) reservationOrgAllowed(w http.ResponseWriter, r *http.Request, res gen.ReservationRow, write bool) bool {
	return h.rowOrgAllowed(w, r, res.OrgID, write, "reservation.not_found", "reservation not found")
}

// sessionAndChannelInOrg proves the session and the sales channel named in a
// request body both belong to orgID. A mismatch is the route's own 404.
func (h *Handler) sessionAndChannelInOrg(w http.ResponseWriter, r *http.Request, sessionID, channelID, orgID uuid.UUID) bool {
	if h.reservationQueries == nil || h.channelQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return false
	}
	sc, err := h.reservationQueries.GetSessionOrgContext(r.Context(), sessionID)
	if err == nil && sc.OrgID != orgID {
		err = pgx.ErrNoRows
	}
	if err == nil {
		_, err = h.channelQueries.GetSalesChannelByID(r.Context(), channelID, orgID)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"reservation.not_found", "session or channel not found", r,
			))
			return false
		}
		h.logger.Error("reservation: org lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.create_failed", "failed to create reservation", r,
		))
		return false
	}
	return true
}

// reservationInOrgForCheckout proves the reservation a checkout is started
// for is the named organization's own, on the named channel.
func (h *Handler) reservationInOrgForCheckout(w http.ResponseWriter, r *http.Request, reservationID, orgID, channelID uuid.UUID) bool {
	if h.reservationQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return false
	}
	res, err := h.reservationQueries.GetReservationByID(r.Context(), reservationID)
	if err == nil && (res.OrgID != orgID || res.ChannelID != channelID) {
		err = pgx.ErrNoRows
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"reservation.not_found", "reservation not found", r,
			))
			return false
		}
		h.logger.Error("checkout: start reservation lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"checkout.start_failed", "failed to create checkout session", r,
		))
		return false
	}
	return true
}
