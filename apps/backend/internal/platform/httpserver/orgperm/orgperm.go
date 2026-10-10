// Package orgperm answers "does this user hold permission P inside
// organization O?" from the user's OWN memberships of O.
//
// The RBAC gate in front of every route (applyAuth, permissions.DBChecker)
// resolves permissions from the union of the roles of ALL the caller's
// memberships, and requireOrgMembership only asks whether the caller is a
// member of the path organization at all. Together they let an agent of
// organization A who owns organization B act in A with B's owner
// permissions. Membership-granting routes (SEC-1) must not: they ask here.
//
// Platform roles (platform_superadmin, platform_operator) never count from a
// membership row — they are global user_roles (SEC-1).
package orgperm

import (
	"context"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Querier is the narrow surface orgperm reads; *gen.Queries satisfies it.
type Querier interface {
	ListMembershipsByUser(ctx context.Context, userID uuid.UUID) ([]gen.MembershipRow, error)
	GetPermissionsForRoles(ctx context.Context, roleNames []string) ([]string, error)
}

// globalOnly mirrors the membership branch of GetActiveRolesForUser.
var globalOnly = map[string]bool{"platform_superadmin": true, "platform_operator": true}

// Check reports whether userID is an active member of orgID (member) and
// whether one of those memberships carries permission (allowed). err is an
// infrastructure failure only.
func Check(ctx context.Context, q Querier, userID, orgID uuid.UUID, permission string) (member, allowed bool, err error) {
	rows, err := q.ListMembershipsByUser(ctx, userID)
	if err != nil {
		return false, false, err
	}
	var roles []string
	for _, m := range rows {
		if m.OrgID == orgID && m.Status == "active" && !globalOnly[m.Role] {
			roles = append(roles, m.Role)
		}
	}
	if len(roles) == 0 {
		return false, false, nil
	}
	perms, err := q.GetPermissionsForRoles(ctx, roles)
	if err != nil {
		return true, false, err
	}
	for _, p := range perms {
		if p == permission {
			return true, true, nil
		}
	}
	return true, false, nil
}
