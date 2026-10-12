// org_scope.go — permissions are decided PER ORGANIZATION (PERM-ORG).
//
// DBChecker.Check used to resolve a caller's permissions from the roles of
// ALL their memberships, so an agent of organization A who is a manager of
// organization B passed `refund.create` on A's orders. A request that names
// an organization — through the `{org_id}` of its path, or through the
// organization of the row a flat route loaded — now carries that
// organization in its context, and the checker reads only the caller's
// roles IN that organization plus the global (org-less) user_roles.

package permissions

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

type orgScopeKey struct{}
type requiredPermKey struct{}

// WithOrgScope returns ctx carrying the organization the request acts in.
func WithOrgScope(ctx context.Context, orgID uuid.UUID) context.Context {
	return context.WithValue(ctx, orgScopeKey{}, orgID)
}

// OrgScopeFromContext reports the organization of the request, if it has one.
func OrgScopeFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(orgScopeKey{}).(uuid.UUID)
	return id, ok && id != uuid.Nil
}

// WithRequiredPermission records the permission the route's gate asked for, so
// a flat route that learns the row's organization LATER can check the same
// permission inside that organization (OrgChecker).
func WithRequiredPermission(ctx context.Context, action string) context.Context {
	return context.WithValue(ctx, requiredPermKey{}, action)
}

// RequiredPermissionFromContext returns the permission recorded by the gate.
func RequiredPermissionFromContext(ctx context.Context) (string, bool) {
	a, ok := ctx.Value(requiredPermKey{}).(string)
	return a, ok && a != ""
}

// OrgMembershipQuerier is the optional membership surface that resolves roles
// inside one organization. *gen.Queries satisfies it.
type OrgMembershipQuerier interface {
	GetActiveRolesForUserInOrg(ctx context.Context, userID, orgID uuid.UUID) ([]string, error)
}

// OrgChecker is a Checker that can also decide a permission inside an
// explicit organization (a flat route that loaded a row).
type OrgChecker interface {
	CheckInOrg(ctx context.Context, action string, orgID uuid.UUID) error
}

// CheckInOrg is Check with the organization named explicitly.
func (c *DBChecker) CheckInOrg(ctx context.Context, action string, orgID uuid.UUID) error {
	return c.Check(WithOrgScope(ctx, orgID), action, "")
}

// membershipRoles returns the roles the caller's memberships give them for
// this request: inside the request's organization when it has one and the
// querier can resolve it, otherwise across all memberships (the historical
// behaviour, kept for routes that name no organization).
func (c *DBChecker) membershipRoles(ctx context.Context, userID uuid.UUID) ([]string, error) {
	if orgID, ok := OrgScopeFromContext(ctx); ok {
		if oq, ok := c.memberships.(OrgMembershipQuerier); ok {
			return oq.GetActiveRolesForUserInOrg(ctx, userID, orgID)
		}
	}
	if c.memberships == nil {
		return nil, fmt.Errorf("permissions: no membership querier")
	}
	return c.memberships.GetActiveRolesForUser(ctx, userID)
}
