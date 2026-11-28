// service_actor.go — the shared org-membership decision for organization API
// keys (spec §13.1, feature #513 / epic #466 W1-C1b).
//
// Six guards enforce org isolation: httpserver.enforceMembershipInOrg plus the
// requireOrgMembership twins in hcatalog, hiam, hpayments, hbankaccounts and
// hseating. They must all treat a service actor identically — a member of
// api_keys.org_id and of nothing else — so the decision lives here, in the one
// package every guard already imports.
//
// Placement matters as much as the rule: callers MUST consult this helper
// BEFORE their `membershipQueries == nil` branch. hpayments and hbankaccounts
// return true from that branch, so a later check would let a key of a
// different organization straight through.
package httputil

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// ServiceActorDecision resolves an org-membership check for a request that is
// not made by a user: an organization API key or a door-scanner device.
//
// notUser is true for both; the caller must then return `allowed`
// immediately and skip its membership lookup entirely (the actor's ID is not
// a user id). allowed is true only for an API key whose org_id equals orgID;
// a device is ALWAYS refused here, its own organization included (the scanner
// routes decide a device with auth.DeviceActorInOrg). When allowed is false
// this function has already written the standard 403 envelope, so the
// caller only has to stop.
//
// For a user (or anonymous) request it returns (false, false) and writes
// nothing — the caller proceeds with its usual membership lookup.
func ServiceActorDecision(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) (notUser, allowed bool) {
	notUser, allowed = auth.ServiceActorInOrg(r.Context(), orgID.String())
	if !notUser {
		return false, false
	}
	if !allowed {
		WriteJSON(w, http.StatusForbidden, ErrorEnvelope(
			"org.access_denied", "caller is not a member of this organization", r,
		))
	}
	return true, allowed
}
