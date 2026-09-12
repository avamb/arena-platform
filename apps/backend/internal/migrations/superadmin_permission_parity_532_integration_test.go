//go:build integration

// superadmin_permission_parity_532_integration_test.go — feature #532
// (W1-S0b): after migration 0100, platform_superadmin must hold every row
// currently in the permissions table. This is the live-database counterpart
// to the static guardrail in superadmin_permission_parity_532_test.go, which
// only checks migrations numbered above 0100 grant their own new
// permissions; this test catches drift against the CURRENT, already-migrated
// schema regardless of which migration introduced a gap.
//
// Requires DATABASE_URL to point at a fully-migrated Postgres instance (the
// shared dev-stand at localhost:55432 in this repo's conventions).
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable \
//	go.exe test -tags integration ./apps/backend/internal/migrations/... -run TestSuperadminPermissionParity532_Integration
package migrations_test

import (
	"context"
	"sort"
	"testing"
)

func TestSuperadminPermissionParity532_Integration(t *testing.T) {
	conn := connectDB(t)
	ctx := context.Background()

	var totalPermissions int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM permissions`).Scan(&totalPermissions); err != nil {
		t.Fatalf("count permissions: %v", err)
	}
	if totalPermissions == 0 {
		t.Fatal("permissions table is empty; is DATABASE_URL pointed at a migrated schema?")
	}

	var superadminGrants int
	if err := conn.QueryRow(ctx, `
SELECT count(*)
FROM   role_permissions rp
JOIN   roles r ON r.id = rp.role_id
WHERE  r.name = 'platform_superadmin' AND r.org_id IS NULL
`).Scan(&superadminGrants); err != nil {
		t.Fatalf("count platform_superadmin role_permissions: %v", err)
	}

	if superadminGrants != totalPermissions {
		missing := missingSuperadminPermissions(t, ctx)
		t.Fatalf(
			"platform_superadmin has %d/%d permissions; missing: %v — a new "+
				"permission migration did not grant platform_superadmin in the "+
				"same file (AGENTS.md rule, feature #532)",
			superadminGrants, totalPermissions, missing,
		)
	}
}

// missingSuperadminPermissions returns the sorted list of permission names
// platform_superadmin does NOT currently hold, for a readable failure
// message.
func missingSuperadminPermissions(t *testing.T, ctx context.Context) []string {
	t.Helper()
	conn := connectDB(t)
	rows, err := conn.Query(ctx, `
SELECT p.name
FROM   permissions p
WHERE  NOT EXISTS (
    SELECT 1
    FROM   role_permissions rp
    JOIN   roles r ON r.id = rp.role_id
    WHERE  rp.permission_id = p.id AND r.name = 'platform_superadmin' AND r.org_id IS NULL
)
`)
	if err != nil {
		t.Fatalf("query missing permissions: %v", err)
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan missing permission: %v", err)
		}
		missing = append(missing, name)
	}
	sort.Strings(missing)
	return missing
}
