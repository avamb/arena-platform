// memberships_network_operator_203_test.go — feature #203.
//
// Asserts that `network_operator` exists in the role enum/constants and
// the role validation path, is seeded by migration 0042, and is treated
// as distinct from `platform_operator` (different validation slot,
// different string identity, both still accepted independently).
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — role enum/constants contain network_operator
// ─────────────────────────────────────────────────────────────────────────────

func TestNetworkOperator203_RoleExistsInValidMembershipRoles(t *testing.T) {
	t.Parallel()
	if !validMembershipRoles["network_operator"] {
		t.Fatalf("validMembershipRoles: expected network_operator to be accepted")
	}
}

func TestNetworkOperator203_RoleAppearsInMembershipRoleList(t *testing.T) {
	t.Parallel()
	list := membershipRoleList()
	found := false
	for _, r := range list {
		if r == "network_operator" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("membershipRoleList(): expected network_operator in %v", list)
	}
}

// network_operator and platform_operator MUST both be present and
// distinct: same map => different keys; same list => different slots.
// This is the explicit "distinct from platform_operator" assertion from
// feature #203's acceptance steps.
func TestNetworkOperator203_DistinctFromPlatformOperator(t *testing.T) {
	t.Parallel()

	if !validMembershipRoles["platform_operator"] {
		t.Fatalf("validMembershipRoles: platform_operator must remain valid (unchanged by #203)")
	}
	if !validMembershipRoles["network_operator"] {
		t.Fatalf("validMembershipRoles: network_operator must be valid after #203")
	}

	list := membershipRoleList()
	var sawNetwork, sawPlatform bool
	for _, r := range list {
		switch r {
		case "network_operator":
			sawNetwork = true
		case "platform_operator":
			sawPlatform = true
		}
	}
	if !sawNetwork || !sawPlatform {
		t.Fatalf("membershipRoleList(): both roles must be present (network=%v, platform=%v) in %v",
			sawNetwork, sawPlatform, list)
	}
}

// platform_superadmin semantics must not be weakened: still validated and
// still a global role distinct from network_operator.
func TestNetworkOperator203_PlatformSuperadminUnchanged(t *testing.T) {
	t.Parallel()
	if !validMembershipRoles["platform_superadmin"] {
		t.Fatalf("validMembershipRoles: platform_superadmin must remain valid after #203")
	}
	list := membershipRoleList()
	found := false
	for _, r := range list {
		if r == "platform_superadmin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("membershipRoleList(): platform_superadmin must remain present (got %v)", list)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — role validation accepts network_operator end-to-end
// ─────────────────────────────────────────────────────────────────────────────

func TestNetworkOperator203_GrantMembershipAcceptsNetworkOperator(t *testing.T) {
	t.Parallel()
	s := buildMembershipServer(t)
	stub, _ := auth.NewStubProvider(auth.StubConfig{
		Secret:  "test-secret-which-is-long-enough-for-hs256",
		Issuer:  "arena-test",
		Enabled: true,
	})

	orgID := uuid.New().String()
	payload, _ := json.Marshal(map[string]string{
		"user_id": uuid.New().String(),
		"role":    "network_operator",
	})
	r := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+orgID+"/members",
		bytes.NewReader(payload))
	r.Header.Set("Content-Type", "application/json")
	tok, _, _ := stub.IssueToken(context.Background(), auth.IssueRequest{
		ActorID: uuid.New().String(), Roles: []string{"admin"},
	})
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	// Must NOT be 400 invalid_role (the validation step). Anything from a
	// successful insert (201) to a stub-database failure (5xx) is fine —
	// what matters is that role validation passed.
	if w.Code == http.StatusBadRequest {
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if errObj, ok := body["error"].(map[string]any); ok {
			if code, _ := errObj["code"].(string); code == "membership.invalid_role" {
				t.Fatalf("network_operator must pass role validation; got %s body=%s",
					code, w.Body.String())
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — seed/migration assertions for 0042_network_operator_role.sql
// ─────────────────────────────────────────────────────────────────────────────

func TestNetworkOperator203_MigrationFileExistsAndSeedsRole(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0042_network_operator_role.sql")
	for _, want := range []string{
		"network_operator",
		"INSERT INTO roles",
		"-- +goose Up",
		"-- +goose Down",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("0042_network_operator_role.sql: expected %q but not found", want)
		}
	}
}

func TestNetworkOperator203_MigrationExtendsRoleCheckConstraint(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0042_network_operator_role.sql")
	if !strings.Contains(content, "memberships_role_check") {
		t.Errorf("0042_network_operator_role.sql: must reference memberships_role_check constraint")
	}
	// All pre-existing roles must remain in the new CHECK list — the
	// migration must NOT weaken platform_operator / platform_superadmin.
	for _, r := range []string{
		"organizer", "agent", "platform_operator",
		"external_ticketing_operator", "platform_superadmin",
		"network_operator",
	} {
		want := "'" + r + "'"
		if !strings.Contains(content, want) {
			t.Errorf("0042_network_operator_role.sql: expected literal %q in extended CHECK", want)
		}
	}
}

func TestNetworkOperator203_MigrationDoesNotDropPriorRoles(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0042_network_operator_role.sql")
	// Sanity: the migration must not DELETE FROM roles for any of the
	// previously-seeded global roles. (The Down path may DELETE the new
	// row, but only for network_operator.)
	for _, forbidden := range []string{
		"DELETE FROM roles WHERE name = 'platform_operator'",
		"DELETE FROM roles WHERE name = 'platform_superadmin'",
		"DELETE FROM roles WHERE name = 'organizer'",
		"DELETE FROM roles WHERE name = 'agent'",
		"DELETE FROM roles WHERE name = 'external_ticketing_operator'",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("0042_network_operator_role.sql: must not contain %q — would weaken existing roles", forbidden)
		}
	}
}
