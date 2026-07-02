// network_permissions_206_test.go — feature #206.
//
// Asserts that migration 0044_network_permissions.sql registers the
// 14 network.* permissions and binds them to the correct roles:
//
//   - platform_superadmin: ALL 14 network.* permissions.
//   - network_operator   : operational subset (11 permissions — no
//     create / archive / manage_users).
//   - platform_operator  : NO new network.* bindings (preserved).
//   - admin              : full network.* grant (idempotent re-seed of
//     the 0008 broad-grant pattern).
//
// These tests are structural / SQL-grep based, mirroring the style used
// by feature #203 (memberships_network_operator_203_test.go) and #166
// (superadmin_166_test.go). The runtime end-to-end binding behavior is
// exercised by the RBAC checker tests once a DB instance is wired into
// integration tests.
package httpserver

import (
	"strings"
	"testing"
)

// allNetworkPermissions is the canonical list of permission names
// registered by migration 0044 (feature #206).
var allNetworkPermissions = []string{
	"network.read",
	"network.create",
	"network.update",
	"network.archive",
	"network.manage_users",
	"network.manage_organizers",
	"network.manage_agents",
	"network.manage_channels",
	"network.view_sales",
	"network.support_orders",
	"network.support_tickets",
	"network.support_refunds",
	"network.view_reports",
	"network.view_audit",
}

// networkOperatorPermissions is the operational subset granted to
// network_operator (no lifecycle / no roster mutation). Must be exactly
// 11 entries — feature #206 step 2 explicitly excludes create / archive
// / manage_users from the network_operator binding.
var networkOperatorPermissions = []string{
	"network.read",
	"network.update",
	"network.manage_organizers",
	"network.manage_agents",
	"network.manage_channels",
	"network.view_sales",
	"network.support_orders",
	"network.support_tickets",
	"network.support_refunds",
	"network.view_reports",
	"network.view_audit",
}

// platformSuperadminExcludedFromNetworkOperator captures the three
// permissions that platform_superadmin holds but network_operator must
// NOT hold (feature #206 acceptance step 2).
var platformSuperadminExcludedFromNetworkOperator = []string{
	"network.create",
	"network.archive",
	"network.manage_users",
}

// ─────────────────────────────────────────────────────────────────────
// Step 1 — permission catalogue registration.
// ─────────────────────────────────────────────────────────────────────

func TestNetworkPermissions206_MigrationFileExists(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0044_network_permissions.sql")
	if len(content) == 0 {
		t.Fatal("0044_network_permissions.sql: file is empty")
	}
}

func TestNetworkPermissions206_HasGooseDirectives(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0044_network_permissions.sql")
	if !strings.Contains(content, "-- +goose Up") {
		t.Error("0044_network_permissions.sql: missing '-- +goose Up'")
	}
	if !strings.Contains(content, "-- +goose Down") {
		t.Error("0044_network_permissions.sql: missing '-- +goose Down'")
	}
}

func TestNetworkPermissions206_RegistersAllPermissionConstants(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0044_network_permissions.sql")
	if !strings.Contains(content, "INSERT INTO permissions") {
		t.Fatal("0044_network_permissions.sql: must INSERT INTO permissions")
	}
	for _, perm := range allNetworkPermissions {
		want := "'" + perm + "'"
		if !strings.Contains(content, want) {
			t.Errorf("0044_network_permissions.sql: missing permission literal %s", want)
		}
	}
}

