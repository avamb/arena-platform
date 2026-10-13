//go:build integration

// org_admin_webhook_subscriber_manage_0140_integration_test.go — after
// migration 0140 no role named org_admin holds webhook.subscriber.manage,
// while 'admin' and platform_superadmin keep it. The subscriber registry is
// platform-wide, so an owner must not see the permission in /v1/me.
//
// Requires DATABASE_URL to point at a fully-migrated Postgres instance.
package migrations_test

import (
	"context"
	"testing"
)

func TestOrgAdminWebhookSubscriberManage0140_Integration(t *testing.T) {
	conn := connectDB(t)
	ctx := context.Background()

	holders := func(role string) int {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, `
SELECT count(*)
FROM   role_permissions rp
JOIN   roles r       ON r.id = rp.role_id
JOIN   permissions p ON p.id = rp.permission_id
WHERE  r.name = $1 AND p.name = 'webhook.subscriber.manage'
`, role).Scan(&n); err != nil {
			t.Fatalf("count %s grants: %v", role, err)
		}
		return n
	}

	if n := holders("org_admin"); n != 0 {
		t.Errorf("org_admin still holds webhook.subscriber.manage on %d role row(s); migration 0140 should have revoked it", n)
	}
	if n := holders("platform_superadmin"); n == 0 {
		t.Error("platform_superadmin lost webhook.subscriber.manage; the superadmin console needs it")
	}
	if n := holders("admin"); n == 0 {
		t.Error("admin lost webhook.subscriber.manage; migration 0140 must touch org_admin only")
	}
}
