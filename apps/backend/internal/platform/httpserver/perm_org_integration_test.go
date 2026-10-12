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
	tok, mgrTok := e.token(t, user), e.token(t, manager)

	// org-scoped route: promo.read is a manager permission, the agent has none
	e.expect(t, http.StatusForbidden, http.MethodGet, "/v1/organizations/"+orgA.String()+"/promo-codes", tok, "", "")
	e.expect(t, http.StatusOK, http.MethodGet, "/v1/organizations/"+orgB.String()+"/promo-codes", tok, "", "")
	e.expect(t, http.StatusOK, http.MethodGet, "/v1/organizations/"+orgA.String()+"/promo-codes", mgrTok, "", "")

	// flat route: the payment belongs to A, the role that passes the gate is B's
	f := newPay03Order(t, e.srv.pgxPool, orgA, "stripe")
	body := fmt.Sprintf(`{"payment_intent_id":%q,"amount":100,"currency":"EUR","reason":"perm-org"}`, f.pay)
	e.expect(t, http.StatusForbidden, http.MethodPost, "/v1/refunds", tok, "", body)
	// a real manager of A is not stopped by the permission check
	if st, out := e.do(t, http.MethodPost, "/v1/refunds", mgrTok, "", body); st == http.StatusForbidden || st == http.StatusNotFound {
		t.Errorf("a manager of the payment's own organization was refused: %d %s", st, out)
	}
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
