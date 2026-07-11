// authz.go resolves the authenticated actor's organization scope for the
// seating-plan mutation endpoints.
//
// The seating-plan routes carry no {org_id} path segment (unlike the
// hbankaccounts / hpayments surfaces, which scope every query by the
// path-supplied org), and the foundation-milestone JWT carries no org
// claim on these routes — auth.Middleware attaches only the Actor
// (subject + roles). The canonical "which organizations may this actor
// act for?" source is therefore the memberships table, exactly as
// GET /v1/me (feature #211) resolves available_scopes: an actor may act
// for every org it holds an ACTIVE membership in.
//
// Guardrail #13 (09_autoforge/00_AGENT_GUARDRAILS.md): modifying a
// seating plan owned by another organization is forbidden — callers must
// fork instead. The helpers here back the enforcement of that rule in
// the create / update / fork handlers.
package hseating

import (
	"context"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// actorIsMemberOfOrg reports whether the authenticated actor holds an
// active membership in orgID. An unauthenticated request, an actor whose
// subject is not a UUID, or an actor with no matching membership all
// resolve to false — the caller decides whether that surfaces as 403
// (create / fork target org) or 404 (mutating an existing plan, where
// existence must not leak). A non-nil error is an infrastructure failure
// and MUST surface as a 5xx, never as an authorization decision.
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
