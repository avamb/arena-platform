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
	"net/http"

	"github.com/google/uuid"

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
