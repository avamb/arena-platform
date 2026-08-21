//go:build integration

// provider_config_ab41_integration_test.go — live-DB coverage for AB-41:
// checkout resolves payment_provider_configs for the org (never a default),
// with the KYB gate on live-mode configs.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestAB41Integration
package httpserver

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

func TestAB41Integration_ProviderConfigResolution(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping AB-41 integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	f := newAB49Fixture(t, ctx, pool, "general_admission")
	defer f.cleanup()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM payment_provider_configs WHERE org_id = $1`, f.orgID)
	}()
	q := gen.New(pool)

	// 1. No config at all → not configured (the pre-AB-41 silent default is gone).
	if _, cfgErr := hcheckout.ResolveProviderConfig(ctx, q, f.orgID, "stripe"); cfgErr == nil || cfgErr.Code != hcheckout.ErrCodeProviderNotConfigured {
		t.Fatalf("no config: err = %v, want %s", cfgErr, hcheckout.ErrCodeProviderNotConfigured)
	}

	// 2. A live config on an unverified org (fixture default) → KYB required.
	secrets := json.RawMessage(`{"api_key":"sk_live_x","webhook_secret":"whsec_org"}`)
	live, err := q.InsertPaymentProviderConfig(ctx, f.orgID, "stripe", "live", nil,
		json.RawMessage(`{}`), secrets, "configured", true)
	if err != nil {
		t.Fatalf("insert live config: %v", err)
	}
	if _, cfgErr := hcheckout.ResolveProviderConfig(ctx, q, f.orgID, "stripe"); cfgErr == nil || cfgErr.Code != hcheckout.ErrCodeProviderKYBRequired {
		t.Fatalf("live+unverified: err = %v, want %s", cfgErr, hcheckout.ErrCodeProviderKYBRequired)
	}

	// 3. Verify the org → the live config becomes usable and its secrets flow.
	if _, err := pool.Exec(ctx, `UPDATE organizations SET kyb_status = 'verified' WHERE id = $1`, f.orgID); err != nil {
		t.Fatalf("verify org: %v", err)
	}
	cfg, cfgErr := hcheckout.ResolveProviderConfig(ctx, q, f.orgID, "stripe")
	if cfgErr != nil {
		t.Fatalf("live+verified: %v", cfgErr)
	}
	if cfg.ID != live.ID || hcheckout.WebhookSecretFromConfig(cfg) != "whsec_org" {
		t.Fatalf("resolved cfg = %s secret=%q, want live row with whsec_org", cfg.ID, hcheckout.WebhookSecretFromConfig(cfg))
	}

	// 4. Deactivate it → inactive, never a fall-through.
	off := false
	if _, err := q.UpdatePaymentProviderConfig(ctx, live.ID, f.orgID, nil, nil, nil, "configured", &off); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if _, cfgErr := hcheckout.ResolveProviderConfig(ctx, q, f.orgID, "stripe"); cfgErr == nil || cfgErr.Code != hcheckout.ErrCodeProviderInactive {
		t.Fatalf("inactive: err = %v, want %s", cfgErr, hcheckout.ErrCodeProviderInactive)
	}

	// 5. A missing-required-fields row is not usable either.
	if _, err := q.InsertPaymentProviderConfig(ctx, f.orgID, "allpay", "test", nil,
		json.RawMessage(`{}`), json.RawMessage(`{}`), "missing_required_fields", true); err != nil {
		t.Fatalf("insert allpay: %v", err)
	}
	if _, cfgErr := hcheckout.ResolveProviderConfig(ctx, q, f.orgID, "allpay"); cfgErr == nil || cfgErr.Code != hcheckout.ErrCodeProviderMissingFields {
		t.Fatalf("missing fields: err = %v, want %s", cfgErr, hcheckout.ErrCodeProviderMissingFields)
	}
}
