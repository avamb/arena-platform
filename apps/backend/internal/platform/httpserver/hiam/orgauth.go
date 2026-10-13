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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// actorIsMemberOfOrg reports whether the authenticated actor holds an active
// membership in orgID. Returns (false, nil) for unauthenticated/non-member
// actors; (false, err) only on infrastructure failure.
func actorIsMemberOfOrg(ctx context.Context, q *gen.Queries, orgID uuid.UUID) (bool, error) {
	// Organization API keys (spec §13.1) hold no org_memberships row: their
	// reach is exactly api_keys.org_id. Decide here and never touch q.
	if isService, allowed := auth.ServiceActorInOrg(ctx, orgID.String()); isService {
		return allowed, nil
	}
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
	if !hasOrgMembershipRow(memberships, orgID) {
		return false, nil
	}
	// A membership is not enough: the gate's permission must be held in
	// THIS organization, not through a role of another one.
	return permissions.AllowedInOrg(ctx, orgID)
}

// platformRoles never count from a membership row: the checker resolves them
// only from the global, org-less user_roles (GetActiveRolesForUserInOrg
// leaves them out of the membership side).
var platformRoles = map[string]bool{"platform_superadmin": true, "platform_operator": true}

// hasOrgMembershipRow reports whether the active membership rows hold one in
// orgID that counts. A row carrying only a platform role grants nothing in
// the checker, so it is not a membership here either.
func hasOrgMembershipRow(rows []gen.MembershipRow, orgID uuid.UUID) bool {
	for _, m := range rows {
		if m.OrgID == orgID && !platformRoles[m.Role] {
			return true
		}
	}
	return false
}

// requireOrgMembership verifies the authenticated actor is an active member of
// orgID. Writes HTTP error and returns false when not a member.
// When membershipQueries is nil the check fails-closed (returns false with 403)
// to prevent accidental bypass of org-isolation enforcement.
func (h *Handler) requireOrgMembership(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) bool {
	if auth.HasSuperadminOrgAccess(r.Context()) {
		if _, ok := httputil.RequireAdminReason(w, r); !ok {
			return false
		}
		return true
	}
	if isService, allowed := httputil.ServiceActorDecision(w, r, orgID); isService {
		return allowed
	}
	if h.membershipQueries == nil {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"org.access_denied", "caller is not a member of this organization", r,
		))
		return false
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
