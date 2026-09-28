// Package orgread answers "may the caller read this organization's data?" for
// the routes that learn the organization from the row they load instead of
// from an {org_id} in the path: GET /v1/events/{id}, the cross-org GET
// /v1/events, /v1/events/{event_id}/publications and /v1/events/{event_id}/report.
//
// A platform superadmin reads every organization — a read needs no
// admin-reason header, unlike the org-scoped override routes. An organization
// API key reads only api_keys.org_id. A user reads the organizations they are
// a member of; the memberships are fetched once per Reader.
//
// Until 2026-09-28 those routes checked only a scope such as `event.read`,
// which every organization API key carries: any organizer could read another
// organizer's event, its sales report and its publications by UUID, and
// publish or unpublish it. A caller that may not read an organization gets
// the route's own 404, never 403, so an id cannot be probed.
package orgread

import (
	"context"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// Reader decides per organization; build one per request.
type Reader struct {
	ctx        context.Context
	q          *gen.Queries
	superadmin bool
	loaded     bool
	members    map[uuid.UUID]bool
}

// New returns a Reader for the request context. q may be nil: a user is then
// a member of nothing, while superadmins and API keys are decided from the
// context alone.
func New(ctx context.Context, q *gen.Queries) *Reader {
	return &Reader{ctx: ctx, q: q, superadmin: auth.HasSuperadminOrgAccess(ctx)}
}

// Can reports whether the caller may read orgID. An error is an
// infrastructure failure, never a denial.
func (r *Reader) Can(orgID uuid.UUID) (bool, error) {
	if r.superadmin {
		return true, nil
	}
	if isService, allowed := auth.ServiceActorInOrg(r.ctx, orgID.String()); isService {
		return allowed, nil
	}
	if !r.loaded {
		members, err := r.load()
		if err != nil {
			return false, err
		}
		r.members, r.loaded = members, true
	}
	return r.members[orgID], nil
}

func (r *Reader) load() (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	actor, ok := auth.ActorFromContext(r.ctx)
	if !ok || actor.ID == "" || r.q == nil {
		return out, nil
	}
	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		return out, nil
	}
	memberships, err := r.q.ListMembershipsByUser(r.ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, m := range memberships {
		out[m.OrgID] = true
	}
	return out, nil
}
