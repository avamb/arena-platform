// orgauth.go provides org-scoped membership enforcement for the himports
// handlers, mirroring hapikeys/orgauth.go.
package himports

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
	// Organization API keys (spec §13.1) hold no org_memberships row — their
	// reach is exactly api_keys.org_id. The lampyris-ops site plugin drives
	// this import under such a key (spec §13.4), so the service-actor branch
	// is the PRIMARY production path here, not an edge case.
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
	for _, m := range memberships {
		if m.OrgID == orgID {
			return true, nil
		}
	}
	return false, nil
}

// requireOrgMembership verifies that the authenticated actor may act on
// orgID. On failure it writes the appropriate HTTP error and returns false;
// the caller must stop handler execution when false is returned. When
// membershipQueries is nil (test environments without the field wired), the
// check is skipped and true is returned.
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
		return true
	}
	member, err := actorIsMemberOfOrg(r.Context(), h.membershipQueries, orgID)
	if err != nil {
		h.logger.Error("import: org membership check failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"import.membership_check_failed", "failed to verify org membership", r,
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
