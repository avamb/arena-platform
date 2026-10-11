// org_access.go - organization isolation for the invoice routes that learn the
// organization from the row they load instead of from an {org_id} in the path:
// GET /v1/billing/invoices/{id}, POST /v1/billing/invoices/{id}/issue|pay|void
// and POST /v1/billing/stripe/push-invoice/{id}.
//
// The decision itself lives in the server (membership rows, API-key org,
// superadmin) and is injected here, exactly like htickets.RowOrgAccess, so the
// handlers stay free of the god-object.
package hbilling

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// RowOrgAccess reports whether the caller may act on a row owned by orgID.
// write is true for a mutation (a superadmin then needs X-Admin-Reason). When
// it refuses it writes the response itself - the route's own 404
// (notFoundCode / notFoundMessage) for a caller outside the organization,
// never a 403, so an id cannot be probed - and returns false.
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

// invoiceOrgAllowed applies the guard to a loaded invoice's organization.
func (h *Handler) invoiceOrgAllowed(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, write bool) bool {
	return h.rowOrgAllowed(w, r, orgID, write, "billing.not_found", "invoice not found")
}
