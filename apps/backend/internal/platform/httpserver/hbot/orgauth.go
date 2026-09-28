package hbot

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// requireOrgMembership is the tenant guard of the org-scoped route, the same
// rule hapikeys applies: a platform superadmin passes with an X-Admin-Reason
// (and is told so through the returned flag, which unlocks the deep link in
// the response), an organization API key only inside its own organization,
// a user only where they hold a membership. A denial has already been
// written to w when ok is false.
func (h *Handler) requireOrgMembership(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) (superadmin, ok bool) {
	if auth.HasSuperadminOrgAccess(r.Context()) {
		if _, reasonOK := httputil.RequireAdminReason(w, r); !reasonOK {
			return false, false
		}
		return true, true
	}
	if isService, allowed := httputil.ServiceActorDecision(w, r, orgID); isService {
		return false, allowed
	}
	if h.membershipQueries == nil {
		return false, true
	}
	member, err := actorIsMemberOfOrg(r.Context(), h.membershipQueries, orgID)
	if err != nil {
		h.logger.Error("hbot: org membership check failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"bot.membership_check_failed", "failed to verify org membership", r,
		))
		return false, false
	}
	if !member {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"org.access_denied", "caller is not a member of this organization", r,
		))
		return false, false
	}
	return false, true
}

func actorIsMemberOfOrg(ctx context.Context, q *gen.Queries, orgID uuid.UUID) (bool, error) {
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || actor.ID == "" {
		return false, nil
	}
	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		return false, nil
	}
	memberships, err := q.ListMembershipsByUser(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, m := range memberships {
		if m.OrgID == orgID && m.Status == "active" {
			return true, nil
		}
	}
	return false, nil
}
