// last_owner.go keeps an organization from losing its last owner (U-4).
//
// An owner is an active memberships row with role org_admin whose account is
// not deactivated. Every route that can take that row away (revoke, change of
// role, admin deactivate) runs in one transaction that takes LockOrgOwners
// FIRST and counts the other owners under it, so two owners removing each
// other at the same moment cannot both succeed. The platform superadmin is not
// exempt: they appoint another owner first.
package hiam

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ownerRole is the membership role of an organization's owner.
const ownerRole = "org_admin"

// lastOwnerCode is the stable error code of every refusal; clients map it to
// their own text.
const lastOwnerCode = "membership.last_owner"

// lastOwnerAfterRemoval reports whether taking userID's owner membership away
// would leave the organization without an active owner. The caller holds
// LockOrgOwners on the same transaction.
func lastOwnerAfterRemoval(ctx context.Context, q *gen.Queries, orgID, userID uuid.UUID) (bool, error) {
	others, err := q.CountOtherActiveOrgAdmins(ctx, orgID, userID)
	if err != nil {
		return false, err
	}
	return others == 0, nil
}

// writeLastOwnerRefused answers 409 membership.last_owner.
func writeLastOwnerRefused(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
		lastOwnerCode,
		"this is the organization's last owner: appoint another owner first", r,
	))
}
