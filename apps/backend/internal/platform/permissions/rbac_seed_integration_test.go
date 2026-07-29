//go:build integration

package permissions_test

import (
	"context"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/tests/pgtest"
)

// TestAB1_FreshTenantRoleMatrix applies the complete embedded migration set to
// a fresh PostgreSQL database, provisions a tenant org_admin assignment, and
// verifies that each catalog/config/media surface exposed in the admin UI has
// its required grant.  It also locks the least-privilege organizer/agent
// matrix so future seed changes cannot silently hand either role payment
// configuration access.
func TestAB1_FreshTenantRoleMatrix(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()

	orgID := uuid.New()
	userID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO organizations (id, name, slug, country) VALUES ($1, 'AB1 Fresh Tenant', 'ab1-fresh-tenant', 'EE')`,
		orgID,
	)
	if err == nil {
		_, err = pool.Exec(ctx,
			`INSERT INTO users (id, email, password_hash) VALUES ($1, 'ab1-org-admin@example.test', '$2a$12$test')`,
			userID,
		)
	}
	if err == nil {
		_, err = pool.Exec(ctx,
			`INSERT INTO user_roles (user_id, role_id, org_id)
SELECT $1, id, $2 FROM roles WHERE name = 'org_admin' AND org_id IS NULL`,
			userID, orgID,
		)
	}
	if err != nil {
		t.Fatalf("provision fresh org_admin: %v", err)
	}

	assertRolePermissions(t, ctx, pool, "org_admin", []string{
		"venue.create", "venue.read", "venue.update", "venue.delete",
		"channel.create", "channel.read", "channel.update", "channel.delete",
		"event.create", "event.read", "event.update", "event.delete", "event.publish",
		"payment_config.read", "payment_config.write",
		"media.write", "media.read", "media.delete",
	})
	assertRolePermissions(t, ctx, pool, "organizer", []string{
		"venue.read", "channel.read",
		"event.create", "event.read", "event.update", "event.delete", "event.publish",
		"media.write", "media.read", "media.delete",
	})
	assertRolePermissions(t, ctx, pool, "agent", []string{
		"venue.read", "channel.read", "event.read", "media.read",
	})
	assertRoleLacksPermissions(t, ctx, pool, "organizer", []string{"payment_config.write"})
	assertRoleLacksPermissions(t, ctx, pool, "agent", []string{
		"payment_config.write", "event.create", "event.update", "event.publish", "media.write",
	})
}

func assertRolePermissions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, role string, want []string) {
	t.Helper()
	got := rolePermissions(t, ctx, pool, role)
	for _, permission := range want {
		if !got[permission] {
			t.Errorf("role %q missing required permission %q; got %v", role, permission, sortedPermissions(got))
		}
	}
}

func assertRoleLacksPermissions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, role string, denied []string) {
	t.Helper()
	got := rolePermissions(t, ctx, pool, role)
	for _, permission := range denied {
		if got[permission] {
			t.Errorf("role %q must not receive %q", role, permission)
		}
	}
}

func rolePermissions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, role string) map[string]bool {
	t.Helper()
	rows, err := pool.Query(ctx, `
SELECT p.name
FROM role_permissions rp
JOIN roles r ON r.id = rp.role_id
JOIN permissions p ON p.id = rp.permission_id
WHERE r.name = $1 AND r.org_id IS NULL`, role)
	if err != nil {
		t.Fatalf("query permissions for %q: %v", role, err)
	}
	defer rows.Close()
	got := make(map[string]bool)
	for rows.Next() {
		var permission string
		if err := rows.Scan(&permission); err != nil {
			t.Fatalf("scan permission for %q: %v", role, err)
		}
		got[permission] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate permissions for %q: %v", role, err)
	}
	return got
}

func sortedPermissions(perms map[string]bool) []string {
	out := make([]string, 0, len(perms))
	for permission := range perms {
		out = append(out, permission)
	}
	sort.Strings(out)
	return out
}
