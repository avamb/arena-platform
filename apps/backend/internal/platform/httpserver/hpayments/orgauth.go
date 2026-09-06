// orgauth.go provides org-scoped membership enforcement for the hpayments
// handlers (PR2-01: cross-tenant authorization bypass fix).
package hpayments

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
	ctx := r.Context()
	member, err := actorIsMemberOfOrg(ctx, h.membershipQueries, orgID)
	if err != nil {
		h.logger.Error("payment_config: org membership check failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"payment_config.membership_check_failed", "failed to verify org membership", r,
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
