// orgauth.go provides org-scoped membership enforcement for the hiam handlers
// (PR2-01: cross-tenant authorization bypass fix).
package hiam

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// actorIsMemberOfOrg reports whether the authenticated actor holds an active
// membership in orgID. Returns (false, nil) for unauthenticated/non-member
// actors; (false, err) only on infrastructure failure.
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
		if m.OrgID == orgID {
			return true, nil
		}
	}
	return false, nil
}

// requireOrgMembership verifies the authenticated actor is an active member of
// orgID. Writes HTTP error and returns false when not a member.
// When membershipQueries is nil the check is skipped and true is returned.
func (h *Handler) requireOrgMembership(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) bool {
	if h.membershipQueries == nil {
		return true
	}
	ctx := r.Context()
	member, err := actorIsMemberOfOrg(ctx, h.membershipQueries, orgID)
	if err != nil {
		h.logger.Error("org: membership check failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"org.membership_check_failed", "failed to verify org membership", r,
		))
		return false
	}
	if !member {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"org.access_denied", "caller is not a member of this organization", r,
		))
		return false
	}
	return true
}
