//go:build integration

// perm_org_integration_test.go — PERM-ORG (review 2026-10-12): permissions are
// decided from the caller's roles IN the organization the request acts in.
//
// The gate used to resolve them from the roles of ALL the caller's
// memberships, so a manager of organization B who was only an agent of
// organization A passed A's manager-only routes. The user here is exactly
// that, with a JWT whose only authority is the membership rows (empty roles
// claim, as /v1/auth/login and the Telegram bot mint it).
package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

func TestPermOrg_ARoleInAnotherOrganizationGivesNothingHere(t *testing.T) {
	e := newSec1Env(t)
	orgA, orgB := e.org(t, "A"), e.org(t, "B")
	user := e.user(t, "mixed")
	e.member(t, user, orgA, "agent")
	e.member(t, user, orgB, "organizer")
	manager := e.user(t, "manager-a")
	e.member(t, manager, orgA, "organizer")
	owner := e.user(t, "owner-a")
	e.member(t, owner, orgA, "org_admin")
	tok, mgrTok, ownerTok := e.token(t, user), e.token(t, manager), e.token(t, owner)
	a, b := "/v1/organizations/"+orgA.String(), "/v1/organizations/"+orgB.String()
	denied := func(method, path, bearer, body string) {
		t.Helper()
		if out := e.expect(t, http.StatusForbidden, method, path, bearer, "", body); sec1Code(out) != "permissions.denied" {
			t.Errorf("%s %s: code %q, want permissions.denied", method, path, sec1Code(out))
		}
	}

	// org-scoped route: promo.read is a manager permission, the agent has none
	denied(http.MethodGet, a+"/promo-codes", tok, "")
	e.expect(t, http.StatusOK, http.MethodGet, b+"/promo-codes", tok, "", "")
	e.expect(t, http.StatusOK, http.MethodGet, a+"/promo-codes", mgrTok, "", "")
	// promo write, payment configurations (owner only)
	denied(http.MethodPost, a+"/promo-codes", tok, `{"code":"PERMORG","discount_type":"percent","discount_value":10}`)
	denied(http.MethodGet, a+"/payment-configs", tok, "")
	denied(http.MethodGet, a+"/payment-configs", mgrTok, "")
	e.expect(t, http.StatusOK, http.MethodGet, a+"/payment-configs", ownerTok, "", "")
	// The case that the union used to let through: owner of B, manager of A.
	ownerBManagerA := e.user(t, "owner-b-manager-a")
	e.member(t, ownerBManagerA, orgA, "organizer")
	e.member(t, ownerBManagerA, orgB, "org_admin")
	obmaTok := e.token(t, ownerBManagerA)
	denied(http.MethodGet, a+"/payment-configs", obmaTok, "")
	e.expect(t, http.StatusOK, http.MethodGet, b+"/payment-configs", obmaTok, "", "")

	// flat route: the payment belongs to A, the role that passes the gate is B's
	f := newPay03Order(t, e.srv.pgxPool, orgA, "stripe")
	body := fmt.Sprintf(`{"payment_intent_id":%q,"amount":100,"currency":"EUR","reason":"perm-org"}`, f.pay)
	denied(http.MethodPost, "/v1/refunds", tok, body)
	// a real manager of A creates it
	out := e.expect(t, http.StatusCreated, http.MethodPost, "/v1/refunds", mgrTok, "", body)
	var created struct {
		Refund struct {
			ID string `json:"id"`
		} `json:"refund"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.Refund.ID == "" {
		t.Fatalf("refund create answer %s: %v", out, err)
	}
	// and only someone holding refund.approve IN A approves it
	denied(http.MethodPost, "/v1/refunds/"+created.Refund.ID+"/approve", tok, "")

	// A platform superadmin is unchanged: everywhere, with a reason.
	_, superTok := e.superadmin(t)
	e.expect(t, http.StatusOK, http.MethodGet, a+"/promo-codes", superTok, "perm-org check", "")
	// An API key is unchanged: its scopes, in its own organization only.
	keyA := e.key(t, orgA, "promo.read")
	e.expect(t, http.StatusOK, http.MethodGet, a+"/promo-codes", keyA, "", "")
	if st, out := e.do(t, http.MethodGet, b+"/promo-codes", keyA, "", ""); st != http.StatusForbidden && st != http.StatusNotFound {
		t.Errorf("A's key reads B's promo codes: %d %s", st, out)
	}

	// The reverse split: manager of A, agent of B.
	rev := e.user(t, "reverse")
	e.member(t, rev, orgA, "organizer")
	e.member(t, rev, orgB, "agent")
	revTok := e.token(t, rev)
	e.expect(t, http.StatusOK, http.MethodGet, a+"/promo-codes", revTok, "", "")
	denied(http.MethodGet, b+"/promo-codes", revTok, "")

	// An org_id that is not a UUID scopes nothing and grants nothing.
	e.expect(t, http.StatusBadRequest, http.MethodGet, "/v1/organizations/not-a-uuid/promo-codes", tok, "", "")
}

// The production wiring decides per organization; a checker that cannot read
// roles inside one organization would refuse to mount (PERM-ORG).
func TestPermOrg_ProductionCheckerDecidesPerOrganization(t *testing.T) {
	srv, _ := productionIntegrationServer(t)
	dc, ok := srv.perms.(*permissions.DBChecker)
	if !ok {
		t.Fatalf("production permission checker is %T, want *permissions.DBChecker", srv.perms)
	}
	if !dc.DecidesPerOrganization() {
		t.Fatal("production permission checker cannot resolve roles per organization")
	}
}

