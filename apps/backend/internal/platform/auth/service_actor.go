// service_actor.go — helpers for organization API-key ("service") actors.
//
// A service actor is produced by the applyAuth middleware when a caller
// presents `Authorization: Bearer ak_<prefix12>_<secret43>` instead of a JWT
// (spec §13.1, feature #513 / epic #466 W1-C1b). Unlike a user actor it
// carries no roles: its permission set is exactly the api_keys.scopes array
// and its organization reach is exactly api_keys.org_id.
//
// The helpers live here (rather than in httpserver) so that the six org-auth
// guards — server_orgauth.go plus the hcatalog / hiam / hpayments /
// hbankaccounts / hseating twins — can all agree on ONE decision function
// without importing each other.
package auth

import "context"

// IsService reports whether the actor was authenticated with an organization
// API key rather than a user JWT.
func (a Actor) IsService() bool {
	return a.Type == ActorTypeService
}

// HasPermission reports whether code is present in the actor's explicit
// permission set. Only meaningful for service actors — user actors resolve
// permissions from roles and always return false here.
func (a Actor) HasPermission(code string) bool {
	for _, p := range a.Permissions {
		if p == code {
			return true
		}
	}
	return false
}

// ServiceActorInOrg is the single authority every org-membership guard
// consults before doing a database lookup.
//
// It returns:
//
//	isService — the request is authenticated with an API key, so the caller
//	            MUST NOT fall through to membership lookup (a service actor
//	            has no org_memberships row and would otherwise be denied, or
//	            worse, allowed by a permissive nil-store guard);
//	allowed   — the key's org_id equals orgID.
//
// orgID is taken as a string so this package stays free of a uuid dependency;
// callers pass orgID.String(). Comparison is exact — an empty actor OrgID
// never matches.
func ServiceActorInOrg(ctx context.Context, orgID string) (isService, allowed bool) {
	actor, ok := ActorFromContext(ctx)
	if !ok || !actor.IsService() {
		return false, false
	}
	return true, actor.OrgID != "" && actor.OrgID == orgID
}
