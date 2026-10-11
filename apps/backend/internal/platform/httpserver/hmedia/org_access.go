// org_access.go — organization isolation for /v1/media. The routes have no
// {org_id} in the path: the organization is the org_id form value of an
// upload, or the media object's own org_id afterwards.
//
// The decision lives in the server (membership rows, API-key org,
// superadmin) and is injected here, like hcheckout.RowOrgAccess.
package hmedia

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// RowOrgAccess reports whether the caller may act on a row owned by orgID.
// When it refuses it writes the route's own 404 (notFoundCode /
// notFoundMessage) and returns false.
type RowOrgAccess func(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, write bool, notFoundCode, notFoundMessage string) bool

// WithRowOrgAccess wires the organization guard. Returns the receiver for
// chaining.
func (h *Handler) WithRowOrgAccess(fn RowOrgAccess) *Handler {
	h.rowOrgAccess = fn
	return h
}

// mediaOrgAllowed decides access to a media object owned by orgID. An object
// without an organization (an upload that named none) stays readable as
// before, but only a platform superadmin may delete it, since nobody else
// can be shown to own it. Without a guard the route fails closed.
func (h *Handler) mediaOrgAllowed(w http.ResponseWriter, r *http.Request, orgID *uuid.UUID, write bool) bool {
	if orgID == nil {
		if !write || auth.HasSuperadminOrgAccess(r.Context()) {
			return true
		}
		writeMediaNotFound(w, r)
		return false
	}
	if h.rowOrgAccess == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.org_access_unavailable", "organization access guard is not configured", r,
		))
		return false
	}
	return h.rowOrgAccess(w, r, *orgID, write, "media.not_found", "media object not found")
}

func writeMediaNotFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"media.not_found", "media object not found", r,
	))
}
