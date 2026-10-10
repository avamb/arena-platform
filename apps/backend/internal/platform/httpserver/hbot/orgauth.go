package hbot

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/orgperm"
)

// requireOrgMembership is the tenant guard of the org-scoped route, the same
// rule hapikeys applies: a platform superadmin passes with an X-Admin-Reason
// (and is told so through the returned flag, which unlocks the deep link in
// the response), an organization API key only inside its own organization,
// a user only where they hold a membership. A denial has already been
// written to w when ok is false.
//
// SEC-1: a user must hold permission through a membership of THIS
// organization (orgperm), not merely be a member of it while the permission
// comes from another organization they own — the invitation routes create
// and remove memberships. Without a membership handle the guard fails
// closed.
func (h *Handler) requireOrgMembership(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, permission string) (superadmin, ok bool) {
	if auth.HasSuperadminOrgAccess(r.Context()) {
		if _, reasonOK := httputil.RequireAdminReason(w, r); !reasonOK {
			return false, false
		}
		return true, true
	}
	if isService, allowed := httputil.ServiceActorDecision(w, r, orgID); isService {
		return false, allowed
	}
	denied := func() (bool, bool) {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"org.access_denied", "caller is not a member of this organization", r,
		))
		return false, false
	}
	if h.membershipQueries == nil {
		return denied()
	}
	actor, authed := auth.ActorFromContext(r.Context())
	if !authed {
		return denied()
	}
	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		return denied()
	}
	member, allowed, err := orgperm.Check(r.Context(), h.membershipQueries, userID, orgID, permission)
	if err != nil {
		h.logger.Error("hbot: org membership check failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bot.membership_check_failed", "failed to verify org membership", r,
		))
		return false, false
	}
	if !member {
		return denied()
	}
	if !allowed {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelopeWithDetails(
			"permissions.denied", "caller lacks this permission in this organization", r,
			map[string]any{"permission": permission},
		))
		return false, false
	}
	return false, true
}
