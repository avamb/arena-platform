//go:build integration

package httpserver

import (
	"context"
	"fmt"
	"testing"
)

// A promo code may only name categories of its own organization.
func TestOrgIsolation_PromoCodeTiersMustBelongToTheOrganization(t *testing.T) {
	e := newSweepEnv(t)
	orgA := e.newOrg(t, "A")
	wA := e.newWorld(t, orgA, e.orgC)
	wB := e.newWorld(t, e.orgB, e.orgB)

	path := fmt.Sprintf("/v1/organizations/%s/promo-codes", e.orgB)
	body := func(code, tier string) string {
		return fmt.Sprintf(`{"code":%q,"discount_type":"percent","discount_value":10,"applies_to_tier_ids":[%q]}`, code, tier)
	}

	status, out := e.do(t, "POST", path, e.owner, body("SECFOREIGN", wA.tier.String()))
	if status != 422 {
		t.Fatalf("foreign tier: got %d %v, want 422", status, out)
	}
	var n int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM promo_codes WHERE org_id = $1`, e.orgB).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a refused promo code was stored (%d rows)", n)
	}
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM promo_codes WHERE org_id = $1`, e.orgB) })

	if status, out := e.do(t, "POST", path, e.owner, body("SECOWN", wB.tier.String())); status != 201 {
		t.Fatalf("own tier: got %d %v, want 201", status, out)
	}
}
