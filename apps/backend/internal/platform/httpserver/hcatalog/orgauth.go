// orgauth.go provides org-scoped membership enforcement for the hcatalog
// handlers (PR2-01: cross-tenant authorization bypass fix).
package hcatalog

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
// orgID using h.membershipQueries. The q parameter is accepted for the
// nil-guard: when the caller's own query handle is nil, the membership check
// is skipped (503 will be returned by the main queries nil-guard upstream).
// When h.membershipQueries is nil the check fails-closed (returns false with
// 403) to prevent accidental bypass of org-isolation enforcement.
func (h *Handler) requireOrgMembership(w http.ResponseWriter, r *http.Request, q *gen.Queries, orgID uuid.UUID) bool {
	// Fail-closed: membershipQueries must be wired for org-scoped routes.
	if h.membershipQueries == nil {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"org.access_denied", "caller is not a member of this organization", r,
		))
		return false
	}
	// If the domain queries are nil the handler's own nil-guard fires with 503.
	if q == nil {
		return true
	}
	ctx := r.Context()
	member, err := actorIsMemberOfOrg(ctx, h.membershipQueries, orgID)
	if err != nil {
		h.logger.Error("catalog: org membership check failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"catalog.membership_check_failed", "failed to verify org membership", r,
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
