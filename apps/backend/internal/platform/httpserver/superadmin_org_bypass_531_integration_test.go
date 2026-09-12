//go:build integration

// superadmin_org_bypass_531_integration_test.go — end-to-end integration
// coverage for feature #531 (W1-S0a): a REAL platform_superadmin login (via
// POST /v1/auth/login, whose issued JWT carries no roles claim, per
// hauth/login.go) must still receive the cross-tenant organization-access
// bypass on an org-scoped route, because markSuperadminOrgAccess now resolves
// roles server-side through the same membership source
// permissions.DBChecker uses, rather than trusting the JWT roles claim.
//
// See 08_architecture/21_superadmin_org_access_parity_ru.md §3.1.
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable \
//	JWT_SIGNING_SECRET=x go.exe test -tags integration \
//	./apps/backend/internal/platform/httpserver/ -run TestSuperadminOrgBypass531_Integration
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestSuperadminOrgBypass531_Integration drives the real login flow for two
// users — a platform_superadmin and an ordinary member-of-nothing user —
// against a foreign organization neither belongs to, and asserts the
// contract from spec §3.1: superadmin + X-Admin-Reason -> 200, superadmin
// without the header -> 400 superadmin.missing_reason, ordinary user -> 403
// org.access_denied, and exactly one superadmin.organization_access audit
// row is written (only for the successful, reason-carrying request).
func TestSuperadminOrgBypass531_Integration(t *testing.T) {
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	client := ts.Client()
	ctx := context.Background()

	// A foreign organization that neither test user is a member of.
	foreignOrgID := uuid.New()
	_, err := srv.pgxPool.Exec(ctx,
		`INSERT INTO organizations (id, name, slug, country) VALUES ($1, $2, $3, 'EE')`,
		foreignOrgID, "W1-S0a Foreign Org "+foreignOrgID.String(), "w1-s0a-foreign-org-"+foreignOrgID.String(),
	)
	if err != nil {
		t.Fatalf("insert foreign org: %v", err)
	}

	superadminEmail := fmt.Sprintf("w1s0a-superadmin-%d@example.com", time.Now().UnixNano())
	plainEmail := fmt.Sprintf("w1s0a-plain-%d@example.com", time.Now().UnixNano())
	const password = "Test1234!"

	registerUser(t, client, ts.URL, superadminEmail, password)
	registerUser(t, client, ts.URL, plainEmail, password)

	// Grant platform_superadmin via a NULL-org_id user_roles row — the only
	// shape GetActiveRolesForUser unions (AGENTS.md gotcha), and therefore
	// the only shape a real login-issued JWT (empty Roles claim) can ever
	// see through the DB fallback this feature adds.
	var superadminUserID uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx,
		`SELECT id FROM users WHERE email = $1`, superadminEmail,
	).Scan(&superadminUserID); err != nil {
		t.Fatalf("look up superadmin user id: %v", err)
	}
	if _, err := srv.pgxPool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id, org_id)
SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`,
		superadminUserID,
	); err != nil {
		t.Fatalf("grant platform_superadmin: %v", err)
	}

	// The plain user needs SOME permission-granting role to reach the
	// membership gate at all (an actor with zero resolved roles is rejected
	// earlier, at the permission check, with permissions.denied rather than
	// org.access_denied) — but that role must live in a DIFFERENT org than
	// foreignOrgID, so the plain user is a genuine non-member of the org
	// under test. GetActiveRolesForUser unions active `memberships` rows
	// (org-scoped) in addition to NULL-org_id user_roles.
	var plainUserID uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx,
		`SELECT id FROM users WHERE email = $1`, plainEmail,
	).Scan(&plainUserID); err != nil {
		t.Fatalf("look up plain user id: %v", err)
	}
	ownOrgID := uuid.New()
	if _, err := srv.pgxPool.Exec(ctx,
		`INSERT INTO organizations (id, name, slug, country) VALUES ($1, $2, $3, 'EE')`,
		ownOrgID, "W1-S0a Plain User Own Org "+ownOrgID.String(), "w1-s0a-own-org-"+ownOrgID.String(),
	); err != nil {
		t.Fatalf("insert plain user's own org: %v", err)
	}
	if _, err := srv.pgxPool.Exec(ctx,
		`INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'organizer')`,
		plainUserID, ownOrgID,
	); err != nil {
		t.Fatalf("grant plain user organizer membership in own org: %v", err)
	}

	superadminToken := loginUser(t, client, ts.URL, superadminEmail, password)
	plainToken := loginUser(t, client, ts.URL, plainEmail, password)

	channelsURL := ts.URL + "/v1/organizations/" + foreignOrgID.String() + "/channels"

	// 1. Superadmin WITH X-Admin-Reason -> 200.
	resp := integDoRequestWithReason(t, client, http.MethodGet, channelsURL, superadminToken, "verify W1-S0a live bypass")
	body := integReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("superadmin+reason: expected 200, got %d, body: %s", resp.StatusCode, body)
	}

	// 2. Superadmin WITHOUT X-Admin-Reason -> 400 superadmin.missing_reason.
	resp = integDoRequestWithReason(t, client, http.MethodGet, channelsURL, superadminToken, "")
	body = integReadBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("superadmin without reason: expected 400, got %d, body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "superadmin.missing_reason") {
		t.Fatalf("superadmin without reason: expected superadmin.missing_reason in body, got: %s", body)
	}

	// 3. Ordinary non-member user -> 403 org.access_denied, bypass never fires.
	resp = integDoRequestWithReason(t, client, http.MethodGet, channelsURL, plainToken, "should not matter")
	body = integReadBody(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plain user: expected 403, got %d, body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "org.access_denied") {
		t.Fatalf("plain user: expected org.access_denied in body, got: %s", body)
	}

	// 4. Exactly one superadmin.organization_access audit row — written only
	// for the successful, reason-carrying request (case 1); cases 2 and 3
	// never reach the audit call (case 2 is rejected before the handler is
	// invoked further; auditSuperadminOrgAccess itself also short-circuits
	// on an empty reason; case 3 never receives the bypass marker at all).
	var auditCount int
	if err := srv.pgxPool.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE action = 'superadmin.organization_access' AND resource_id = $1`,
		foreignOrgID.String(),
	).Scan(&auditCount); err != nil {
		t.Fatalf("query audit_events: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected exactly one superadmin.organization_access audit row for org %s, got %d", foreignOrgID, auditCount)
	}
}

// registerUser performs a real POST /v1/auth/register call and fails the
// test on any non-2xx response.
func registerUser(t *testing.T, client *http.Client, baseURL, email, password string) {
	t.Helper()
	body := fmt.Sprintf(`{"email":%q,"password":%q,"first_name":"W1S0a","last_name":"Test"}`, email, password)
	resp := integDoRequest(t, client, http.MethodPost, baseURL+"/v1/auth/register", "", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("register %s: got %d, body: %s", email, resp.StatusCode, integReadBody(t, resp))
	}
}

// loginUser performs a real POST /v1/auth/login call and returns the access
// token.
func loginUser(t *testing.T, client *http.Client, baseURL, email, password string) string {
	t.Helper()
	body := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)
	resp := integDoRequest(t, client, http.MethodPost, baseURL+"/v1/auth/login", "", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s: got %d, body: %s", email, resp.StatusCode, integReadBody(t, resp))
	}
	data := integDecodeMap(t, resp)
	token, _ := data["access_token"].(string)
	if token == "" {
		t.Fatalf("login %s: empty access_token", email)
	}
	return token
}

// integDoRequestWithReason is integDoRequest plus an optional X-Admin-Reason
// header (empty string omits the header entirely).
func integDoRequestWithReason(t *testing.T, client *http.Client, method, url, bearer, reason string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if reason != "" {
		req.Header.Set("X-Admin-Reason", reason)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}
