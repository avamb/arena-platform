//go:build integration

// apikey_scope_onboarding_integration_test.go — the operator routes of the
// organizer applications are for a platform superadmin only, an API key never
// carries an onboarding.* scope, and a new key carries only permissions its
// creator holds in the organization.
//
//	DATABASE_URL=... go test -tags integration -count=1 -p 1 \
//	    ./apps/backend/internal/platform/httpserver/ -run 'TestOnboardingAdmin_|TestAPIKeyCreate_'
package httpserver

import (
	"context"
	"net/http"
	"testing"
)

func TestOnboardingAdmin_SuperadminOnly(t *testing.T) {
	e := newSec1Env(t)
	org := e.org(t, "onb")
	owner := e.user(t, "owner")
	e.member(t, owner, org, "org_admin")
	ownerTok := e.token(t, owner)
	_, superTok := e.superadmin(t)

	const list = "/v1/admin/onboarding/applications"
	const settings = "/v1/admin/onboarding/settings"

	// The owner of an organization is refused, reads included.
	e.expect(t, http.StatusForbidden, http.MethodGet, list, ownerTok, "", "")
	e.expect(t, http.StatusForbidden, http.MethodGet, settings, ownerTok, "", "")
	e.expect(t, http.StatusForbidden, http.MethodPut, settings, ownerTok, "h2", `{"max_new_per_day":5}`)

	// The platform superadmin reads the queue and the settings.
	e.expect(t, http.StatusOK, http.MethodGet, list, superTok, "", "")
	e.expect(t, http.StatusOK, http.MethodGet, settings, superTok, "", "")

	// A key row that carries onboarding.* scopes (stored before the scope was
	// refused at issue time) gets nothing from them.
	key := e.key(t, org, "event.read")
	if _, err := e.srv.pgxPool.Exec(context.Background(),
		`UPDATE api_keys SET scopes = ARRAY['event.read','onboarding.review','onboarding.settings'] WHERE org_id = $1`, org); err != nil {
		t.Fatalf("set stored scopes: %v", err)
	}
	e.expect(t, http.StatusForbidden, http.MethodGet, list, key, "", "")
	e.expect(t, http.StatusForbidden, http.MethodGet, settings, key, "", "")
	e.expect(t, http.StatusForbidden, http.MethodPut, settings, key, "h2", `{"max_new_per_day":5}`)
}

func TestAPIKeyCreate_ScopesHeldByCreator(t *testing.T) {
	e := newSec1Env(t)
	org := e.org(t, "keys")
	owner := e.user(t, "owner")
	e.member(t, owner, org, "org_admin")
	ownerTok := e.token(t, owner)
	_, superTok := e.superadmin(t)
	path := "/v1/organizations/" + org.String() + "/api-keys"

	// The site key set an owner issues today still works.
	e.expect(t, http.StatusCreated, http.MethodPost, path, ownerTok, "h2",
		`{"name":"site","scopes":["event.read","event.create","session.seats.manage","import.bil24_session","promo.read"]}`)

	for _, c := range []struct {
		scopes string
		code   string
	}{
		{`["event.read","customer.import"]`, "api_key.scope_not_held"},
		{`["no_such.permission"]`, "api_key.scope_not_held"},
		{`["onboarding.review"]`, "api_key.forbidden_scope"},
	} {
		st, out := e.do(t, http.MethodPost, path, ownerTok, "h2", `{"name":"k","scopes":`+c.scopes+`}`)
		if st != http.StatusUnprocessableEntity || sec1Code(out) != c.code {
			t.Errorf("owner scopes %s: got %d %s, want 422 %s", c.scopes, st, out, c.code)
		}
	}

	// A platform superadmin is not limited by its own memberships.
	e.expect(t, http.StatusCreated, http.MethodPost, path, superTok, "h2",
		`{"name":"import","scopes":["customer.import"]}`)
	st, out := e.do(t, http.MethodPost, path, superTok, "h2", `{"name":"k","scopes":["onboarding.review"]}`)
	if st != http.StatusUnprocessableEntity || sec1Code(out) != "api_key.forbidden_scope" {
		t.Errorf("superadmin onboarding.review: got %d %s, want 422 api_key.forbidden_scope", st, out)
	}
}
