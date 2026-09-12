//go:build integration

// superadmin_permission_parity_532_integration_test.go — feature #532
// (W1-S0b, spec §3.2): after migration 0100 closes the permission-grant
// gap, GET /v1/me for a real, freshly logged-in platform_superadmin must
// list every permission seeded after 0071 that used to be missing —
// api_key.manage is the concrete regression named by the spec (the
// API-keys admin tab 403'd before this feature).
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable \
//	JWT_SIGNING_SECRET=x go.exe test -tags integration \
//	./apps/backend/internal/platform/httpserver/ -run TestSuperadminPermissionParity532_MeListsApiKeyManage
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSuperadminPermissionParity532_MeListsApiKeyManage(t *testing.T) {
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	client := ts.Client()
	ctx := context.Background()

	email := fmt.Sprintf("w1s0b-superadmin-%d@example.com", time.Now().UnixNano())
	const password = "Test1234!"
	registerUser(t, client, ts.URL, email, password)

	var userID uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&userID); err != nil {
		t.Fatalf("look up user id: %v", err)
	}
	if _, err := srv.pgxPool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id, org_id)
SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`,
		userID,
	); err != nil {
		t.Fatalf("grant platform_superadmin: %v", err)
	}

	token := loginUser(t, client, ts.URL, email, password)

	resp := integDoRequest(t, client, http.MethodGet, ts.URL+"/v1/me", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/me: got %d, body: %s", resp.StatusCode, integReadBody(t, resp))
	}
	data := integDecodeMap(t, resp)

	permsRaw, _ := data["permissions"].([]any)
	perms := make(map[string]bool, len(permsRaw))
	for _, p := range permsRaw {
		if s, ok := p.(string); ok {
			perms[s] = true
		}
	}

	for _, want := range []string{"api_key.manage", "import.bil24_session", "order.read", "order.write", "customer.read", "customer.import"} {
		if !perms[want] {
			t.Errorf("GET /v1/me for a real platform_superadmin login is missing %q (got %d permissions total)", want, len(perms))
		}
	}
}
