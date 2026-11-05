//go:build integration

package httpserver

// shop_module_fixture_integration_test.go — since migration 0198 the shop is a
// paid module (spec 08_architecture/61): an organization without a shop
// period answers 404 shop.module_not_enabled on every shop route and has no
// storefront. Every shop fixture connects the module here, the way the
// superadmin's POST /v1/admin/organizations/{org_id}/modules/shop would, and
// removes the periods before the fixture deletes its organizations.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// connectShopModuleForTest opens an open-ended shop period for every
// organization, starting a month ago, free (individual price 0). Its cleanup
// is registered now, so it runs BEFORE a fixture cleanup registered earlier
// (t.Cleanup is LIFO): org_module_periods references organizations.
func connectShopModuleForTest(t *testing.T, pool *pgxpool.Pool, orgIDs ...uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	startsOn := time.Now().UTC().AddDate(0, -1, 0).Format("2006-01-02")
	for _, id := range orgIDs {
		if _, err := pool.Exec(ctx, `INSERT INTO org_module_periods (org_id, module, starts_on, monthly_price_minor, currency, reason)
			VALUES ($1, 'shop', $2::date, 0, 'EUR', 'integration test fixture')`, id, startsOn); err != nil {
			t.Fatalf("connect shop module for %s: %v", id, err)
		}
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM org_module_periods WHERE org_id = ANY($1)`, orgIDs); err != nil {
			t.Logf("shop module cleanup: %v", err)
		}
	})
}
