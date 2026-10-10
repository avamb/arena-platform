// membership_authority.go keeps the membership routes inside the caller's
// authority (SEC-1, org-isolation audit 2026-10-10, holes H1/H2).
//
// The RBAC gate in front of every route (applyAuth) checks a permission
// against the union of the roles of ALL the caller's memberships: the owner
// of any organization holds membership.grant "somewhere" and so passed the
// gate of every other organization's membership routes. Those routes also
// accepted every value of memberships_role_check from any caller, so an
// owner could grant themselves platform_superadmin. The helpers below are
// the per-request answer:
//
//   - the platform superadmin (the superadmin.read marker set by
//     markSuperadminOrgAccess, which since SEC-1 only a global user_roles row
//     or a JWT claim can produce) acts everywhere, with X-Admin-Reason;
//   - an organization API key acts only inside api_keys.org_id;
//   - a user acts only inside an organization where one of their OWN active
//     memberships carries the permission — never with authority held in
//     another organization;
//   - nobody but the superadmin grants, changes or revokes a platform-level
//     role, and platform_superadmin / platform_operator are never membership
//     roles at all: they are global (POST /v1/admin/users/{user_id}/roles).
package hiam

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/orgperm"
)

// ownerManagedMembershipRoles is the explicit allow-list of roles an
// organization's own owner (or its API key) may grant, change to or revoke.
// Everything else in memberships_role_check is put there by the platform.
var ownerManagedMembershipRoles = map[string]bool{
	"org_admin": true,
	"organizer": true,
	"agent":     true,
}

// globalOnlyRoles are platform-wide roles. They live in user_roles with a
// NULL org_id; a membership row carrying one grants nothing (the membership
// branch of GetActiveRolesForUser skips them), so no route creates one.
var globalOnlyRoles = map[string]bool{
	"platform_superadmin": true,
	"platform_operator":   true,
}

// OwnerManagedMembershipRoles exposes the allow-list for tests.
func OwnerManagedMembershipRoles() []string {
	return []string{"agent", "org_admin", "organizer"}
}

// membershipCaller is who is calling a membership route.
type membershipCaller struct {
	superadmin bool
	reason     string
}

// membershipCallerKind resolves the superadmin case, which needs no database:
// a superadmin must name a reason. ok=false means a response was written.
func membershipCallerKind(w http.ResponseWriter, r *http.Request) (membershipCaller, bool) {
	if !auth.HasSuperadminOrgAccess(r.Context()) {
		return membershipCaller{}, true
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return membershipCaller{}, false
	}
	return membershipCaller{superadmin: true, reason: reason}, true
}

// checkMembershipRole enforces the role policy for a role the caller wants to
// grant, change to (target=true) or revoke / change away from (target=false).
// codePrefix is the route family's error prefix ("membership" or
// "admin_membership"), so codes stay in each family.
func checkMembershipRole(w http.ResponseWriter, r *http.Request, c membershipCaller, role string, target bool, codePrefix string) bool {
	if !c.superadmin && !ownerManagedMembershipRoles[role] {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelopeWithDetails(
			codePrefix+".role_not_allowed",
			"only the platform superadmin manages this role", r,
			map[string]any{"field": "role", "allowed": OwnerManagedMembershipRoles()},
		))
		return false
	}
	if target && globalOnlyRoles[role] {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			codePrefix+".platform_role_is_global",
			"platform roles are global: grant them with POST /v1/admin/users/{user_id}/roles, not as a membership", r,
			map[string]any{"field": "role"},
		))
		return false
	}
	return true
}

// requireMembershipAuthority is the per-organization half of the gate: a
// non-superadmin caller must hold permission inside orgID itself. A caller
// with no membership there answers 403 org.access_denied (the
// requireOrgMembership convention), a member without the permission there
// answers 403 permissions.denied. Fails closed without a database.
func (h *Handler) requireMembershipAuthority(w http.ResponseWriter, r *http.Request, c membershipCaller, orgID uuid.UUID, permission string) bool {
	if c.superadmin {
		return true
	}
	// Organization API keys: the RBAC gate already checked the key's scopes;
	// their reach is exactly api_keys.org_id.
	if isService, allowed := httputil.ServiceActorDecision(w, r, orgID); isService {
		return allowed
	}
	denied := func() bool {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"org.access_denied", "caller is not a member of this organization", r,
		))
		return false
	}
	if h.membershipQueries == nil || h.membershipQueries.DB() == nil {
		return denied()
	}
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		return denied()
	}
	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		return denied()
	}
	member, allowed, err := orgperm.Check(r.Context(), h.membershipQueries, userID, orgID, permission)
	if err != nil {
		h.logger.Error("membership: authority lookup failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"org.membership_check_failed", "failed to verify org membership", r,
		))
		return false
	}
	if !member {
		return denied()
	}
	if allowed {
		return true
	}
	httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelopeWithDetails(
		"permissions.denied", "caller lacks this permission in this organization", r,
		map[string]any{"permission": permission},
	))
	return false
}

// requirePlatformSuperadmin guards the admin console's organization routes
// (POST/PATCH /v1/admin/organizations, sender-dns, archive): they are the
// platform superadmin's, whatever org.* permission an owner or an API key
// holds. Owners edit their own organization through /v1/organizations/{id},
// which has the membership check and the KYB guard.
func requirePlatformSuperadmin(w http.ResponseWriter, r *http.Request) bool {
	if auth.HasSuperadminOrgAccess(r.Context()) {
		return true
	}
	httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
		"superadmin.required", "this route is reserved for the platform superadmin", r,
	))
	return false
}

// RequirePlatformSuperadmin is the exported form for the httpserver shims
// (sender-dns lives outside hiam).
func RequirePlatformSuperadmin(w http.ResponseWriter, r *http.Request) bool {
	return requirePlatformSuperadmin(w, r)
}

// requireOwnerManagedRow refuses a non-superadmin change to a membership row
// whose CURRENT role the platform put there (network_operator and the like).
// A row missing from orgID answers the route's own 404.
func (h *Handler) requireOwnerManagedRow(w http.ResponseWriter, r *http.Request, c membershipCaller, membershipID, orgID uuid.UUID) bool {
	if c.superadmin {
		return true
	}
	row, err := h.membershipQueries.GetMembershipByID(r.Context(), membershipID, orgID)
	if err != nil || row.Status != "active" {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"admin_membership.not_found",
			"no active membership matches this id within the organization", r,
		))
		return false
	}
	return checkMembershipRole(w, r, c, row.Role, false, "admin_membership")
}
