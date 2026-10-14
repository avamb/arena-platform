//go:build integration

package httpserver

import (
	"encoding/json"
	"net/http"
	"testing"
)

type orgMeBody struct {
	OrgName            string   `json:"org_name"`
	CallerType         string   `json:"caller_type"`
	Role               string   `json:"role"`
	MembershipRoles    []string `json:"membership_roles"`
	Roles              []string `json:"roles"`
	Permissions        []string `json:"permissions"`
	PlatformSuperadmin bool     `json:"platform_superadmin"`
}

func decodeOrgMe(t *testing.T, raw string) orgMeBody {
	t.Helper()
	var b orgMeBody
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return b
}

func hasPerm(list []string, want string) bool {
	for _, p := range list {
		if p == want {
			return true
		}
	}
	return false
}

// A1: GET /v1/organizations/{org_id}/me answers the caller's access INSIDE
// that organization, and agrees with the per-organization gates. The user is
// an agent of A and a manager (organizer) of B.
func TestOrgMe_RealRouter_RoleAndPermissionsPerOrganization(t *testing.T) {
	e := newSec1Env(t)
	orgA, orgB, orgC := e.org(t, "A"), e.org(t, "B"), e.org(t, "C")
	user := e.user(t, "mixed")
	e.member(t, user, orgA, "agent")
	e.member(t, user, orgB, "organizer")
	tok := e.token(t, user)
	a, b, c := "/v1/organizations/"+orgA.String(), "/v1/organizations/"+orgB.String(), "/v1/organizations/"+orgC.String()

	inA := decodeOrgMe(t, e.expect(t, http.StatusOK, http.MethodGet, a+"/me", tok, "", ""))
	inB := decodeOrgMe(t, e.expect(t, http.StatusOK, http.MethodGet, b+"/me", tok, "", ""))

	if inA.Role != "agent" || inA.CallerType != "user" || inA.PlatformSuperadmin {
		t.Errorf("A = %+v; want role agent, caller user", inA)
	}
	if inB.Role != "organizer" {
		t.Errorf("B role = %q; want organizer", inB.Role)
	}
	// promo.read is a manager permission (migration 0129): listed in B only.
	if hasPerm(inA.Permissions, "promo.read") {
		t.Error("A lists promo.read, which only the manager role of B gives")
	}
	if !hasPerm(inB.Permissions, "promo.read") {
		t.Error("B does not list promo.read for a manager")
	}
	// The list agrees with the gate in both organizations.
	if code, _ := e.do(t, http.MethodGet, a+"/promo-codes", tok, "", ""); code != http.StatusForbidden {
		t.Errorf("promo-codes in A = %d; want 403, as /me says", code)
	}
	if code, _ := e.do(t, http.MethodGet, b+"/promo-codes", tok, "", ""); code != http.StatusOK {
		t.Errorf("promo-codes in B = %d; want 200, as /me says", code)
	}
	// Owner-only permissions stay away from a manager.
	if hasPerm(inB.Permissions, "payment_config.read") || hasPerm(inB.Permissions, "membership.grant") {
		t.Errorf("a manager lists an owner permission: %v", inB.Permissions)
	}

	// Not a member of C.
	if out := e.expect(t, http.StatusForbidden, http.MethodGet, c+"/me", tok, "", ""); sec1Code(out) != "org.access_denied" {
		t.Errorf("C: code %q; want org.access_denied", sec1Code(out))
	}

	// A membership row that carries only a platform role is not a membership:
	// the real guard refuses it like a stranger (the checker grants nothing
	// from such a row either).
	e.member(t, user, orgC, "platform_operator")
	if out := e.expect(t, http.StatusForbidden, http.MethodGet, c+"/me", tok, "", ""); sec1Code(out) != "org.access_denied" {
		t.Errorf("C with a platform_operator row: code %q; want org.access_denied", sec1Code(out))
	}

	// An organization API key: its own organization only, permissions = scopes.
	key := e.key(t, orgB, "order.read", "event.read")
	k := decodeOrgMe(t, e.expect(t, http.StatusOK, http.MethodGet, b+"/me", key, "", ""))
	if k.CallerType != "api_key" || k.Role != "" || !hasPerm(k.Permissions, "order.read") || hasPerm(k.Permissions, "promo.read") {
		t.Errorf("key = %+v", k)
	}
	e.expect(t, http.StatusForbidden, http.MethodGet, a+"/me", key, "", "")

	// The owner of A sees the owner's permissions.
	owner := e.user(t, "owner-a")
	e.member(t, owner, orgA, "org_admin")
	o := decodeOrgMe(t, e.expect(t, http.StatusOK, http.MethodGet, a+"/me", e.token(t, owner), "", ""))
	if o.Role != "org_admin" || !hasPerm(o.Permissions, "membership.grant") {
		t.Errorf("owner = %+v", o)
	}

	// A platform superadmin who is not a member: allowed, flagged, no reason needed.
	_, saTok := e.superadmin(t)
	s := decodeOrgMe(t, e.expect(t, http.StatusOK, http.MethodGet, c+"/me", saTok, "", ""))
	if !s.PlatformSuperadmin || s.Role != "" || !hasPerm(s.Permissions, "superadmin.read") {
		t.Errorf("superadmin = %+v", s)
	}

	// No token at all.
	e.expect(t, http.StatusUnauthorized, http.MethodGet, a+"/me", "", "", "")
}