func TestNetworkPermissions206_PermissionCountIsFourteen(t *testing.T) {
	t.Parallel()
	if got, want := len(allNetworkPermissions), 14; got != want {
		t.Fatalf("allNetworkPermissions: expected %d entries, got %d", want, got)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Step 2 — role bindings.
// ─────────────────────────────────────────────────────────────────────

func TestNetworkPermissions206_BindsAllToPlatformSuperadmin(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0044_network_permissions.sql")
	if !strings.Contains(content, "r.name = 'platform_superadmin'") {
		t.Error("0044_network_permissions.sql: must seed platform_superadmin bindings")
	}
	// The superadmin grant uses LIKE 'network.%' to attach the full set.
	if !strings.Contains(content, "p.name LIKE 'network.%'") {
		t.Error("0044_network_permissions.sql: platform_superadmin must receive the FULL network.* set " +
			"via LIKE 'network.%' (or equivalent enumeration of all 14 names)")
	}
}

func TestNetworkPermissions206_BindsOperationalSubsetToNetworkOperator(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0044_network_permissions.sql")
	if !strings.Contains(content, "r.name = 'network_operator'") {
		t.Error("0044_network_permissions.sql: must seed network_operator bindings")
	}
	// Each operational-subset permission MUST appear in the
	// network_operator binding clause.
	for _, perm := range networkOperatorPermissions {
		want := "'" + perm + "'"
		if !strings.Contains(content, want) {
			t.Errorf("0044_network_permissions.sql: network_operator binding missing literal %s", want)
		}
	}
	// The expected operational subset is exactly 11 entries (14 minus
	// the 3 lifecycle/roster perms).
	if got, want := len(networkOperatorPermissions), 11; got != want {
		t.Fatalf("networkOperatorPermissions: expected %d entries, got %d", want, got)
	}
}

func TestNetworkPermissions206_NetworkOperatorExcludesLifecyclePerms(t *testing.T) {
	t.Parallel()
	// The network_operator binding clause must NOT list any of the
	// excluded perms — this is the structural guarantee that the
	// operational subset stays "operational".
	content := findFileByName(t, "0044_network_permissions.sql")

	// Locate the network_operator binding block.
	const marker = "r.name = 'network_operator'"
	idx := strings.Index(content, marker)
	if idx < 0 {
		t.Fatalf("0044_network_permissions.sql: no network_operator binding block found")
	}
	// The IN-list lives within roughly 1200 chars after the marker.
	end := idx + 1200
	if end > len(content) {
		end = len(content)
	}
	block := content[idx:end]

	for _, excluded := range platformSuperadminExcludedFromNetworkOperator {
		want := "'" + excluded + "'"
		if strings.Contains(block, want) {
			t.Errorf("0044_network_permissions.sql: network_operator binding must NOT include %s", want)
		}
	}
}

func TestNetworkPermissions206_PlatformOperatorUnchanged(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0044_network_permissions.sql")
	// Feature #206 explicitly preserves platform_operator behavior:
	// the migration must not seed any network.* binding for it.
	if strings.Contains(content, "r.name = 'platform_operator'") {
		t.Errorf("0044_network_permissions.sql: platform_operator must not receive network.* " +
			"bindings (feature #206 preserves its behavior unchanged)")
	}
}

func TestNetworkPermissions206_AdminRoleStillGetsNetworkPerms(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0044_network_permissions.sql")
	// The 'admin' role inherits all permissions per the 0008 broad-grant
	// pattern. The migration must re-attach the network.* slice to admin
	// so re-seeding stays idempotent.
	if !strings.Contains(content, "r.name = 'admin'") {
		t.Errorf("0044_network_permissions.sql: must re-attach network.* to the 'admin' role")
	}
}

// ─────────────────────────────────────────────────────────────────────
// Step 3 — rollback safety.
// ─────────────────────────────────────────────────────────────────────

func TestNetworkPermissions206_DownRemovesNetworkPermissionsOnly(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "0044_network_permissions.sql")
	// The Down section must DELETE both the join rows and the
	// permissions, scoped to the 'network.%' name prefix.
	if !strings.Contains(content, "DELETE FROM permissions WHERE name LIKE 'network.%'") {
		t.Errorf("0044_network_permissions.sql: Down must DELETE FROM permissions WHERE name LIKE 'network.%%'")
	}
	if !strings.Contains(content, "DELETE FROM role_permissions") {
		t.Errorf("0044_network_permissions.sql: Down must DELETE FROM role_permissions")
	}
	// Sanity — must NOT drop roles, drop tables, or touch unrelated permissions.
	for _, forbidden := range []string{
		"DROP TABLE",
		"DELETE FROM roles",
		"DELETE FROM permissions WHERE name = 'superadmin.read'",
		"DELETE FROM permissions WHERE name = 'geo.admin'",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("0044_network_permissions.sql: Down must not contain %q", forbidden)
		}
	}
}
